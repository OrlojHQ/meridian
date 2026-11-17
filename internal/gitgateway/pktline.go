package gitgateway

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
)

// maxPktPayload is Git's largest pkt-line payload (LARGE_PACKET_MAX - 4).
const maxPktPayload = 65516

type command struct {
	old, new, ref string
}

type commandSection struct {
	// raw is every byte read, through the terminating flush, forwarded as is.
	raw          []byte
	shallow      []string
	commands     []command
	capabilities []string
}

// readPkt reads one pkt-line. A flush returns flush true and no payload.
func readPkt(reader io.Reader) (payload []byte, raw []byte, flush bool, err error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, nil, false, err
	}
	length, err := strconv.ParseUint(string(header[:]), 16, 16)
	if err != nil {
		return nil, nil, false, errors.New("invalid pkt-line length")
	}
	if length == 0 {
		return nil, header[:], true, nil
	}
	if length < 4 || length-4 > maxPktPayload {
		return nil, nil, false, errors.New("invalid pkt-line length")
	}
	payload = make([]byte, length-4)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, nil, false, err
	}
	return payload, append(header[:], payload...), false, nil
}

// readCommandSection reads a receive-pack request up to its first flush.
// It reads at most maxCommandSection bytes and leaves the pack unread.
func readCommandSection(body io.Reader) (commandSection, error) {
	limited := &io.LimitedReader{R: body, N: maxCommandSection}
	var section commandSection
	for {
		payload, raw, flush, err := readPkt(limited)
		if err != nil {
			return commandSection{}, errors.New("invalid or oversized push command section")
		}
		section.raw = append(section.raw, raw...)
		if flush {
			return section, nil
		}
		line := strings.TrimSuffix(string(payload), "\n")
		if oid, ok := strings.CutPrefix(line, "shallow "); ok && len(section.commands) == 0 {
			if !validObjectID(oid) {
				return commandSection{}, errors.New("invalid shallow line")
			}
			section.shallow = append(section.shallow, oid)
			continue
		}
		if strings.HasPrefix(line, "push-cert") {
			return commandSection{}, errors.New("signed pushes are not accepted")
		}
		if len(section.commands) == 0 {
			var capabilities string
			line, capabilities, _ = strings.Cut(line, "\x00")
			section.capabilities = strings.Fields(capabilities)
		} else if strings.Contains(line, "\x00") {
			return commandSection{}, errors.New("capabilities after the first command")
		}
		fields := strings.Split(line, " ")
		if len(fields) != 3 || !validObjectID(fields[0]) || !validObjectID(fields[1]) || fields[2] == "" {
			return commandSection{}, errors.New("invalid push command")
		}
		section.commands = append(section.commands, command{old: fields[0], new: fields[1], ref: fields[2]})
	}
}

// checkCommand allows exactly the approved ref update and nothing else.
func checkCommand(commands []command, capabilities []string, grant Grant) error {
	if len(commands) != 1 {
		return errors.New("a push may update exactly one ref")
	}
	for _, capability := range capabilities {
		name, _, _ := strings.Cut(capability, "=")
		if name == "push-options" {
			return errors.New("push options are not accepted")
		}
	}
	update := commands[0]
	expected := grant.ExpectedOld
	if expected == "" {
		expected = strings.Repeat("0", len(grant.SourceCommit))
	}
	if update.ref != grant.DestinationRef {
		return errors.New("the push names a different branch than was approved")
	}
	if update.new != grant.SourceCommit {
		return errors.New("the push names a different commit than was approved")
	}
	if update.old != expected {
		return errors.New("the branch is not at the commit the approval expected")
	}
	return nil
}

// findRef reads a receive-pack advertisement and returns ref's commit, or ""
// when the source host does not have it.
func findRef(body io.Reader, ref string) (string, error) {
	reader := bufio.NewReader(body)
	payload, _, flush, err := readPkt(reader)
	if err != nil || flush || strings.TrimSuffix(string(payload), "\n") != "# service=git-receive-pack" {
		return "", errors.New("unexpected advertisement from the source host")
	}
	if _, _, flush, err := readPkt(reader); err != nil || !flush {
		return "", errors.New("unexpected advertisement from the source host")
	}
	for {
		payload, _, flush, err := readPkt(reader)
		if err != nil {
			return "", errors.New("unexpected advertisement from the source host")
		}
		if flush {
			return "", nil
		}
		line, _, _ := bytes.Cut(payload, []byte{0})
		oid, name, ok := strings.Cut(strings.TrimSuffix(string(line), "\n"), " ")
		if !ok || !validObjectID(oid) {
			return "", errors.New("unexpected advertisement from the source host")
		}
		if name == ref {
			if _, err := hex.DecodeString(oid); err != nil || strings.Trim(oid, "0") == "" {
				return "", nil
			}
			return oid, nil
		}
	}
}
