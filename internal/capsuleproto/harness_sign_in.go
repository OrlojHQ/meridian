package capsuleproto

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrlojHQ/meridian/internal/antigravityauth"
)

// A harness sign-in runs a harness's own login program in a sign-in Capsule
// (ADR 0062). The only harness with one is agy: capsuled runs the pinned
// binary with no shell, relays the Google link it prints and the code the
// user pastes, and returns the two profile files it wrote. capsuled keeps
// neither the code nor the files; the sign-in home is removed when the
// attempt ends.

const (
	harnessSignInPath     = "/v1/harness-sign-in"
	harnessSignInCodePath = "/v1/harness-sign-in/code"
	signInHomeName        = ".meridian-sign-in"
)

var (
	// signInURLWait bounds how long agy may take to print its link.
	signInURLWait = 30 * time.Second
	// signInExitWait bounds the code exchange after a code is sent.
	signInExitWait = 2 * time.Minute
	// signInLifetime ends an abandoned attempt.
	signInLifetime = 11 * time.Minute
)

// signInCommands are the trusted login programs, by harness. Tests replace
// the program path.
var signInCommands = map[string][]string{
	"agy": {"/usr/local/bin/agy", "acp", "--disable-telemetry", "--login"},
}

type HarnessSignInRequest struct {
	Harness string `json:"harness"`
}

type HarnessSignInStart struct {
	AuthorizationURL string `json:"authorizationUrl"`
}

type HarnessSignInCode struct {
	Code string `json:"code"`
}

type HarnessSignInResult struct {
	Files []antigravityauth.File `json:"files"`
}

type harnessSignIn struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	home     string
	url      string
	codeSent bool
	exited   chan struct{}
	exitErr  error
	killMu   sync.Mutex
	reaped   bool
	timer    *time.Timer
	homeMu   sync.Mutex
	removed  bool
	stopOnce sync.Once
}

func (c *Client) StartHarnessSignIn(ctx context.Context, harness string) (HarnessSignInStart, error) {
	var result HarnessSignInStart
	err := c.call(ctx, http.MethodPost, harnessSignInPath, HarnessSignInRequest{Harness: harness}, &result, true)
	return result, err
}

func (c *Client) FinishHarnessSignIn(ctx context.Context, code string) (HarnessSignInResult, error) {
	var result HarnessSignInResult
	err := c.call(ctx, http.MethodPost, harnessSignInCodePath, HarnessSignInCode{Code: code}, &result, true)
	return result, err
}

func (c *Client) CancelHarnessSignIn(ctx context.Context) error {
	return c.call(ctx, http.MethodDelete, harnessSignInPath, nil, nil, true)
}

func (s *Server) startHarnessSignIn(writer http.ResponseWriter, request *http.Request) {
	var input HarnessSignInRequest
	if !decodeRequest(writer, request, s.config.BodyLimit, &input) {
		return
	}
	argv, ok := signInCommands[input.Harness]
	if !ok {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	s.signInMu.Lock()
	defer s.signInMu.Unlock()
	if s.signIn != nil {
		writeProtocolError(writer, http.StatusConflict, "sign_in_in_progress")
		return
	}
	attempt, err := s.launchSignIn(argv)
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "sign_in_failed")
		return
	}
	s.signIn = attempt
	writeJSON(writer, http.StatusOK, HarnessSignInStart{AuthorizationURL: attempt.url})
}

// launchSignIn starts the login program in a fresh private home and waits
// for the authorization link. The caller holds signInMu.
func (s *Server) launchSignIn(argv []string) (*harnessSignIn, error) {
	base := s.config.HarnessHome
	if base == "" {
		base = "/home/capsule"
	}
	home := filepath.Join(base, signInHomeName)
	if err := os.RemoveAll(home); err != nil {
		return nil, err
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		return nil, err
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = home
	command.Env = setupEnvironment(childEnvironment(nil), map[string]string{"HOME": home})
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 2 * time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(home)
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(home)
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = os.RemoveAll(home)
		return nil, err
	}
	attempt := &harnessSignIn{command: command, stdin: stdin, home: home, exited: make(chan struct{})}
	found := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 4096), 16<<10)
		sent := false
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !sent && antigravityauth.CheckAuthorizationURL(line) == nil {
				found <- line
				sent = true
			}
		}
		// Keep reading until agy closes stdout, so it never blocks writing.
		_, _ = io.Copy(io.Discard, stdout)
		if !sent {
			close(found)
		}
	}()
	go func() {
		err := command.Wait()
		attempt.killMu.Lock()
		attempt.exitErr = err
		attempt.reaped = true
		attempt.killMu.Unlock()
		close(attempt.exited)
	}()
	select {
	case link, ok := <-found:
		if ok && antigravityauth.CheckAuthorizationURL(link) == nil {
			attempt.url = link
		}
	case <-time.After(signInURLWait):
	case <-attempt.exited:
	}
	if attempt.url == "" {
		attempt.stop()
		return nil, errors.New("sign-in link unavailable")
	}
	attempt.killMu.Lock()
	attempt.timer = time.AfterFunc(signInLifetime, func() { s.endSignIn(attempt) })
	attempt.killMu.Unlock()
	return attempt, nil
}

