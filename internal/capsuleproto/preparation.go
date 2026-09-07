package capsuleproto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type PreparationIdentity struct {
	SourceRevision string `json:"sourceRevision"`
	Platform       string `json:"platform"`
}
type FinishPreparationRequest struct {
	Setup          []string `json:"setup"`
	SourceRevision string   `json:"sourceRevision"`
	Key            string   `json:"key"`
}

func (c *Client) PreparationIdentity(ctx context.Context) (PreparationIdentity, error) {
	var result PreparationIdentity
	err := c.call(ctx, http.MethodGet, "/v1/preparation/identity", nil, &result, true)
	return result, err
}
func (c *Client) FinishPreparation(ctx context.Context, input FinishPreparationRequest) error {
	var result PrepareResponse
	return c.call(ctx, http.MethodPost, "/v1/preparation/finish", input, &result, true)
}
func (s *Server) preparationIdentity(w http.ResponseWriter, r *http.Request) {
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasActiveRunLocked() {
		writeProtocolError(w, http.StatusConflict, "active_run")
		return
	}
	head := preparationHead(r.Context(), s.config.Workspace)
	writeJSON(w, http.StatusOK, PreparationIdentity{SourceRevision: head, Platform: runtime.GOOS + "/" + runtime.GOARCH})
}
func (s *Server) finishPreparation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.config.BodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input FinishPreparationRequest
	if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !validPreparationKey(input.Key) || s.validateRequest(PrepareRequest{Destination: s.config.Workspace, Setup: input.Setup}) != nil {
		writeProtocolError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasActiveRunLocked() {
		writeProtocolError(w, http.StatusConflict, "active_run")
		return
	}
	root, err := os.OpenRoot(s.config.Workspace)
	if err != nil {
		writeProtocolError(w, http.StatusUnprocessableEntity, "workspace_invalid")
		return
	}
	defer root.Close()
	if value, err := root.ReadFile(".meridian-setup-complete"); err == nil && string(value) == input.Key {
		writeJSON(w, http.StatusOK, PrepareResponse{Status: StateReady})
		return
	}
	head := preparationHead(r.Context(), s.config.Workspace)
	if head != input.SourceRevision {
		writeProtocolError(w, http.StatusConflict, "source_changed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.config.SetupTimeout)
	defer cancel()
	if len(input.Setup) > 0 {
		if err := s.config.CommandRunner(ctx, input.Setup[0], input.Setup[1:], s.config.Workspace, s.config.OutputLimit); err != nil {
			writeProtocolError(w, http.StatusUnprocessableEntity, "setup_command_failed")
			return
		}
	}
	// Rooted writes reject Capsule-controlled links outside the workspace.
	if err := root.WriteFile(".meridian-setup-complete", []byte(input.Key), 0600); err != nil {
		writeProtocolError(w, http.StatusUnprocessableEntity, "setup_marker_failed")
		return
	}
	writeJSON(w, http.StatusOK, PrepareResponse{Status: StateReady})
}
func validPreparationKey(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func preparationHead(ctx context.Context, workspace string) string {
	command := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "-C", workspace, "rev-parse", "--verify", "HEAD")
	command.Env = childEnvironment(nil)
	value, err := command.Output()
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(value))
	if (len(head) != 40 && len(head) != 64) || strings.Trim(head, "0123456789abcdef") != "" {
		return ""
	}
	return head
}
