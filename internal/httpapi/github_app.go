package httpapi

import (
	"errors"
	"html"
	"io"
	"net/http"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func (s *Server) getGitHubApp(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	view, err := s.service.GitHubApp(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) beginGitHubAppSetup(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Organization string `json:"organization"`
	}
	if !decode(writer, request, &body) {
		return
	}
	setup, err := s.service.BeginGitHubAppSetup(request.Context(), body.Organization, request.Header.Get("Origin"), request.Host)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, setup)
}

func (s *Server) deleteGitHubApp(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedResourceVersion int64 `json:"expectedResourceVersion"`
	}
	if !decode(writer, request, &body) {
		return
	}
	if err := s.service.DeleteGitHubApp(request.Context(), domain.ResourceVersion(body.ExpectedResourceVersion)); err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncGitHubAppInstallations(writer http.ResponseWriter, request *http.Request) {
	view, err := s.service.SyncGitHubAppInstallations(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (s *Server) getGitHubAppAccess(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	projectID := domain.ProjectID(request.URL.Query().Get("projectId"))
	if projectID == "" {
		writeError(writer, domain.ErrInvalid)
		return
	}
	access, err := s.service.GitHubAppAccess(request.Context(), projectID)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, access)
}

func (s *Server) githubAppCallback(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	query := request.URL.Query()
	if query.Get("error") != "" {
		writeGitHubAppPage(writer, http.StatusBadRequest, "GitHub did not create the App. Start again from Meridian.")
		return
	}
	err := s.service.CompleteGitHubAppSetup(request.Context(), query.Get("state"), query.Get("code"))
	if err != nil {
		writeGitHubAppPage(writer, http.StatusBadRequest, "Meridian could not save the GitHub App. Start again from settings.")
		return
	}
	writeGitHubAppPage(writer, http.StatusOK, "The GitHub App is saved on this Station. Return to Meridian and install it on your account or organization. Choose All repositories if you want every private repository to work without another setup step.")
}

func (s *Server) githubAppHandoff(writer http.ResponseWriter, request *http.Request) {
	action, manifest, err := s.service.GitHubAppHandoff(request.URL.Query().Get("ticket"))
	if err != nil {
		writeGitHubAppPage(writer, http.StatusBadRequest, "This GitHub App setup has expired. Start again from Meridian.")
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action https://github.com; script-src 'unsafe-inline'")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>Create the GitHub App</title></head>
<body><p>Continue to GitHub to create the App. The private key comes back to this Station and is not shown.</p>
<form id="github-app-manifest" method="post" action="`+html.EscapeString(action)+`">
<input type="hidden" name="manifest" value="`+html.EscapeString(manifest)+`">
<button type="submit">Continue to GitHub</button>
</form>
<script>document.getElementById("github-app-manifest").submit()</script>
</body></html>`)
}

func (s *Server) githubAppSetupComplete(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	// The installation id in the query is not stored here. Settings asks
	// GitHub, with the App key, which installations exist.
	_ = request
	writeGitHubAppPage(writer, http.StatusOK, "If you installed the App, return to Meridian. Settings shows the accounts it is installed on.")
}

func (s *Server) githubAppWebhook(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20+1))
	if err != nil {
		writeError(writer, domain.ErrInvalid)
		return
	}
	if len(body) > 1<<20 {
		writer.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	err = s.service.ReceiveGitHubAppWebhook(
		request.Context(),
		request.Header.Get("X-GitHub-Event"),
		request.Header.Get("X-Hub-Signature-256"),
		body,
	)
	if errors.Is(err, app.ErrGitHubAppSignature) {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func writeGitHubAppPage(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>Meridian GitHub App</title></head>
<body><p>`+html.EscapeString(message)+`</p><p><a href="/ui/settings/account#github-app">Return to Meridian</a></p></body></html>`)
}
