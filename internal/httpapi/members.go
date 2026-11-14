package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

type memberAdminJSON struct {
	ID          string          `json:"id"`
	Role        domain.UserRole `json:"role"`
	DisplayName string          `json:"displayName,omitempty"`
	Email       string          `json:"email,omitempty"`
	Disabled    bool            `json:"disabled"`
	CreatedAt   string          `json:"createdAt"`
}

func (s *Server) listMembers(writer http.ResponseWriter, request *http.Request) {
	members, err := s.identity.Members(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]memberAdminJSON, len(members))
	for index, member := range members {
		items[index] = memberAdminJSON{
			ID: string(member.ID), Role: member.Role, DisplayName: member.DisplayName, Email: member.Email,
			Disabled: !member.DisabledAt.IsZero(), CreatedAt: member.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, struct {
		Items []memberAdminJSON `json:"items"`
	}{Items: items})
}

func memberID(request *http.Request) domain.UserID {
	return domain.UserID(request.PathValue("userId"))
}

func (s *Server) disableMember(writer http.ResponseWriter, request *http.Request) {
	if err := s.identity.DisableMember(request.Context(), memberID(request)); err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) enableMember(writer http.ResponseWriter, request *http.Request) {
	if err := s.identity.EnableMember(request.Context(), memberID(request)); err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) setMemberRole(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Role domain.UserRole `json:"role"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, fmt.Errorf("%w: invalid role request", domain.ErrInvalid))
		return
	}
	if err := s.identity.SetMemberRole(request.Context(), memberID(request), input.Role); err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) revokeMemberCredentials(writer http.ResponseWriter, request *http.Request) {
	if err := s.identity.RevokeMemberCredentials(request.Context(), memberID(request)); err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
