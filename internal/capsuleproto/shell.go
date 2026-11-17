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
	subscribers map[int]chan []byte
	nextSub     int
	attachments int
	exited      bool
	dead        chan struct{}
}

func (s *Server) startShell(columns, rows uint16) error {
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
			return nil
		}
	}
	path, args := shellProgram()
	command := exec.Command(path, args...)
	command.Dir = s.config.Workspace
	command.Env = withPTYEnvironment(shellEnvironment())
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: columns, Rows: rows})
	if err != nil {
		return err
	}
	session := &shellSession{
		command: command, terminal: terminal, subscribers: map[int]chan []byte{},
		dead: make(chan struct{}),
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
			chunk := append([]byte(nil), buffer[:n]...)
			sh.mu.Lock()
			for _, subscriber := range sh.subscribers {
				select {
				case subscriber <- chunk:
				default:
				}
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
		close(subscriber)
	}
	_ = sh.terminal.Close()
}

func (sh *shellSession) kill() {
	if sh.command.Process != nil {
		_ = syscall.Kill(-sh.command.Process.Pid, syscall.SIGKILL)
	}
	sh.finish()
}

func (sh *shellSession) subscribe() (int, <-chan []byte, error) {
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
	channel := make(chan []byte, 32)
	sh.subscribers[id] = channel
	return id, channel, nil
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
	if err := s.startShell(uint16(columns), uint16(rows)); err != nil {
		writeProtocolError(writer, http.StatusServiceUnavailable, "shell_unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"state": "running"})
}

func (s *Server) shellAttach(writer http.ResponseWriter, request *http.Request) {
	columns, _ := strconv.ParseUint(request.URL.Query().Get("columns"), 10, 16)
	rows, _ := strconv.ParseUint(request.URL.Query().Get("rows"), 10, 16)
	if err := s.startShell(uint16(columns), uint16(rows)); err != nil {
		writeProtocolError(writer, http.StatusServiceUnavailable, "shell_unavailable")
		return
	}
	s.mu.Lock()
	session := s.shell
	s.mu.Unlock()
	if session == nil || session.exited {
		writeProtocolError(writer, http.StatusNotFound, "shell_unavailable")
		return
	}
	id, output, err := session.subscribe()
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
			select {
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			case <-s.stopped:
				errc <- errors.New("capsule stopping")
				return
			case <-session.dead:
				_ = connection.Write(context.Background(), websocket.MessageText, []byte(`{"type":"exit"}`))
				errc <- io.EOF
				return
			case chunk, ok := <-output:
				if !ok {
					_ = connection.Write(context.Background(), websocket.MessageText, []byte(`{"type":"exit"}`))
					errc <- io.EOF
					return
				}
				if err := connection.Write(ctx, websocket.MessageBinary, chunk); err != nil {
					errc <- err
					return
				}
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
