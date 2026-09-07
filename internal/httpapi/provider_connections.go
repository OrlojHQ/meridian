package httpapi

import (
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"net/http"
)

func (s *Server) listProviderConnections(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.ListProviderConnections(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "enabled": s.service.ProviderGatewayEnabled()})
}
func (s *Server) putProviderConnection(w http.ResponseWriter, r *http.Request) {
	var input app.ConnectionInput
	if !decode(w, r, &input) {
		return
	}
	item, err := s.service.PutProviderConnection(r.Context(), r.PathValue("connectionId"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) projectProviderConnection(w http.ResponseWriter, r *http.Request) {
	project := domain.ProjectID(r.PathValue("projectId"))
	harness := r.PathValue("harness")
	if r.Method == http.MethodPut {
		var input struct {
			ConnectionID string `json:"connectionId"`
		}
		if !decode(w, r, &input) {
			return
		}
		if err := s.service.GrantProviderConnection(r.Context(), project, harness, input.ConnectionID); err != nil {
			writeError(w, err)
			return
		}
	}
	id, err := s.service.ProjectProviderConnection(r.Context(), project, harness)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"connectionId": id})
}