func (s *Server) finishHarnessSignIn(writer http.ResponseWriter, request *http.Request) {
	var input HarnessSignInCode
	if !decodeRequest(writer, request, s.config.BodyLimit, &input) {
		return
	}
	code, err := antigravityauth.CleanCode(input.Code)
	input.Code = ""
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	s.signInMu.Lock()
	attempt := s.signIn
	switch {
	case attempt == nil:
		s.signInMu.Unlock()
		writeProtocolError(writer, http.StatusNotFound, "sign_in_not_found")
		return
	case attempt.codeSent:
		s.signInMu.Unlock()
		writeProtocolError(writer, http.StatusConflict, "sign_in_in_progress")
		return
	}
	attempt.codeSent = true
	s.signInMu.Unlock()
	defer s.endSignIn(attempt)

	_, writeErr := io.WriteString(attempt.stdin, code+"\n")
	_ = attempt.stdin.Close()
	if writeErr != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "sign_in_rejected")
		return
	}
	select {
	case <-attempt.exited:
	case <-time.After(signInExitWait):
		writeProtocolError(writer, http.StatusUnprocessableEntity, "sign_in_failed")
		return
	case <-request.Context().Done():
		return
	}
	if attempt.exitErr != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "sign_in_rejected")
		return
	}
	attempt.homeMu.Lock()
	var files []antigravityauth.File
	if attempt.removed {
		err = errors.New("sign-in cancelled")
	} else {
		files, err = readSignInProfile(attempt.home)
	}
	attempt.homeMu.Unlock()
	defer func() {
		for index := range files {
			clear(files[index].Content)
		}
	}()
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "sign_in_failed")
		return
	}
	writeJSON(writer, http.StatusOK, HarnessSignInResult{Files: files})
}

func (s *Server) cancelHarnessSignIn(writer http.ResponseWriter, _ *http.Request) {
	s.signInMu.Lock()
	attempt := s.signIn
	s.signInMu.Unlock()
	if attempt != nil {
		s.endSignIn(attempt)
	}
	writer.Header().Set(VersionHeader, Version)
	writer.WriteHeader(http.StatusNoContent)
}

// endSignIn stops an attempt and removes its home. It is safe to call more
// than once.
func (s *Server) endSignIn(attempt *harnessSignIn) {
	s.signInMu.Lock()
	if s.signIn == attempt {
		s.signIn = nil
	}
	s.signInMu.Unlock()
	attempt.stop()
}

func (attempt *harnessSignIn) stop() {
	attempt.stopOnce.Do(func() {
		attempt.killMu.Lock()
		if attempt.timer != nil {
			attempt.timer.Stop()
		}
		reaped := attempt.reaped
		var pid int
		if !reaped && attempt.command.Process != nil {
			pid = attempt.command.Process.Pid
		}
		if pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		attempt.killMu.Unlock()
		_ = attempt.stdin.Close()
		<-attempt.exited
		attempt.homeMu.Lock()
		attempt.removed = true
		_ = os.RemoveAll(attempt.home)
		attempt.homeMu.Unlock()
	})
}

// readSignInProfile reads the two profile files with rooted operations, so a
// link in the sign-in home cannot point the read elsewhere.
func readSignInProfile(home string) ([]antigravityauth.File, error) {
	root, err := os.OpenRoot(filepath.Join(home, filepath.FromSlash(antigravityauth.Dir)))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := make([]antigravityauth.File, 0, len(antigravityauth.Paths))
	for _, p := range antigravityauth.Paths {
		info, err := root.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() > antigravityauth.MaxFileSize {
			return nil, errors.New("sign-in profile is incomplete")
		}
		file, err := root.Open(p)
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(io.LimitReader(file, antigravityauth.MaxFileSize+1))
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		files = append(files, antigravityauth.File{Path: p, Content: content})
	}
	if _, err := antigravityauth.Check(files); err != nil {
		return nil, err
	}
	return files, nil
}
