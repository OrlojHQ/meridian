package httpapi

import (
	"net/http"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func (s *Server) getGitHubConnection(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	state, err := s.service.GitHubConnection(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newGitHubConnectionJSON(state))
}

func (s *Server) checkGitHubConnection(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	state, err := s.service.CheckGitHubConnection(request.Context(), isLoopbackRequest(request))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newGitHubConnectionJSON(state))
}

func (s *Server) disconnectGitHub(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	var body struct {
		ExpectedResourceVersion int64 `json:"expectedResourceVersion"`
	}
	if !decodeLimit(writer, request, &body, 4096) {
		return
	}
	state, err := s.service.DisconnectGitHub(
		request.Context(), domain.ResourceVersion(body.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newGitHubConnectionJSON(state))
}

func (s *Server) getHostGitHubLogin(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	login, available, err := s.service.HostGitHubLogin(request.Context(), isLoopbackRequest(request))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, hostGitHubJSON{Available: available, Login: login})
}

func (s *Server) useHostGitHubLogin(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	var body struct {
		ExpectedResourceVersion int64 `json:"expectedResourceVersion"`
	}
	if !decodeLimit(writer, request, &body, 4096) {
		return
	}
	state, err := s.service.UseHostGitHub(
		request.Context(), isLoopbackRequest(request),
		domain.ResourceVersion(body.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newGitHubConnectionJSON(state))
}

func (s *Server) connectGitHubToken(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	var body struct {
		Token                   string `json:"token"`
		ExpectedResourceVersion int64  `json:"expectedResourceVersion"`
	}
	if !decodeLimit(writer, request, &body, 8192) {
		return
	}
	token := body.Token
	body.Token = ""
	state, err := s.service.ConnectGitHubToken(
		request.Context(), token, domain.ResourceVersion(body.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"),
	)
	token = ""
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, newGitHubConnectionJSON(state))
}

func (s *Server) beginGitHubDevice(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	var body struct {
		ExpectedResourceVersion int64 `json:"expectedResourceVersion"`
	}
	if !decodeLimit(writer, request, &body, 4096) {
		return
	}
	started, err := s.service.BeginGitHubDevice(request.Context(), domain.ResourceVersion(body.ExpectedResourceVersion))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, gitHubDeviceJSON{
		UserCode: started.UserCode, VerificationURI: started.VerificationURI,
		ExpiresAt: started.ExpiresAt, IntervalSeconds: started.IntervalSeconds,
	})
}

func (s *Server) pollGitHubDevice(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	status, err := s.service.PollGitHubDevice(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	response := gitHubDeviceStatusJSON{Status: status.Status}
	if status.Status == "connected" {
		encoded := newGitHubConnectionJSON(status.Connection)
		response.Connection = &encoded
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) cancelGitHubDevice(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if err := s.service.CancelGitHubDevice(request.Context()); err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) searchGitHubRepos(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	repos, err := s.service.SearchGitHubRepos(request.Context(), request.URL.Query().Get("q"))
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]gitHubRepoJSON, 0, len(repos))
	for _, repo := range repos {
		items = append(items, gitHubRepoJSON{
			FullName: repo.FullName, Private: repo.Private, Description: repo.Description,
			DefaultBranch: repo.DefaultBranch, URL: repo.URL,
		})
	}
	writeJSON(writer, http.StatusOK, gitHubRepoPageJSON{Items: items})
}

type gitHubConnectionJSON struct {
	Connected       bool       `json:"connected"`
	DeviceFlow      bool       `json:"deviceFlow"`
	Login           string     `json:"login,omitempty"`
	Status          string     `json:"status,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	Scopes          string     `json:"scopes,omitempty"`
	CreatedAt       *time.Time `json:"createdAt,omitempty"`
	UpdatedAt       *time.Time `json:"updatedAt,omitempty"`
	ResourceVersion int64      `json:"resourceVersion,omitempty"`
}

func newGitHubConnectionJSON(state app.GitHubConnectionState) gitHubConnectionJSON {
	encoded := gitHubConnectionJSON{Connected: state.Connected, DeviceFlow: state.DeviceFlow}
	if !state.Connected {
		return encoded
	}
	encoded.Login = state.Login
	encoded.Status = string(state.Status)
	encoded.Reason = string(state.Reason)
	encoded.Scopes = state.Scopes
	encoded.ResourceVersion = int64(state.ResourceVersion)
	if !state.CreatedAt.IsZero() {
		created := state.CreatedAt.UTC()
		encoded.CreatedAt = &created
	}
	if !state.UpdatedAt.IsZero() {
		updated := state.UpdatedAt.UTC()
		encoded.UpdatedAt = &updated
	}
	return encoded
}

type hostGitHubJSON struct {
	Available bool   `json:"available"`
	Login     string `json:"login,omitempty"`
}

type gitHubDeviceJSON struct {
	UserCode        string    `json:"userCode"`
	VerificationURI string    `json:"verificationUri"`
	ExpiresAt       time.Time `json:"expiresAt"`
	IntervalSeconds int       `json:"intervalSeconds"`
}

type gitHubDeviceStatusJSON struct {
	Status     string                `json:"status"`
	Connection *gitHubConnectionJSON `json:"connection,omitempty"`
}

type gitHubRepoJSON struct {
	FullName      string `json:"fullName"`
	Private       bool   `json:"private"`
	Description   string `json:"description,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	URL           string `json:"url"`
}

type gitHubRepoPageJSON struct {
	Items []gitHubRepoJSON `json:"items"`
}
