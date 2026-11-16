package httpapi

import (
	"net/http"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func (s *Server) projectChecks(writer http.ResponseWriter, request *http.Request) {
	id := domain.ProjectID(request.PathValue("projectId"))
	if request.Method == http.MethodPut {
		var input app.ProjectChecksUpdate
		if !decode(writer, request, &input) {
			return
		}
		result, err := s.service.UpdateProjectChecks(request.Context(), id, input, request.Header.Get("Idempotency-Key"))
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, result)
		return
	}
	result, err := s.service.ProjectChecks(request.Context(), id)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) suggestProjectChecks(writer http.ResponseWriter, request *http.Request) {
	id := domain.ProjectID(request.PathValue("projectId"))
	capsuleID := domain.CapsuleID(request.URL.Query().Get("capsuleId"))
	result, err := s.service.SuggestProjectChecks(request.Context(), id, capsuleID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) capsuleCheckRuns(writer http.ResponseWriter, request *http.Request) {
	id := domain.CapsuleID(request.PathValue("capsuleId"))
	if request.Method == http.MethodPost {
		if request.Body != nil && request.ContentLength != 0 {
			var ignored struct{}
			if !decode(writer, request, &ignored) {
				return
			}
		}
		result, err := s.service.CreateCheckRun(request.Context(), id, request.Header.Get("Idempotency-Key"))
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusAccepted, result)
		return
	}
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	items, more, err := s.service.ListCheckRuns(request.Context(), id, ports.Page{Offset: offset, Limit: limit})
	if err != nil {
		writeError(writer, err)
		return
	}
	page := struct {
		Items      []app.CheckRunView `json:"items"`
		NextCursor string             `json:"nextCursor,omitempty"`
	}{Items: items}
	if more {
		page.NextCursor = encodeCursor(offset + len(items))
	}
	writeJSON(writer, http.StatusOK, page)
}

func (s *Server) checkRun(writer http.ResponseWriter, request *http.Request) {
	result, err := s.service.GetCheckRun(request.Context(), domain.CheckRunID(request.PathValue("checkRunId")))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) cancelCheckRun(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
	}
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.CancelCheckRun(
		request.Context(), domain.CheckRunID(request.PathValue("checkRunId")), input.ExpectedResourceVersion,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) capsuleAutoRework(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(writer, request, &input) {
		return
	}
	err := s.service.SetCapsuleAutoRework(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")), input.Enabled,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, input)
}
