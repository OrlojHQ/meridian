package capsuleproto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// shellSession is one interactive shell in the Capsule workspace. It runs as
// the Capsule user, because capsuled itself does. Its environment is the
// Capsule process environment with protocol and credential variables removed,
// so a host secret is not copied in. Stopping the Capsule kills the process;
// the shell is not waited on.
type shellSession struct {
	mu          sync.Mutex
	command     *exec.Cmd
	terminal    *os.File
	subscribers map[int]*shellSubscriber
	nextSub     int
	attachments int
	exited      bool
	dead        chan struct{}
	redactor    *tokenRedactor
}

// shellSubscriberBuffer bounds the output held for one attached viewer.
// Output is never dropped silently: a viewer that falls this far behind is
// disconnected, because a terminal stream with a gap in it is corrupt.
const shellSubscriberBuffer = 1 << 20

// shellSubscriber is one attached viewer. pending is the output it has not
// written yet. ready is signalled whenever pending, overflow, or the shell's
// exit changes.
type shellSubscriber struct {
	pending  []byte
	overflow bool
	ready    chan struct{}
}

func (sub *shellSubscriber) signal() {
	select {
	case sub.ready <- struct{}{}:
	default:
	}
}

func (s *Server) startShell(columns, rows uint16, access *harnesssetup.GitHubAccess) error {
	if columns == 0 {
		columns = 80
	}
	if rows == 0 {
		rows = 24
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stopped:
		return errors.New("capsule is stopping")
	default:
	}
	if s.shell != nil {
		s.shell.mu.Lock()
		live := !s.shell.exited
		s.shell.mu.Unlock()
		if live {
			if access != nil && access.Usable() {
				_ = s.refreshGitHubToken(access.Token)
			}
			if access != nil {
				access.Wipe()
			}
			return nil
		}
	}
	path, args := shellProgram()
	command := exec.Command(path, args...)
	command.Dir = s.config.Workspace
	gitEnv, err := s.prepareSessionGit(nil, access, nil)
	if err != nil {
		if access != nil {
			access.Wipe()
		}
		return err
	}
	command.Env = withPTYEnvironment(setupEnvironment(applyGitHubEnv(shellEnvironment(), access), gitEnv))
	if access != nil {
		access.Wipe()
	}
	clearGitHubEnv(gitEnv)
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: columns, Rows: rows})
	if err != nil {
		return err
	}
	session := &shellSession{
		command: command, terminal: terminal, subscribers: map[int]*shellSubscriber{},
		dead: make(chan struct{}), redactor: newTokenRedactor(s.githubTokens),
	}
	s.shell = session
	go session.readLoop()
	go session.wait()
	return nil
}

func (s *Server) killShell() {
	s.mu.Lock()
	session := s.shell
	s.mu.Unlock()
	if session == nil {
		return
	}
	session.kill()
}

// Close ends the workspace shell without waiting for it to exit, so Capsule
// shutdown is not held by an open terminal.
func (s *Server) Close() {
	s.stopOnce.Do(func() { close(s.stopped) })
	s.killShell()
}

func shellProgram() (string, []string) {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash", []string{"-i"}
	}
	return "/bin/sh", []string{"-i"}
}

// shellEnvironment is the Capsule environment without protocol credentials
// or other secret-shaped variables. Network egress is the Capsule's own.
func shellEnvironment() []string {
	blocked := map[string]bool{
		"MERIDIAN_CAPSULE_TOKEN": true, "MERIDIAN_PROVIDER_TOKEN": true,
		"MERIDIAN_GIT_USERNAME": true, "MERIDIAN_GIT_PASSWORD": true,
		"GIT_ASKPASS": true, "SSH_ASKPASS": true,
		"GH_TOKEN": true, "GITHUB_TOKEN": true,
	}
	result := make([]string, 0, len(os.Environ()))
	for _, item := range childEnvironment(nil) {
		key, _, _ := strings.Cut(item, "=")
		if blocked[key] || secretEnvName(key) {
			continue
		}
		result = append(result, item)
	}
	return result
}

func secretEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, suffix := range []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_PASSWD"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

func (sh *shellSession) readLoop() {
	buffer := make([]byte, 4096)
	for {
		n, err := sh.terminal.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			if sh.redactor != nil {
				chunk = sh.redactor.push(chunk)
			}
			sh.mu.Lock()
			for _, subscriber := range sh.subscribers {
				if subscriber.overflow {
					continue
				}
				if len(subscriber.pending)+len(chunk) > shellSubscriberBuffer {
					subscriber.overflow = true
					subscriber.pending = nil
				} else {
					subscriber.pending = append(subscriber.pending, chunk...)
				}
				subscriber.signal()
			}
			sh.mu.Unlock()
		}
		if err != nil {
			sh.finish()
			return
		}
	}
}

func (sh *shellSession) wait() {
	_ = sh.command.Wait()
	sh.finish()
}

func (sh *shellSession) finish() {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.exited {
		return
	}
	sh.exited = true
	close(sh.dead)
	for _, subscriber := range sh.subscribers {
		subscriber.signal()
	}
	_ = sh.terminal.Close()
}

func (sh *shellSession) kill() {
	if sh.command.Process != nil {
		_ = syscall.Kill(-sh.command.Process.Pid, syscall.SIGKILL)
	}
	sh.finish()
}

