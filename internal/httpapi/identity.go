package httpapi

import (
	"context"
	"errors"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/principal"
)

const (
	// userSessionCookie carries a member's session in team mode. Over TLS
	// the __Host- prefix pins it to this origin, Secure, and Path=/.
	userSessionCookie     = "meridian_user"
	hostUserSessionCookie = "__Host-meridian_user"
	// claimCookie carries a pending claim from the OIDC callback to the
	// claim request in the same browser. It lives ten minutes.
	claimCookie = "meridian_claim"
)

type sessionKey struct{}

// SetIdentity enables OIDC sign-in, the claim, and team mode sessions. A
// server without one stays in single-user mode.
func (s *Server) SetIdentity(identity *app.Identity) {
	s.identity = identity
	if identity != nil {
		// A disabled member's tickets stop at once, not at expiry.
		identity.OnMemberDisabled(func(_ context.Context, user domain.UserID) {
			s.tickets.revokeUser(user)
			s.previews.revokeUser(user)
		})
	}
}

// withIdentity answers not found when no Identity is configured.
func (s *Server) withIdentity(handler http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if s.identity == nil {
			writeError(writer, domain.ErrNotFound)
			return
		}
		handler(writer, request)
	}
}

func (s *Server) teamMode() bool {
	return s.identity != nil && s.identity.Mode() == domain.TeamMode
}

func userCookieName(request *http.Request) string {
	if request.TLS != nil {
		return hostUserSessionCookie
	}
	return userSessionCookie
}

func secureCookie(request *http.Request) bool {
	return request.TLS != nil || !isLoopbackRequest(request)
}

func (s *Server) setUserCookie(writer http.ResponseWriter, request *http.Request, token string) {
	http.SetCookie(writer, &http.Cookie{
		Name: userCookieName(request), Value: token, Path: "/",
		HttpOnly: true, Secure: secureCookie(request), SameSite: http.SameSiteStrictMode,
	})
}

func clearCookie(writer http.ResponseWriter, request *http.Request, name, path string) {
	http.SetCookie(writer, &http.Cookie{
		Name: name, Value: "", Path: path, MaxAge: -1,
		HttpOnly: true, Secure: secureCookie(request), SameSite: http.SameSiteStrictMode,
	})
}

// singleCookie returns name's value when the request carries it exactly
// once.
func singleCookie(request *http.Request, name string) (string, bool) {
	var value string
	found := false
	for _, cookie := range request.Cookies() {
		if cookie.Name == name {
			if found {
				return "", false
			}
			value, found = cookie.Value, true
		}
	}
	return value, found && value != ""
}

// breakGlassRoute is what the installation bearer may reach in team mode:
// the audit log and membership administration (ADR 0046).
func breakGlassRoute(request *http.Request) bool {
	path := request.URL.Path
	return (request.Method == http.MethodGet && (path == "/audit-records" || path == "/users")) ||
		strings.HasPrefix(path, "/users/")
}

func sessionFrom(ctx context.Context) string {
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}

type authenticationModeJSON struct {
	Mode      domain.InstallationMode `json:"mode"`
	SignIn    bool                    `json:"signIn"`
	Claimable bool                    `json:"claimable"`
}

func (s *Server) authenticationMode(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, authenticationModeJSON{
		Mode: s.identity.Mode(), SignIn: s.identity.SignInAvailable(), Claimable: s.identity.Claimable(),
	})
}

