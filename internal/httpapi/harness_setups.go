package httpapi

import (
	"fmt"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"net/http"
)

func (s *Server) listHarnessSetups(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.ListHarnessSetups(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) importHarnessSetup(w http.ResponseWriter, r *http.Request) {
	var input app.SetupImport
	if !decodeLimit(w, r, &input, 4<<20) {
		return
	}
	item, err := s.service.ImportHarnessSetup(r.Context(), input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (s *Server) mutateHarnessSetup(w http.ResponseWriter, r *http.Request) {
	var input app.SetupMutation
	if !decode(w, r, &input) {
		return
	}
	item, err := s.service.MutateHarnessSetup(r.Context(), r.PathValue("setupId"), input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) listHarnessSetupRevisions(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.HarnessSetupRevisions(r.Context(), r.PathValue("setupId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) projectHarnessSetup(w http.ResponseWriter, r *http.Request) {
	project := domain.ProjectID(r.PathValue("projectId"))
	harness := r.PathValue("harness")
	if r.Method == http.MethodPut {
		var input struct {
			Setup string `json:"setup"`
		}
		if !decode(w, r, &input) {
			return
		}
		if err := s.service.SetProjectHarnessSetup(r.Context(), project, harness, input.Setup); err != nil {
			writeError(w, err)
			return
		}
	}
	items, err := s.service.ProjectHarnessSetups(r.Context(), project)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"setup": items[harness]})
}

func (s *Server) previewHarnessSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		Harness string              `json:"harness"`
		Files   []harnesssetup.File `json:"files"`
	}
	if !decodeLimit(w, r, &input, 4<<20) {
		return
	}
	preview, err := harnesssetup.ReviewFiles(input.Harness, input.Files)
	if err != nil {
		writeError(w, fmt.Errorf("%w: %s", domain.ErrInvalid, err))
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Bundle   harnesssetup.Bundle  `json:"bundle"`
		Issues   []harnesssetup.Issue `json:"issues"`
		Digest   string               `json:"digest"`
		Warnings []harnesssetup.Issue `json:"warnings,omitempty"`
	}{preview.Bundle, preview.Issues, preview.Digest, preview.Warnings})
}

func (s *Server) harnessSetupContents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := s.service.HarnessSetupContents(r.Context(), r.PathValue("setupId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