func (sh *shellSession) subscribe() (int, *shellSubscriber, error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.exited {
		return 0, nil, io.EOF
	}
	if sh.attachments >= maxAttach {
		return 0, nil, errShellBusy
	}
	sh.attachments++
	sh.nextSub++
	id := sh.nextSub
	subscriber := &shellSubscriber{ready: make(chan struct{}, 1)}
	sh.subscribers[id] = subscriber
	return id, subscriber, nil
}

// errShellOverflow is a viewer that did not keep up with the shell output.
var errShellOverflow = errors.New("shell output overflow")

// take returns the viewer's unwritten output, at most one websocket frame.
// A slow writer must not coalesce the backlog into a message the peer's
// read limit rejects. With none left it reports whether the viewer
// overflowed or the shell exited. Output written before either is always
// returned first.
func (sh *shellSession) take(subscriber *shellSubscriber) ([]byte, error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if len(subscriber.pending) > 0 {
		n := len(subscriber.pending)
		if n > maxFrameBytes {
			n = maxFrameBytes
		}
		chunk := append([]byte(nil), subscriber.pending[:n]...)
		if n == len(subscriber.pending) {
			subscriber.pending = nil
		} else {
			subscriber.pending = append([]byte(nil), subscriber.pending[n:]...)
		}
		return chunk, nil
	}
	if subscriber.overflow {
		return nil, errShellOverflow
	}
	if sh.exited {
		return nil, io.EOF
	}
	return nil, nil
}

func (sh *shellSession) unsubscribe(id int) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if _, ok := sh.subscribers[id]; ok {
		delete(sh.subscribers, id)
		if sh.attachments > 0 {
			sh.attachments--
		}
	}
}

var errShellBusy = errors.New("shell attachment limit")

func (s *Server) shellStart(writer http.ResponseWriter, request *http.Request) {
	columns, _ := strconv.ParseUint(request.URL.Query().Get("columns"), 10, 16)
	rows, _ := strconv.ParseUint(request.URL.Query().Get("rows"), 10, 16)
	var body struct {
		GitHub *harnesssetup.GitHubAccess `json:"github"`
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, 1<<16))
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	if len(raw) > 0 {
		if json.Unmarshal(raw, &body) != nil {
			writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	if err := s.startShell(uint16(columns), uint16(rows), body.GitHub); err != nil {
		writeProtocolError(writer, http.StatusServiceUnavailable, "shell_unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"state": "running"})
}

func (s *Server) githubToken(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !decodeRequest(writer, request, 1<<16, &body) {
		return
	}
	if err := s.refreshGitHubToken(body.Token); err != nil {
		body.Token = ""
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	body.Token = ""
	writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) shellAttach(writer http.ResponseWriter, request *http.Request) {
	columns, _ := strconv.ParseUint(request.URL.Query().Get("columns"), 10, 16)
	rows, _ := strconv.ParseUint(request.URL.Query().Get("rows"), 10, 16)
	if err := s.startShell(uint16(columns), uint16(rows), nil); err != nil {
		writeProtocolError(writer, http.StatusServiceUnavailable, "shell_unavailable")
		return
	}
	s.mu.Lock()
	session := s.shell
	s.mu.Unlock()
	if session == nil {
		writeProtocolError(writer, http.StatusNotFound, "shell_unavailable")
		return
	}
	id, subscriber, err := session.subscribe()
	if errors.Is(err, errShellBusy) {
		writeProtocolError(writer, http.StatusTooManyRequests, "attachment_limit")
		return
	}
	if err != nil {
		writeProtocolError(writer, http.StatusNotFound, "shell_unavailable")
		return
	}
	defer session.unsubscribe(id)
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(maxFrameBytes)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	errc := make(chan error, 2)
	go func() {
		for {
			chunk, state := session.take(subscriber)
			if chunk != nil {
				if err := connection.Write(ctx, websocket.MessageBinary, chunk); err != nil {
					errc <- err
					return
				}
				continue
			}
			if errors.Is(state, errShellOverflow) {
				_ = connection.Close(websocket.StatusPolicyViolation, "shell output overflow")
				errc <- state
				return
			}
			if state != nil {
				_ = connection.Write(context.Background(), websocket.MessageText, []byte(`{"type":"exit"}`))
				errc <- state
				return
			}
			select {
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			case <-s.stopped:
				errc <- errors.New("capsule stopping")
				return
			case <-subscriber.ready:
			}
		}
	}()
	go func() {
		for {
			messageType, value, err := connection.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			if messageType == websocket.MessageBinary {
				session.mu.Lock()
				terminal := session.terminal
				session.mu.Unlock()
				if terminal != nil {
					_, _ = terminal.Write(value)
				}
				continue
			}
			var control struct {
				Type    string `json:"type"`
				Columns uint16 `json:"columns"`
				Rows    uint16 `json:"rows"`
			}
			if json.Unmarshal(value, &control) != nil || control.Type != "resize" || control.Columns == 0 || control.Rows == 0 {
				continue
			}
			session.mu.Lock()
			terminal := session.terminal
			session.mu.Unlock()
			if terminal != nil {
				_ = pty.Setsize(terminal, &pty.Winsize{Cols: control.Columns, Rows: control.Rows})
			}
		}
	}()
	<-errc
}