func (s *Server) startOIDCSignIn(writer http.ResponseWriter, request *http.Request) {
	if !s.identity.SignInAvailable() {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	target, err := s.identity.BeginSignIn(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(writer, request, target, http.StatusFound)
}

func (s *Server) completeOIDCSignIn(writer http.ResponseWriter, request *http.Request) {
	if !s.identity.SignInAvailable() {
		writeSignInPage(writer, http.StatusNotFound, "This Meridian installation has no sign-in configured.")
		return
	}
	query := request.URL.Query()
	if query.Get("error") != "" {
		writeSignInPage(writer, http.StatusBadRequest, "The identity provider did not sign you in. Start again from Meridian.")
		return
	}
	result, err := s.identity.CompleteSignIn(request.Context(), query.Get("state"), query.Get("code"))
	switch {
	case errors.Is(err, app.ErrSignInDenied):
		writeSignInPage(writer, http.StatusForbidden, "This account may not sign in to Meridian. Ask your Meridian administrator.")
		return
	case err != nil:
		writeSignInPage(writer, http.StatusBadRequest, "Sign-in did not finish. Start again from Meridian.")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	if result.ClaimToken != "" {
		http.SetCookie(writer, &http.Cookie{
			Name: claimCookie, Value: result.ClaimToken, Path: "/auth", MaxAge: int((10 * time.Minute).Seconds()),
			HttpOnly: true, Secure: secureCookie(request), SameSite: http.SameSiteStrictMode,
		})
		http.Redirect(writer, request, "/ui/settings/account?claim=ready", http.StatusSeeOther)
		return
	}
	s.setUserCookie(writer, request, result.SessionToken)
	http.Redirect(writer, request, "/ui/", http.StatusSeeOther)
}

func writeSignInPage(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>Meridian sign-in</title></head>
<body><p>` + html.EscapeString(message) + `</p><p><a href="/ui/">Return to Meridian</a></p></body></html>`))
}

// claimInstallation needs the installation bearer itself, not the browser
// cookie minted from it, plus the claim cookie from the OIDC callback.
func (s *Server) claimInstallation(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") == "" {
		writeUnauthorized(writer)
		return
	}
	token, ok := singleCookie(request, claimCookie)
	if !ok {
		writeError(writer, domain.ErrNotFound)
		return
	}
	session, err := s.identity.Claim(request.Context(), token)
	if err != nil {
		writeError(writer, err)
		return
	}
	clearCookie(writer, request, claimCookie, "/auth")
	clearCookie(writer, request, browserSessionCookie, "/")
	s.setUserCookie(writer, request, session)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

type memberJSON struct {
	Mode        domain.InstallationMode `json:"mode"`
	ID          string                  `json:"id"`
	Role        domain.UserRole         `json:"role"`
	DisplayName string                  `json:"displayName,omitempty"`
	Email       string                  `json:"email,omitempty"`
}

func (s *Server) currentMember(writer http.ResponseWriter, request *http.Request) {
	member, err := s.identity.Member(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, memberJSON{
		Mode: s.identity.Mode(), ID: string(member.ID), Role: member.Role,
		DisplayName: member.DisplayName, Email: member.Email,
	})
}

func (s *Server) signOut(writer http.ResponseWriter, request *http.Request) {
	session := sessionFrom(request.Context())
	if session == "" {
		writeError(writer, domain.ErrNotFound)
		return
	}
	if err := s.identity.SignOut(request.Context(), session); err != nil {
		writeError(writer, err)
		return
	}
	clearCookie(writer, request, userCookieName(request), "/")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

type sessionJSON struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt"`
	ExpiresAt  string `json:"expiresAt"`
	Revoked    bool   `json:"revoked"`
	Current    bool   `json:"current"`
}

func (s *Server) listSessions(writer http.ResponseWriter, request *http.Request) {
	sessions, err := s.identity.Sessions(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	current := sessionFrom(request.Context())
	items := make([]sessionJSON, len(sessions))
	for index, session := range sessions {
		items[index] = sessionJSON{
			ID: session.ID, CreatedAt: session.CreatedAt.UTC().Format(time.RFC3339Nano),
			LastUsedAt: session.LastUsedAt.UTC().Format(time.RFC3339Nano),
			ExpiresAt:  session.ExpiresAt.UTC().Format(time.RFC3339Nano),
			Revoked:    !session.RevokedAt.IsZero(), Current: session.ID == current,
		}
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, struct {
		Items []sessionJSON `json:"items"`
	}{Items: items})
}

func (s *Server) revokeSession(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("sessionId")
	if err := s.identity.RevokeSession(request.Context(), id); err != nil {
		writeError(writer, err)
		return
	}
	if id == sessionFrom(request.Context()) {
		clearCookie(writer, request, userCookieName(request), "/")
	}
	writer.WriteHeader(http.StatusNoContent)
}

// breakGlass reports a request that authenticated with the installation
// bearer in team mode.
func breakGlass(caller principal.Principal) bool {
	return caller.Kind() == principal.KindBreakGlass
}
