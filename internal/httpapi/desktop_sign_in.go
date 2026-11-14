package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// desktopHandoffPath is where the desktop shell's loopback listener waits
// for the system browser (ADR 0047).
const desktopHandoffPath = "/meridian-sign-in"

// desktopSignInFrom reads the desktop shell's parameters from
// /auth/oidc/start. ok reports that they are present; valid is false when
// only some are, or any is malformed.
func desktopSignInFrom(request *http.Request) (desktop app.DesktopSignIn, ok, valid bool) {
	query := request.URL.Query()
	names := []string{"desktopPort", "desktopState", "desktopChallenge"}
	present := 0
	for _, name := range names {
		if query.Has(name) {
			present++
			if len(query[name]) != 1 {
				return app.DesktopSignIn{}, true, false
			}
		}
	}
	if present == 0 {
		return app.DesktopSignIn{}, false, true
	}
	if present != len(names) {
		return app.DesktopSignIn{}, true, false
	}
	port, err := strconv.Atoi(query.Get("desktopPort"))
	if err != nil {
		return app.DesktopSignIn{}, true, false
	}
	desktop = app.DesktopSignIn{Port: port, State: query.Get("desktopState"), Challenge: query.Get("desktopChallenge")}
	if app.ValidDesktopSignIn(desktop) != nil {
		return app.DesktopSignIn{}, true, false
	}
	return desktop, true, true
}

// desktopHandoffURL is always the IPv4 loopback literal, never a host name
// that could resolve elsewhere (RFC 8252 section 8.3).
func desktopHandoffURL(handoff app.DesktopHandoff) string {
	target := url.URL{
		Scheme: "http",
		Host:   "127.0.0.1:" + strconv.Itoa(handoff.Port),
		Path:   desktopHandoffPath,
		RawQuery: url.Values{
			"state":  {handoff.State},
			"ticket": {handoff.Ticket},
		}.Encode(),
	}
	return target.String()
}

type desktopRedeemedJSON struct {
	Next string `json:"next"`
}

// redeemDesktopSignIn is unauthenticated: the ticket and the verifier
// only the shell that began the sign-in holds are the credential. The
// shell's webview calls it from the gateway's own page, so the cookie it
// sets lands in the webview.
func (s *Server) redeemDesktopSignIn(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !sameOrigin(request) {
		writeJSON(writer, http.StatusForbidden, errorEnvelope{
			Error: apiError{Code: "forbidden", Message: "same-origin request required"},
		})
		return
	}
	var input struct {
		Ticket   string `json:"ticket"`
		Verifier string `json:"verifier"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Ticket == "" || input.Verifier == "" ||
		len(input.Ticket) > 128 || len(input.Verifier) > 128 {
		writeError(writer, fmt.Errorf("%w: ticket and verifier are required", domain.ErrInvalid))
		return
	}
	result, err := s.identity.RedeemDesktopSignIn(request.Context(), input.Ticket, input.Verifier)
	switch {
	case errors.Is(err, app.ErrSignInDenied):
		writeJSON(writer, http.StatusForbidden, errorEnvelope{
			Error: apiError{Code: "forbidden", Message: "this account may not sign in to Meridian"},
		})
		return
	case err != nil:
		writeError(writer, err)
		return
	}
	if result.ClaimToken != "" {
		setClaimCookie(writer, request, result.ClaimToken)
		writeJSON(writer, http.StatusOK, desktopRedeemedJSON{Next: "/ui/settings/account?claim=ready"})
		return
	}
	s.setUserCookie(writer, request, result.SessionToken)
	writeJSON(writer, http.StatusOK, desktopRedeemedJSON{Next: "/ui/"})
}
