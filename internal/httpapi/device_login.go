package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
)

type deviceLoginJSON struct {
	Handle                  string `json:"handle"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete,omitempty"`
	IntervalSeconds         int    `json:"intervalSeconds"`
	ExpiresAt               string `json:"expiresAt"`
}

// beginDeviceLogin is unauthenticated: it is how a terminal with no
// credential starts meridian login. The member approves at the issuer.
func (s *Server) beginDeviceLogin(writer http.ResponseWriter, request *http.Request) {
	login, err := s.identity.BeginDeviceLogin(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusCreated, deviceLoginJSON{
		Handle: login.Handle, UserCode: login.UserCode, VerificationURI: login.VerificationURI,
		VerificationURIComplete: login.VerificationURIComplete,
		IntervalSeconds:         int(login.Interval / time.Second),
		ExpiresAt:               login.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// pollDeviceLogin is authorized by the handle alone, which only the
// terminal that began the login holds. It answers 202 until the member
// approves, then the new token once.
func (s *Server) pollDeviceLogin(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Handle string `json:"handle"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Handle == "" || len(input.Handle) > 128 {
		writeError(writer, fmt.Errorf("%w: handle is required", domain.ErrInvalid))
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	token, wait, err := s.identity.PollDeviceLogin(request.Context(), input.Handle)
	switch {
	case errors.Is(err, app.ErrDevicePending):
		writeJSON(writer, http.StatusAccepted, struct {
			Status          string `json:"status"`
			IntervalSeconds int    `json:"intervalSeconds"`
		}{Status: "pending", IntervalSeconds: int(wait / time.Second)})
	case errors.Is(err, app.ErrSignInDenied):
		writeJSON(writer, http.StatusForbidden, errorEnvelope{
			Error: apiError{Code: "forbidden", Message: "sign-in was refused or expired"},
		})
	case err != nil:
		writeError(writer, err)
	default:
		writeJSON(writer, http.StatusOK, struct {
			Token string `json:"token"`
		}{Token: token})
	}
}
