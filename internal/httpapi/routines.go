package httpapi

import (
	"net/http"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// Routines (ADR 0057).

func (s *Server) projectRoutines(writer http.ResponseWriter, request *http.Request) {
	id := domain.ProjectID(request.PathValue("projectId"))
	if request.Method == http.MethodPost {
		var input app.RoutineInput
		if !decode(writer, request, &input) {
			return
		}
		result, err := s.service.CreateRoutine(request.Context(), id, input, request.Header.Get("Idempotency-Key"))
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, result)
		return
	}
	items, err := s.service.ListRoutines(request.Context(), id)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []app.RoutineView `json:"items"`
	}{Items: items})
}

func (s *Server) ownedRoutines(writer http.ResponseWriter, request *http.Request) {
	items, err := s.service.ListOwnedRoutines(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []app.RoutineView `json:"items"`
	}{Items: items})
}

func (s *Server) reportedModels(writer http.ResponseWriter, request *http.Request) {
	result, err := s.service.ReportedModels(request.Context(),
		domain.ProjectID(request.PathValue("projectId")), request.PathValue("harness"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) routine(writer http.ResponseWriter, request *http.Request) {
	id := domain.RoutineID(request.PathValue("routineId"))
	if request.Method == http.MethodPatch {
		var input app.RoutineUpdate
		if !decode(writer, request, &input) {
			return
		}
		result, err := s.service.UpdateRoutine(request.Context(), id, input, request.Header.Get("Idempotency-Key"))
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, result)
		return
	}
	result, err := s.service.GetRoutine(request.Context(), id)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) deleteRoutine(writer http.ResponseWriter, request *http.Request) {
	var input lifecycleRequest
	if !decode(writer, request, &input) {
		return
	}
	if err := s.service.DeleteRoutine(request.Context(), domain.RoutineID(request.PathValue("routineId")),
		domain.ResourceVersion(input.ExpectedResourceVersion), request.Header.Get("Idempotency-Key")); err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) runRoutine(writer http.ResponseWriter, request *http.Request) {
	if request.Body != nil && request.ContentLength != 0 {
		var ignored struct{}
		if !decode(writer, request, &ignored) {
			return
		}
	}
	result, err := s.service.RunRoutine(request.Context(), domain.RoutineID(request.PathValue("routineId")),
		request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, result)
}

func (s *Server) routineFires(writer http.ResponseWriter, request *http.Request) {
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	items, more, err := s.service.ListRoutineFires(request.Context(),
		domain.RoutineID(request.PathValue("routineId")), ports.Page{Offset: offset, Limit: limit})
	if err != nil {
		writeError(writer, err)
		return
	}
	page := struct {
		Items      []app.RoutineFireView `json:"items"`
		NextCursor string                `json:"nextCursor,omitempty"`
	}{Items: items}
	if more {
		page.NextCursor = encodeCursor(offset + len(items))
	}
	writeJSON(writer, http.StatusOK, page)
}
