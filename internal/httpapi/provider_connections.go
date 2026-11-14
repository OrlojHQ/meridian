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
	for index := range items {
		items[index] = publicConnection(items[index])
	}
	writeJSON(w, 200, map[string]any{
		"items": items, "enabled": s.service.ProviderGatewayEnabled(),
		"privateHttpAllowed": s.service.ProviderGatewayAllowsPrivateHTTP(),
		"chatgptAvailable":   s.service.ChatGPTAvailable(),
	})
}

// publicConnection drops persisted credential bookkeeping from responses.
func publicConnection(item domain.ProviderConnection) domain.ProviderConnection {
	item.CredentialGeneration = 0
	return item
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
	writeJSON(w, 200, publicConnection(item))
}

// ChatGPT sign-in responses carry an authorization URL but never a token,
// and are not cached.
func (s *Server) startChatGPTSignIn(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ConnectionID string `json:"connectionId"`
		Listen       bool   `json:"listen"`
	}
	if !decode(w, r, &input) {
		return
	}
	view, err := s.service.StartChatGPTSignIn(r.Context(), input.ConnectionID, input.Listen)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, view)
}
func (s *Server) chatGPTSignIn(w http.ResponseWriter, r *http.Request) {
	view, err := s.service.ChatGPTSignInStatus(r.Context(), r.PathValue("signInId"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, view)
}
func (s *Server) completeChatGPTSignIn(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CallbackURL string `json:"callbackUrl"`
	}
	if !decode(w, r, &input) {
		return
	}
	view, err := s.service.CompleteChatGPTSignIn(r.Context(), r.PathValue("signInId"), input.CallbackURL)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, view)
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
