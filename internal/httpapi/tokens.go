package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

type tokenJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ReadOnly   bool   `json:"readOnly"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt"`
	ExpiresAt  string `json:"expiresAt"`
	Revoked    bool   `json:"revoked"`
}

func tokenResponse(token domain.PersonalAccessToken) tokenJSON {
	return tokenJSON{
		ID: token.ID, Name: token.Name, ReadOnly: token.ReadOnly,
		CreatedAt:  token.CreatedAt.UTC().Format(time.RFC3339Nano),
		LastUsedAt: token.LastUsedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:  token.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Revoked:    !token.RevokedAt.IsZero(),
	}
}

func (s *Server) listTokens(writer http.ResponseWriter, request *http.Request) {
	tokens, err := s.identity.Tokens(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]tokenJSON, len(tokens))
	for index, token := range tokens {
		items[index] = tokenResponse(token)
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, struct {
		Items []tokenJSON `json:"items"`
	}{Items: items})
}

type mintTokenRequest struct {
	Name          string `json:"name"`
	ReadOnly      bool   `json:"readOnly"`
	ExpiresInDays int    `json:"expiresInDays"`
}

// mintToken needs a browser session: a personal access token cannot mint
// another, so a leaked token cannot outlive its own expiry.
func (s *Server) mintToken(writer http.ResponseWriter, request *http.Request) {
	if sessionFrom(request.Context()) == "" {
		writeJSON(writer, http.StatusForbidden, errorEnvelope{
			Error: apiError{Code: "forbidden", Message: "mint personal access tokens from a signed-in browser"},
		})
		return
	}
	var input mintTokenRequest
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, fmt.Errorf("%w: invalid token request", domain.ErrInvalid))
		return
	}
	if input.ExpiresInDays < 0 || input.ExpiresInDays > 365 {
		writeError(writer, fmt.Errorf("%w: expiresInDays must be between 1 and 365", domain.ErrInvalid))
		return
	}
	minted, err := s.identity.MintToken(request.Context(), input.Name, input.ReadOnly,
		time.Duration(input.ExpiresInDays)*24*time.Hour)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusCreated, struct {
		tokenJSON
		Token string `json:"token"`
	}{tokenJSON: tokenResponse(minted.Record), Token: minted.Value})
}

func (s *Server) revokeToken(writer http.ResponseWriter, request *http.Request) {
	if err := s.identity.RevokeToken(request.Context(), request.PathValue("tokenId")); err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
