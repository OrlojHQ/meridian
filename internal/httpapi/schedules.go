package httpapi

import (
	"net/http"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// Scheduled Project Threads (ADR 0057).

func (s *Server) projectSchedules(writer http.ResponseWriter, request *http.Request) {
	id := domain.ProjectID(request.PathValue("projectId"))
	if request.Method == http.MethodPost {
		var input app.ScheduleInput
		if !decode(writer, request, &input) {
			return
		}
		result, err := s.service.CreateSchedule(request.Context(), id, input, request.Header.Get("Idempotency-Key"))
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, result)
		return
	}
	items, err := s.service.ListSchedules(request.Context(), id)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []app.ScheduleView `json:"items"`
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

func (s *Server) schedule(writer http.ResponseWriter, request *http.Request) {
	id := domain.ScheduleID(request.PathValue("scheduleId"))
	if request.Method == http.MethodPatch {
		var input app.ScheduleUpdate
		if !decode(writer, request, &input) {
			return
		}
		result, err := s.service.UpdateSchedule(request.Context(), id, input, request.Header.Get("Idempotency-Key"))
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, result)
		return
	}
	result, err := s.service.GetSchedule(request.Context(), id)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) deleteSchedule(writer http.ResponseWriter, request *http.Request) {
	var input lifecycleRequest
	if !decode(writer, request, &input) {
		return
	}
	if err := s.service.DeleteSchedule(request.Context(), domain.ScheduleID(request.PathValue("scheduleId")),
		domain.ResourceVersion(input.ExpectedResourceVersion), request.Header.Get("Idempotency-Key")); err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) runSchedule(writer http.ResponseWriter, request *http.Request) {
	if request.Body != nil && request.ContentLength != 0 {
		var ignored struct{}
		if !decode(writer, request, &ignored) {
			return
		}
	}
	result, err := s.service.RunSchedule(request.Context(), domain.ScheduleID(request.PathValue("scheduleId")),
		request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, result)
}

func (s *Server) scheduleFires(writer http.ResponseWriter, request *http.Request) {
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	items, more, err := s.service.ListScheduleFires(request.Context(),
		domain.ScheduleID(request.PathValue("scheduleId")), ports.Page{Offset: offset, Limit: limit})
	if err != nil {
		writeError(writer, err)
		return
	}
	page := struct {
		Items      []app.ScheduleFireView `json:"items"`
		NextCursor string                 `json:"nextCursor,omitempty"`
	}{Items: items}
	if more {
		page.NextCursor = encodeCursor(offset + len(items))
	}
	writeJSON(writer, http.StatusOK, page)
}
