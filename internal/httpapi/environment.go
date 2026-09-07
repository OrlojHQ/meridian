package httpapi

import (
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"net/http"
)

func (s *Server) projectEnvironment(w http.ResponseWriter, r *http.Request) {
	id := domain.ProjectID(r.PathValue("projectId"))
	var result app.ProjectEnvironment
	var err error
	if r.Method == http.MethodPut {
		var input app.EnvironmentMutation
		if !decode(w, r, &input) {
			return
		}
		result, err = s.service.UpdateProjectEnvironment(r.Context(), id, input, r.Header.Get("Idempotency-Key"))
	} else {
		result, err = s.service.ProjectEnvironment(r.Context(), id)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
