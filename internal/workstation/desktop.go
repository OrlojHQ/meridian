package workstation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/OrlojHQ/meridian/pkg/client"
)

// The desktop app's import window (ADR 0049) drives this through
// `meridiand desktop-import`, a hidden client mode of the binary it already
// ships. The protocol is line-delimited JSON on stdin and stdout:
//
//	shell → mode  {"origin":"https://gateway","cookie":"name=value; …"}   once, first
//	mode → shell  {"type":"review",…} | {"type":"signedOut"} | {"type":"error",…}
//	shell → mode  {"action":"save"} | {"action":"cancel"}
//	mode → shell  {"type":"saved",…} | {"type":"cancelled"} | {"type":"error",…}
//
// The session cookie arrives only on stdin, never in argv, the environment,
// a file, or a log. The mode starts no listener and opens no data directory.

// maxDesktopLine bounds every line the shell sends.
const maxDesktopLine = 64 << 10

// DesktopHello is the shell's first line: the gateway the import window is
// bound to and the main window's session cookie for that origin.
type DesktopHello struct {
	Origin string `json:"origin"`
	Cookie string `json:"cookie"`
}

// DesktopAccount is who the window is signed in as, from GET /auth/me.
type DesktopAccount struct {
	ID          string `json:"id"`
	Mode        string `json:"mode"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
}

// DesktopMessage is one line the mode writes.
type DesktopMessage struct {
	Type    string          `json:"type"`
	Origin  string          `json:"origin,omitempty"`
	Account *DesktopAccount `json:"account,omitempty"`
	Review  *Review         `json:"review,omitempty"`
	// Digest is the scan's digest, which the shell records as the baseline
	// for its change prompt after a save or dismiss.
	Digest     string   `json:"digest,omitempty"`
	Exclusions int      `json:"exclusions,omitempty"`
	Changes    bool     `json:"changes,omitempty"`
	Saved      []string `json:"saved,omitempty"`
	Unchanged  []string `json:"unchanged,omitempty"`
	Message    string   `json:"message,omitempty"`
}

// DesktopAPI is what the mode needs from a client signed in as the window.
type DesktopAPI interface {
	API
	GetCurrentMember(ctx context.Context) (client.GetCurrentMemberRes, error)
}

// ParseGatewayOrigin accepts only a bare http(s) origin.
func ParseGatewayOrigin(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("the import window needs a gateway origin")
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return nil, errors.New("a remote gateway must use HTTPS")
		}
	}
	parsed.Path = ""
	return parsed, nil
}

// cookieTransport sends the window's session as the page's own requests
// would: the Cookie header and the gateway origin as Origin, which the API
// requires for a cookie-authenticated change. The generated client always
// attaches a bearer, so the placeholder is removed here.
type cookieTransport struct {
	origin string
	cookie string
	next   http.RoundTripper
}

func (t cookieTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Del("Authorization")
	clone.Header.Set("Cookie", t.cookie)
	clone.Header.Set("Origin", t.origin)
	return t.next.RoundTrip(clone)
}

type placeholderBearer struct{}

func (placeholderBearer) BearerAuth(context.Context, client.OperationName) (client.BearerAuth, error) {
	return client.BearerAuth{Token: "desktop-session"}, nil
}

// NewDesktopClient returns a client that authenticates as the window's
// session for one gateway origin.
func NewDesktopClient(hello DesktopHello) (*client.Client, error) {
	origin, err := ParseGatewayOrigin(hello.Origin)
	if err != nil {
		return nil, err
	}
	if hello.Cookie == "" || strings.ContainsAny(hello.Cookie, "\r\n\x00") || len(hello.Cookie) > 16<<10 {
		return nil, errors.New("the window's session is not usable")
	}
	transport := cookieTransport{origin: origin.String(), cookie: hello.Cookie, next: http.DefaultTransport}
	return client.NewClient(origin.String(), placeholderBearer{}, client.WithClient(&http.Client{Transport: transport}))
}

func emit(out io.Writer, message DesktopMessage) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(message)
}

// RunDesktopImport runs one import window session: read the hello, scan,
// compare as the signed-in account, send the review, then wait for Save or
// Cancel. connect builds the API for the hello; tests replace it.
func RunDesktopImport(
	ctx context.Context, in io.Reader, out io.Writer, scan func() (Scan, error),
	connect func(DesktopHello) (DesktopAPI, error),
) error {
	lines := bufio.NewScanner(in)
	lines.Buffer(make([]byte, 0, 4096), maxDesktopLine)
	if !lines.Scan() {
		return errors.New("no gateway session was given")
	}
	var hello DesktopHello
	if err := json.Unmarshal(lines.Bytes(), &hello); err != nil {
		return errors.New("the gateway session line is not valid")
	}
	origin, err := ParseGatewayOrigin(hello.Origin)
	if err != nil {
		return emit(out, DesktopMessage{Type: "error", Message: err.Error()})
	}
	result, err := scan()
	if err != nil {
		return emit(out, DesktopMessage{Type: "error", Message: "this computer could not be scanned"})
	}
	if hello.Cookie == "" {
		return emit(out, DesktopMessage{Type: "signedOut", Origin: origin.String()})
	}
	api, err := connect(hello)
	if err != nil {
		return emit(out, DesktopMessage{Type: "error", Message: err.Error()})
	}
	member, err := api.GetCurrentMember(ctx)
	var status *client.DefaultErrorStatusCode
	if errors.As(err, &status) && (status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden) {
		return emit(out, DesktopMessage{Type: "signedOut", Origin: origin.String()})
	}
	if err != nil {
		return emit(out, DesktopMessage{Type: "error", Origin: origin.String(), Message: "the gateway could not be reached"})
	}
	current, ok := member.(*client.Member)
	if !ok {
		return emit(out, DesktopMessage{Type: "signedOut", Origin: origin.String()})
	}
	account := &DesktopAccount{ID: current.ID, Mode: string(current.Mode), DisplayName: current.DisplayName.Or(""), Email: current.Email.Or("")}
	review, err := Compare(ctx, api, result)
	if err != nil {
		return emit(out, DesktopMessage{Type: "error", Origin: origin.String(), Account: account, Message: "your saved profile could not be read"})
	}
	includeGit := review.Git.Changed
	changes := includeGit
	for _, harness := range review.Harnesses {
		changes = changes || harness.Status != "unchanged"
	}
	if err := emit(out, DesktopMessage{
		Type: "review", Origin: origin.String(), Account: account, Review: &review,
		Digest: result.Digest(), Exclusions: review.Exclusions(), Changes: changes,
	}); err != nil {
		return err
	}
	for lines.Scan() {
		var command struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(lines.Bytes(), &command) != nil {
			continue
		}
		switch command.Action {
		case "cancel":
			return emit(out, DesktopMessage{Type: "cancelled"})
		case "save":
			if !changes {
				return emit(out, DesktopMessage{Type: "saved", Digest: result.Digest()})
			}
			saved, err := Save(ctx, api, Request(review, includeGit))
			if err != nil {
				return emit(out, DesktopMessage{Type: "error", Origin: origin.String(), Account: account, Message: err.Error()})
			}
			message := DesktopMessage{Type: "saved", Digest: result.Digest(), Unchanged: saved.Unchanged, Saved: []string{}}
			for _, setup := range saved.Saved {
				message.Saved = append(message.Saved, setup.Harness)
			}
			return emit(out, message)
		}
	}
	if err := lines.Err(); err != nil {
		return fmt.Errorf("the import window closed: %w", err)
	}
	return emit(out, DesktopMessage{Type: "cancelled"})
}

// CheckDigest is all `desktop-import --check` prints: the scan's digest. It
// takes no session and makes no network request.
type CheckDigest struct {
	Digest string `json:"digest"`
}
