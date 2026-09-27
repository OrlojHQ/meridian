// Package httpapi adapts the Meridian application service to its OpenAPI routes.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/observability"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/webui"
	"github.com/coder/websocket"
	"github.com/felixge/httpsnoop"
)

type Server struct {
	service       *app.Service
	capabilities  ports.ProviderCapabilities
	ready         atomic.Bool
	mux           *http.ServeMux
	ui            http.Handler
	tickets       *attachTicketRegistry
	previews      *previewTicketRegistry
	previewBase   string
	metrics       *observability.Metrics
	apiToken      *apiauth.Token
	browserToken  *apiauth.Token
	browserValue  string
	harnessImages []domain.HarnessImage
}

const browserSessionCookie = "meridian_session"

func New(service *app.Service, token *apiauth.Token) *Server {
	return NewWithCapabilities(service, ports.ProviderCapabilities{}, token)
}

func NewWithCapabilities(
	service *app.Service,
	capabilities ports.ProviderCapabilities,
	token *apiauth.Token,
) *Server {
	return NewWithPreview(service, capabilities, "", token)
}

func NewWithPreview(
	service *app.Service,
	capabilities ports.ProviderCapabilities,
	previewBaseURL string,
	token *apiauth.Token,
) *Server {
	server := &Server{
		service:      service,
		capabilities: capabilities,
		mux:          http.NewServeMux(),
		ui:           webui.Handler(),
		tickets:      newAttachTicketRegistry(maxOutstandingAttachTickets),
		previews:     newPreviewTicketRegistry(maxPreviewTickets),
		previewBase:  previewBaseURL,
		apiToken:     token,
	}
	server.initializeBrowserSession()
	server.routes()
	return server
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if s.metrics == nil {
		s.serveHTTP(writer, request)
		return
	}
	started := time.Now()
	ctx, span := observability.StartSpan(request.Context(), "http.request")
	request = request.WithContext(ctx)
	measured := httpsnoop.CaptureMetrics(snoopHandlerFunc(s.serveHTTP), writer, request)
	span.End()
	s.metrics.HTTP(request.Method, request.Pattern, measured.Code, time.Since(started))
}

type snoopHandlerFunc func(http.ResponseWriter, *http.Request)

func (f snoopHandlerFunc) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	f(writer, request)
}

func (s *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/provider-gateway/") && s.service.ProviderGatewayEnabled() {
		s.service.ProviderGateway().ServeHTTP(writer, request)
		return
	}
	if request.URL.Path == "/" || request.URL.Path == "/ui" ||
		request.URL.Path == "/ui/" || strings.HasPrefix(request.URL.Path, "/ui/") {
		s.bootstrapLoopbackBrowser(writer, request)
		s.ui.ServeHTTP(writer, request)
		return
	}
	if !authExempt(request) {
		authenticated, browser := s.authenticate(request)
		if !authenticated {
			writeUnauthorized(writer)
			return
		}
		if browser && !safeMethod(request.Method) && !sameOrigin(request) {
			writeJSON(writer, http.StatusForbidden, errorEnvelope{
				Error: apiError{Code: "forbidden", Message: "same-origin request required"},
			})
			return
		}
	}
	s.mux.ServeHTTP(writer, request)
}

func (s *Server) ConfigureObservability(metrics *observability.Metrics) {
	s.metrics = metrics
}

func (s *Server) SetHarnessImages(items []domain.HarnessImage) {
	s.harnessImages = append([]domain.HarnessImage(nil), items...)
}

func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
	if s.metrics != nil {
		s.metrics.SetReady(ready)
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /projects/{projectId}/harnesses/{harness}/setup", s.projectHarnessSetup)
	s.mux.HandleFunc("PUT /projects/{projectId}/harnesses/{harness}/setup", s.projectHarnessSetup)
	s.mux.HandleFunc("GET /provider-connections", s.listProviderConnections)
	s.mux.HandleFunc("POST /provider-connections", s.putProviderConnection)
	s.mux.HandleFunc("PUT /provider-connections/{connectionId}", s.putProviderConnection)
	s.mux.HandleFunc("GET /projects/{projectId}/harnesses/{harness}/connection", s.projectProviderConnection)
	s.mux.HandleFunc("PUT /projects/{projectId}/harnesses/{harness}/connection", s.projectProviderConnection)
	s.mux.HandleFunc("GET /harness-setups/{setupId}/contents", s.harnessSetupContents)
	s.mux.HandleFunc("GET /harness-setups", s.listHarnessSetups)
	s.mux.HandleFunc("POST /harness-setups", s.importHarnessSetup)
	s.mux.HandleFunc("POST /harness-setups/preview", s.previewHarnessSetup)
	s.mux.HandleFunc("PATCH /harness-setups/{setupId}", s.mutateHarnessSetup)
	s.mux.HandleFunc("GET /harness-setups/{setupId}/revisions", s.listHarnessSetupRevisions)
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.readiness)
	s.mux.HandleFunc("POST /auth/browser-session", s.createBrowserSession)
	s.mux.HandleFunc("GET /capabilities", s.getCapabilities)
	s.mux.HandleFunc("POST /projects", s.createProject)
	s.mux.HandleFunc("GET /projects", s.listProjects)
	s.mux.HandleFunc("GET /projects/{projectId}", s.getProject)
	s.mux.HandleFunc("PATCH /projects/{projectId}", s.patchProject)
	s.mux.HandleFunc("POST /projects/{projectId}/threads", s.createProjectThread)
	s.mux.HandleFunc("GET /project-thread-intents/{intentId}", s.getProjectThreadIntent)
	s.mux.HandleFunc("PUT /secrets/{secretName}", s.putSecret)
	s.mux.HandleFunc("GET /secrets", s.listSecrets)
	s.mux.HandleFunc("DELETE /secrets/{secretName}", s.deleteSecret)
	s.mux.HandleFunc("POST /projects/{projectId}/capsules", s.createCapsule)
	s.mux.HandleFunc("GET /projects/{projectId}/capsules", s.listCapsules)
	s.mux.HandleFunc("GET /capsules/{capsuleId}", s.getCapsule)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/harness-profiles", s.listHarnessProfiles)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/threads", s.createThread)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/threads", s.listThreads)
	s.mux.HandleFunc("GET /threads/{threadId}", s.getThread)
	s.mux.HandleFunc("POST /threads/{threadId}/archive", s.archiveThread)
	s.mux.HandleFunc("POST /threads/{threadId}/delete", s.deleteThread)
	s.mux.HandleFunc("POST /threads/{threadId}/start", s.startThread)
	s.mux.HandleFunc("POST /threads/{threadId}/resume", s.resumeThread)
	s.mux.HandleFunc("POST /threads/{threadId}/messages", s.sendThreadMessage)
	s.mux.HandleFunc("POST /threads/{threadId}/responses", s.respondThread)
	s.mux.HandleFunc("POST /threads/{threadId}/cancel", s.cancelThread)
	s.mux.HandleFunc("GET /threads/{threadId}/blocks", s.listThreadBlocks)
	s.mux.HandleFunc("GET /threads/{threadId}/blocks/stream", s.streamThreadBlocks)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/previews", s.listPreviewPorts)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/previews/{port}/tickets", s.createPreviewTicket)
	s.mux.HandleFunc("GET /projects/{projectId}/environment", s.projectEnvironment)
	s.mux.HandleFunc("PUT /projects/{projectId}/environment", s.projectEnvironment)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/pause", s.pauseCapsule)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/resume", s.resumeCapsule)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/retry", func(w http.ResponseWriter, r *http.Request) { s.lifecycle(w, r, s.service.RetryCapsule) })
	s.mux.HandleFunc("POST /capsules/{capsuleId}/delete", s.deleteCapsule)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/runs", s.startRun)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/runs", s.listRuns)
	s.mux.HandleFunc("GET /runs/{runId}", s.getRun)
	s.mux.HandleFunc("POST /runs/{runId}/cancel", s.cancelRun)
	s.mux.HandleFunc("GET /runs/{runId}/events", s.runEvents)
	s.mux.HandleFunc("GET /runs/{runId}/events/stream", s.streamRunEvents)
	s.mux.HandleFunc("POST /runs/{runId}/attach-ticket", s.createAttachTicket)
	s.mux.HandleFunc("GET /runs/{runId}/attach", s.attachRun)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/git/status", s.gitStatus)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/git/diff", s.gitDiff)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/files", s.listCapsuleFiles)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/files/content", s.readCapsuleFile)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/workspace", s.exportWorkspace)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/deliveries/inspection", s.inspectDelivery)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/deliveries", s.createDelivery)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/deliveries", s.listDeliveries)
	s.mux.HandleFunc("GET /deliveries/{deliveryId}", s.getDelivery)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/moments", s.captureMoment)
	s.mux.HandleFunc("GET /capsules/{capsuleId}/moments", s.listMoments)
	s.mux.HandleFunc("GET /moments/{momentId}", s.getMoment)
	s.mux.HandleFunc("GET /timelines/{timelineId}", s.getTimeline)
	s.mux.HandleFunc("POST /moments/{momentId}/shards", s.createShard)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/rewind", s.rewindCapsule)
	s.mux.HandleFunc("POST /capsules/{capsuleId}/seal", s.sealCapsule)
}

func (s *Server) initializeBrowserSession() {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return
	}
	value := base64.RawURLEncoding.EncodeToString(random)
	for index := range random {
		random[index] = 0
	}
	token, err := apiauth.ParseToken(value)
	if err != nil {
		return
	}
	s.browserToken = token
	s.browserValue = value
}

func (s *Server) authenticate(request *http.Request) (authenticated, browser bool) {
	values := request.Header.Values("Authorization")
	if len(values) > 0 {
		if len(values) != 1 {
			return false, false
		}
		scheme, value, ok := strings.Cut(values[0], " ")
		return ok && strings.EqualFold(scheme, "Bearer") &&
			value != "" && !strings.ContainsAny(value, " \t\r\n") &&
			s.apiToken.Matches(value), false
	}
	var cookieValue string
	for _, cookie := range request.Cookies() {
		if cookie.Name == browserSessionCookie {
			if cookieValue != "" {
				return false, false
			}
			cookieValue = cookie.Value
		}
	}
	return cookieValue != "" && s.browserToken.Matches(cookieValue), true
}

func (s *Server) createBrowserSession(writer http.ResponseWriter, request *http.Request) {
	s.setBrowserCookie(writer, request)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) bootstrapLoopbackBrowser(writer http.ResponseWriter, request *http.Request) {
	if isLoopbackRequest(request) {
		s.setBrowserCookie(writer, request)
	}
}

func isLoopbackRequest(request *http.Request) bool {
	host := request.Host
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	} else {
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	remote, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false
	}
	remoteIP := net.ParseIP(remote)
	hostIP := net.ParseIP(host)
	hostIsLoopback := strings.EqualFold(host, "localhost") ||
		(hostIP != nil && hostIP.IsLoopback())
	return remoteIP != nil && remoteIP.IsLoopback() && hostIsLoopback
}

func (s *Server) setBrowserCookie(writer http.ResponseWriter, request *http.Request) {
	if s.browserValue == "" {
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: browserSessionCookie, Value: s.browserValue, Path: "/",
		HttpOnly: true, Secure: request.TLS != nil || !isLoopbackRequest(request),
		SameSite: http.SameSiteStrictMode,
	})
}

func authExempt(request *http.Request) bool {
	if request.Method == http.MethodGet &&
		(request.URL.Path == "/healthz" || request.URL.Path == "/readyz") {
		return true
	}
	if request.Method != http.MethodGet {
		return false
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	return len(parts) == 3 && parts[0] == "runs" && parts[1] != "" && parts[2] == "attach"
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		strings.EqualFold(parsed.Host, request.Host)
}

func writeUnauthorized(writer http.ResponseWriter) {
	writer.Header().Set("WWW-Authenticate", `Bearer realm="meridian"`)
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusUnauthorized, errorEnvelope{
		Error: apiError{Code: "unauthorized", Message: "authentication required"},
	})
}

func (s *Server) listHarnessProfiles(writer http.ResponseWriter, request *http.Request) {
	items, err := s.service.ListHarnessProfiles(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")))
	if err != nil {
		writeError(writer, err)
		return
	}
	response := make([]harnessProfileJSON, len(items))
	for index, item := range items {
		response[index] = harnessProfileJSON{
			Name: item.Name, Structured: item.Structured, AdapterKind: item.AdapterKind,
			Protocol: item.Protocol, PTY: item.PTY,
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": response})
}

func (s *Server) createThread(writer http.ResponseWriter, request *http.Request) {
	var input createThreadRequest
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.CreateThread(request.Context(), app.CreateThreadInput{
		CapsuleID: domain.CapsuleID(request.PathValue("capsuleId")), Harness: input.Harness,
		FirstMessage: input.FirstMessage, Start: input.Start,
		IdempotencyKey: request.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(result.Thread.ResourceVersion))
	writeJSON(writer, http.StatusCreated, s.threadMutationResponse(request.Context(), result))
}

func (s *Server) createProjectThread(writer http.ResponseWriter, request *http.Request) {
	var input createProjectThreadRequest
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.CreateProjectThread(
		request.Context(),
		app.CreateProjectThreadInput{
			ProjectID: domain.ProjectID(request.PathValue("projectId")),
			Name:      input.Name, Harness: input.Harness, Prompt: input.Prompt,
			IdempotencyKey: request.Header.Get("Idempotency-Key"),
		},
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(result.Intent.ResourceVersion))
	writeJSON(writer, http.StatusAccepted, s.projectThreadIntentResponse(request.Context(), result))
}

func (s *Server) getProjectThreadIntent(writer http.ResponseWriter, request *http.Request) {
	result, err := s.service.GetProjectThreadIntent(
		request.Context(),
		domain.ProjectThreadIntentID(request.PathValue("intentId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(result.Intent.ResourceVersion))
	writeJSON(writer, http.StatusOK, s.projectThreadIntentResponse(request.Context(), result))
}

func (s *Server) listThreads(writer http.ResponseWriter, request *http.Request) {
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	page, err := s.service.ListThreads(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")), offset, limit)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]threadJSON, len(page.Items))
	for index := range page.Items {
		items[index] = s.threadResponse(request.Context(), page.Items[index])
	}
	response := threadPageJSON{Items: items}
	if page.NextOffset > 0 {
		response.NextCursor = encodeCursor(page.NextOffset)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) getThread(writer http.ResponseWriter, request *http.Request) {
	thread, err := s.service.GetThread(
		request.Context(), domain.ThreadID(request.PathValue("threadId")))
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(thread.ResourceVersion))
	writeJSON(writer, http.StatusOK, s.threadResponse(request.Context(), thread))
}

func (s *Server) archiveThread(writer http.ResponseWriter, request *http.Request) {
	s.threadLifecycle(writer, request, s.service.ArchiveThread)
}

func (s *Server) deleteThread(writer http.ResponseWriter, request *http.Request) {
	var input deleteThreadRequest
	if !decode(writer, request, &input) {
		return
	}
	if input.Confirmation != "crypto-shred" {
		writeError(writer, fmt.Errorf("%w: confirmation must be crypto-shred", domain.ErrInvalid))
		return
	}
	thread, err := s.service.DeleteThread(
		request.Context(), domain.ThreadID(request.PathValue("threadId")),
		domain.ResourceVersion(input.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(thread.ResourceVersion))
	writeJSON(writer, http.StatusOK, s.threadResponse(request.Context(), thread))
}

func (s *Server) threadLifecycle(
	writer http.ResponseWriter,
	request *http.Request,
	operation func(context.Context, domain.ThreadID, domain.ResourceVersion, string) (domain.Thread, error),
) {
	var input lifecycleRequest
	if !decode(writer, request, &input) {
		return
	}
	thread, err := operation(
		request.Context(), domain.ThreadID(request.PathValue("threadId")),
		domain.ResourceVersion(input.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(thread.ResourceVersion))
	writeJSON(writer, http.StatusOK, s.threadResponse(request.Context(), thread))
}

func (s *Server) startThread(writer http.ResponseWriter, request *http.Request) {
	s.threadSessionLifecycle(writer, request, s.service.StartThread)
}

func (s *Server) resumeThread(writer http.ResponseWriter, request *http.Request) {
	s.threadSessionLifecycle(writer, request, s.service.ResumeThread)
}

func (s *Server) cancelThread(writer http.ResponseWriter, request *http.Request) {
	s.threadSessionLifecycle(writer, request, s.service.CancelThread)
}

func (s *Server) threadSessionLifecycle(
	writer http.ResponseWriter,
	request *http.Request,
	operation func(context.Context, domain.ThreadID, domain.ResourceVersion, string) (app.ThreadMutationResult, error),
) {
	var input lifecycleRequest
	if !decode(writer, request, &input) {
		return
	}
	result, err := operation(
		request.Context(), domain.ThreadID(request.PathValue("threadId")),
		domain.ResourceVersion(input.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(result.Thread.ResourceVersion))
	writeJSON(writer, http.StatusAccepted, s.threadMutationResponse(request.Context(), result))
}

func (s *Server) sendThreadMessage(writer http.ResponseWriter, request *http.Request) {
	var input sendThreadMessageRequest
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.SendThreadMessage(
		request.Context(), domain.ThreadID(request.PathValue("threadId")),
		domain.ResourceVersion(input.ExpectedResourceVersion), input.Content,
		request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(result.Thread.ResourceVersion))
	writeJSON(writer, http.StatusAccepted, s.threadMutationResponse(request.Context(), result))
}

func (s *Server) respondThread(writer http.ResponseWriter, request *http.Request) {
	var input threadResponseRequest
	if !decode(writer, request, &input) {
		return
	}
	if (input.Choice == nil) == (input.Input == nil) {
		writeError(writer, fmt.Errorf("%w: exactly one choice or input is required", domain.ErrInvalid))
		return
	}
	result, err := s.service.RespondThread(
		request.Context(), domain.ThreadID(request.PathValue("threadId")),
		domain.ResourceVersion(input.ExpectedResourceVersion), input.ResponseTo,
		input.Choice, input.Input, request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(result.Thread.ResourceVersion))
	writeJSON(writer, http.StatusAccepted, s.threadMutationResponse(request.Context(), result))
}

func (s *Server) listThreadBlocks(writer http.ResponseWriter, request *http.Request) {
	after, limit, err := threadBlockPageRequest(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	page, err := s.service.ThreadBlocks(
		request.Context(), domain.ThreadID(request.PathValue("threadId")), after, limit)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

func (s *Server) streamThreadBlocks(writer http.ResponseWriter, request *http.Request) {
	afterValue := request.URL.Query().Get("after")
	if last := request.Header.Get("Last-Event-ID"); last != "" {
		afterValue = last
	}
	after, err := strconv.ParseInt(defaultQuery(afterValue, "0"), 10, 64)
	if err != nil || after < 0 {
		writeError(writer, fmt.Errorf("%w: invalid Thread cursor", domain.ErrInvalid))
		return
	}
	initial, err := s.service.ThreadBlocks(
		request.Context(), domain.ThreadID(request.PathValue("threadId")), after, 100)
	if err != nil {
		writeError(writer, err)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()
	poll := time.NewTicker(250 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	page := initial
	for {
		for _, block := range page.Items {
			value, _ := json.Marshal(block)
			controller := http.NewResponseController(writer)
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(writer, "id: %d\nevent: block\ndata: %s\n\n",
				block.MessageSequence, value); err != nil {
				if s.metrics != nil {
					s.metrics.EventBackpressure("slow_client")
				}
				return
			}
			after = block.MessageSequence
		}
		if len(page.Items) > 0 {
			flusher.Flush()
		}
		thread, err := s.service.GetThread(
			request.Context(), domain.ThreadID(request.PathValue("threadId")))
		if err != nil {
			return
		}
		if thread.State == domain.ThreadArchived || thread.State == domain.ThreadDeleted {
			return
		}
		if thread.CurrentRunID != "" {
			run, runErr := s.service.GetRunStored(request.Context(), thread.CurrentRunID)
			if runErr == nil && run.State.Terminal() && !page.More {
				return
			}
		}
		select {
		case <-request.Context().Done():
			return
		case <-heartbeat.C:
			controller := http.NewResponseController(writer)
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(writer, ": heartbeat\n\n"); err != nil {
				if s.metrics != nil {
					s.metrics.EventBackpressure("slow_client")
				}
				return
			}
			flusher.Flush()
		case <-poll.C:
		}
		page, err = s.service.ThreadBlocks(
			request.Context(), domain.ThreadID(request.PathValue("threadId")), after, 100)
		if err != nil {
			return
		}
	}
}

func threadBlockPageRequest(request *http.Request) (int64, int, error) {
	after, err := strconv.ParseInt(defaultQuery(request.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		return 0, 0, fmt.Errorf("%w: invalid Thread cursor", domain.ErrInvalid)
	}
	limit := 100
	if raw := request.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			return 0, 0, fmt.Errorf("%w: Thread block limit must be between 1 and 1000", domain.ErrInvalid)
		}
	}
	return after, limit, nil
}

func (s *Server) captureMoment(writer http.ResponseWriter, request *http.Request) {
	var input captureMomentRequest
	if !decode(writer, request, &input) {
		return
	}
	moment, err := s.service.CaptureMoment(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")), input.Name,
		domain.ResourceVersion(input.ExpectedResourceVersion), request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, momentResponse(moment))
}

func (s *Server) listMoments(writer http.ResponseWriter, request *http.Request) {
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	page, err := s.service.ListMoments(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")), offset, limit,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]momentJSON, len(page.Items))
	for i := range page.Items {
		items[i] = momentResponse(page.Items[i])
	}
	response := momentPageJSON{Items: items}
	if page.NextOffset > 0 {
		response.NextCursor = encodeCursor(page.NextOffset)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) getMoment(writer http.ResponseWriter, request *http.Request) {
	moment, err := s.service.GetMoment(
		request.Context(), domain.MomentID(request.PathValue("momentId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, momentResponse(moment))
}

func (s *Server) getTimeline(writer http.ResponseWriter, request *http.Request) {
	view, err := s.service.GetTimeline(
		request.Context(), domain.TimelineID(request.PathValue("timelineId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	ancestry := make([]timelineJSON, len(view.Ancestry))
	for i := range view.Ancestry {
		ancestry[i] = timelineResponse(view.Ancestry[i])
	}
	writeJSON(writer, http.StatusOK, timelineViewJSON{
		Timeline: timelineResponse(view.Timeline), Ancestry: ancestry,
	})
}

func (s *Server) createShard(writer http.ResponseWriter, request *http.Request) {
	var input createRequest
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.CreateShard(
		request.Context(), domain.MomentID(request.PathValue("momentId")), input.Name,
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, descendantResponse(result))
}

func (s *Server) rewindCapsule(writer http.ResponseWriter, request *http.Request) {
	var input rewindRequest
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.Rewind(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
		domain.MomentID(input.MomentID), input.Name, request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, descendantResponse(result))
}

func (s *Server) sealCapsule(writer http.ResponseWriter, request *http.Request) {
	var input lifecycleRequest
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.Seal(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
		domain.ResourceVersion(input.ExpectedResourceVersion), request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	s.previews.revokeCapsule(domain.CapsuleID(request.PathValue("capsuleId")))
	writeJSON(writer, http.StatusCreated, sealResponse(result))
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, probeResponse{Status: "ok"})
}

func (s *Server) readiness(writer http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(writer, http.StatusServiceUnavailable, probeResponse{Status: "not_ready"})
		return
	}
	writeJSON(writer, http.StatusOK, probeResponse{Status: "ok"})
}

func (s *Server) getCapabilities(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, capabilitiesJSON{
		ProviderVersion: s.capabilities.Version,
		Attach:          s.capabilities.Attach,
		Run:             s.capabilities.Run,
		Structured:      s.capabilities.Structured,
		StructuredProtocol: func() string {
			if s.capabilities.Structured {
				return "meridian.adapter.v1"
			}
			return ""
		}(),
		PTYFallback:     s.capabilities.Run && s.capabilities.Attach,
		Git:             s.capabilities.Git,
		Pause:           s.capabilities.Pause,
		Snapshot:        s.capabilities.Snapshot,
		Clone:           s.capabilities.Clone,
		Preview:         s.capabilities.Preview && s.previewBase != "" && s.service != nil,
		Browse:          s.capabilities.Browse,
		Delivery:        s.capabilities.Delivery,
		ResourceMetrics: false,
		HarnessImages:   harnessImagesToJSON(s.harnessImages),
	})
}

func (s *Server) createProject(writer http.ResponseWriter, request *http.Request) {
	var input createProjectRequest
	if !decode(writer, request, &input) {
		return
	}
	project, err := s.service.CreateProjectConfigured(
		request.Context(),
		input.Name,
		app.ProjectConfiguration{
			RepositoryURL: input.RepositoryURL, Setup: input.Setup, ImageReference: input.ImageReference,
			HarnessImages:       harnessImagesFromRequest(input.HarnessImages),
			GitSecretName:       input.GitSecretName,
			HarnessSecretNames:  input.HarnessSecretNames,
			GitPushSecretName:   input.GitPushSecretName,
			GitHubAPISecretName: input.GitHubAPISecretName,
			CommitAuthorName:    input.CommitAuthorName,
			CommitAuthorEmail:   input.CommitAuthorEmail,
			DefaultBaseBranch:   input.DefaultBaseBranch,
		},
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(project.ResourceVersion))
	writeJSON(writer, http.StatusCreated, projectResponse(project))
}

func (s *Server) putSecret(writer http.ResponseWriter, request *http.Request) {
	var input putSecretRequest
	if !decode(writer, request, &input) {
		return
	}
	secret, err := s.service.PutSecret(
		request.Context(), request.PathValue("secretName"),
		app.SecretInput{
			Purpose: domain.SecretPurpose(input.Purpose), Value: input.Value,
			Username: input.Username, Password: input.Password,
		},
		domain.ResourceVersion(input.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(secret.ResourceVersion))
	writeJSON(writer, http.StatusOK, secretResponse(secret))
}

func (s *Server) listSecrets(writer http.ResponseWriter, request *http.Request) {
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	page, err := s.service.ListSecrets(request.Context(), offset, limit)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]secretJSON, len(page.Items))
	for index := range page.Items {
		items[index] = secretResponse(page.Items[index])
	}
	response := secretPageJSON{Items: items}
	if page.NextOffset > 0 {
		response.NextCursor = encodeCursor(page.NextOffset)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) deleteSecret(writer http.ResponseWriter, request *http.Request) {
	var input deleteSecretRequest
	if !decode(writer, request, &input) {
		return
	}
	secret, err := s.service.DeleteSecret(
		request.Context(), request.PathValue("secretName"),
		domain.ResourceVersion(input.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, secretResponse(secret))
}

func (s *Server) listProjects(writer http.ResponseWriter, request *http.Request) {
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	page, err := s.service.ListProjects(request.Context(), offset, limit)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]projectJSON, len(page.Items))
	for i := range page.Items {
		items[i] = projectResponse(page.Items[i])
	}
	response := projectPageJSON{Items: items}
	if page.NextOffset > 0 {
		response.NextCursor = encodeCursor(page.NextOffset)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) getProject(writer http.ResponseWriter, request *http.Request) {
	project, err := s.service.GetProject(
		request.Context(), domain.ProjectID(request.PathValue("projectId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(project.ResourceVersion))
	writeJSON(writer, http.StatusOK, projectResponse(project))
}

func (s *Server) patchProject(writer http.ResponseWriter, request *http.Request) {
	var input patchProjectRequest
	if !decode(writer, request, &input) {
		return
	}
	project, err := s.service.UpdateProjectHarnessImages(
		request.Context(),
		domain.ProjectID(request.PathValue("projectId")),
		harnessImagesFromRequest(input.HarnessImages),
		domain.ResourceVersion(input.ExpectedResourceVersion),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(project.ResourceVersion))
	writeJSON(writer, http.StatusOK, projectResponse(project))
}

func (s *Server) createCapsule(writer http.ResponseWriter, request *http.Request) {
	var input createRequest
	if !decode(writer, request, &input) {
		return
	}
	capsule, err := s.service.CreateCapsuleForHarness(
		request.Context(),
		domain.ProjectID(request.PathValue("projectId")),
		input.Name,
		input.Harness,
		request.Header.Get("Idempotency-Key"),
		input.Setup,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeCapsule(writer, http.StatusAccepted, capsule)
}

func (s *Server) listCapsules(writer http.ResponseWriter, request *http.Request) {
	projectID := domain.ProjectID(request.PathValue("projectId"))
	if _, err := s.service.GetProject(request.Context(), projectID); err != nil {
		writeError(writer, err)
		return
	}
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	page, err := s.service.ListCapsules(request.Context(), projectID, offset, limit)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]capsuleJSON, len(page.Items))
	for i := range page.Items {
		items[i] = capsuleResponse(page.Items[i])
	}
	response := capsulePageJSON{Items: items}
	if page.NextOffset > 0 {
		response.NextCursor = encodeCursor(page.NextOffset)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) getCapsule(writer http.ResponseWriter, request *http.Request) {
	capsule, err := s.service.GetCapsule(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeCapsule(writer, http.StatusOK, capsule)
}

func (s *Server) listPreviewPorts(writer http.ResponseWriter, request *http.Request) {
	if !s.capabilities.Preview || s.previewBase == "" || s.service == nil {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	items, err := s.service.DiscoverPreviewPorts(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	response := previewPortPageJSON{Items: make([]previewPortJSON, len(items))}
	for index, item := range items {
		response.Items[index] = previewPortJSON{Port: item.Port}
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) createPreviewTicket(writer http.ResponseWriter, request *http.Request) {
	if !s.capabilities.Preview || s.previewBase == "" || s.service == nil {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	portValue, err := strconv.ParseUint(request.PathValue("port"), 10, 16)
	if err != nil || portValue < 1024 || portValue == 7777 {
		writeError(writer, fmt.Errorf("%w: invalid preview port", domain.ErrInvalid))
		return
	}
	capsuleID := domain.CapsuleID(request.PathValue("capsuleId"))
	port := uint16(portValue)
	items, err := s.service.DiscoverPreviewPorts(request.Context(), capsuleID)
	if err != nil {
		writeError(writer, err)
		return
	}
	found := false
	for _, item := range items {
		found = found || item.Port == port
	}
	if !found {
		writeError(writer, domain.ErrNotFound)
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(previewTicketTTL)
	token, err := s.previews.mint(capsuleID, port, expiresAt, now)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, previewTicketJSON{
		URL:         previewURL(s.previewBase, token, capsuleID, port),
		ExpiresAt:   expiresAt.Format(time.RFC3339Nano),
		ReusePolicy: "reusable_until_expiry",
	})
}

func (s *Server) pauseCapsule(writer http.ResponseWriter, request *http.Request) {
	s.lifecycle(writer, request, s.service.PauseCapsule)
}

func (s *Server) resumeCapsule(writer http.ResponseWriter, request *http.Request) {
	s.lifecycle(writer, request, s.service.ResumeCapsule)
}

func (s *Server) deleteCapsule(writer http.ResponseWriter, request *http.Request) {
	s.lifecycle(writer, request, func(
		ctx context.Context,
		id domain.CapsuleID,
		version domain.ResourceVersion,
		key string,
	) (domain.Capsule, error) {
		capsule, err := s.service.DeleteCapsule(ctx, id, version, key)
		if err == nil {
			s.previews.revokeCapsule(id)
		}
		return capsule, err
	})
}

func (s *Server) lifecycle(
	writer http.ResponseWriter,
	request *http.Request,
	operation func(
		context.Context,
		domain.CapsuleID,
		domain.ResourceVersion,
		string,
	) (domain.Capsule, error),
) {
	var input lifecycleRequest
	if !decode(writer, request, &input) {
		return
	}
	capsule, err := operation(
		request.Context(),
		domain.CapsuleID(request.PathValue("capsuleId")),
		domain.ResourceVersion(input.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeCapsule(writer, http.StatusAccepted, capsule)
}

func (s *Server) startRun(writer http.ResponseWriter, request *http.Request) {
	var input startRunRequest
	if !decode(writer, request, &input) {
		return
	}
	run, err := s.service.StartRun(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
		input.Harness, input.Prompt, request.Header.Get("Idempotency-Key"),
		input.Columns, input.Rows,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeRun(writer, http.StatusAccepted, run)
}

func (s *Server) listRuns(writer http.ResponseWriter, request *http.Request) {
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	page, err := s.service.ListRuns(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")), offset, limit,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]runJSON, len(page.Items))
	for index := range page.Items {
		items[index] = runResponse(page.Items[index])
	}
	response := runPageJSON{Items: items}
	if page.NextOffset > 0 {
		response.NextCursor = encodeCursor(page.NextOffset)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) getRun(writer http.ResponseWriter, request *http.Request) {
	run, err := s.service.GetRun(request.Context(), domain.RunID(request.PathValue("runId")))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeRun(writer, http.StatusOK, run)
}

func (s *Server) cancelRun(writer http.ResponseWriter, request *http.Request) {
	var input lifecycleRequest
	if !decode(writer, request, &input) {
		return
	}
	run, err := s.service.CancelRun(
		request.Context(), domain.RunID(request.PathValue("runId")),
		domain.ResourceVersion(input.ExpectedResourceVersion),
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeRun(writer, http.StatusAccepted, run)
}

func (s *Server) runEvents(writer http.ResponseWriter, request *http.Request) {
	after, err := strconv.ParseInt(defaultQuery(request.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		writeError(writer, fmt.Errorf("%w: invalid event cursor", domain.ErrInvalid))
		return
	}
	limit := 100
	if value := request.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 1000 {
			writeError(writer, fmt.Errorf("%w: event limit must be between 1 and 1000", domain.ErrInvalid))
			return
		}
	}
	page, err := s.service.RunEvents(
		request.Context(), domain.RunID(request.PathValue("runId")), after, limit,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]runEventJSON, len(page.Items))
	for index, event := range page.Items {
		items[index] = runEventJSON{
			Sequence: event.Sequence, Type: event.Type,
			Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano),
			Metadata:  json.RawMessage(event.Data),
		}
		if len(event.Data) == 0 {
			items[index].Metadata = nil
		}
	}
	writeJSON(writer, http.StatusOK, runEventPageJSON{
		Items: items, NextCursor: page.NextCursor, More: page.More,
	})
}

func (s *Server) streamRunEvents(writer http.ResponseWriter, request *http.Request) {
	afterValue := request.URL.Query().Get("after")
	if last := request.Header.Get("Last-Event-ID"); last != "" {
		afterValue = last
	}
	after, err := strconv.ParseInt(defaultQuery(afterValue, "0"), 10, 64)
	if err != nil || after < 0 {
		writeError(writer, fmt.Errorf("%w: invalid event cursor", domain.ErrInvalid))
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := s.service.RunEvents(
			request.Context(), domain.RunID(request.PathValue("runId")), after, 100,
		)
		if err != nil {
			return
		}
		for _, event := range page.Items {
			value, _ := json.Marshal(runEventJSON{
				Sequence: event.Sequence, Type: event.Type,
				Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano),
				Metadata:  json.RawMessage(event.Data),
			})
			if _, err := fmt.Fprintf(
				writer, "id: %d\nevent: %s\ndata: %s\n\n",
				event.Sequence, event.Type, value,
			); err != nil {
				if s.metrics != nil {
					s.metrics.EventBackpressure("slow_client")
				}
				return
			}
			after = event.Sequence
		}
		if len(page.Items) > 0 {
			flusher.Flush()
		}
		run, err := s.service.GetRun(request.Context(), domain.RunID(request.PathValue("runId")))
		if err != nil {
			return
		}
		if run.State.Terminal() && !page.More {
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) gitStatus(writer http.ResponseWriter, request *http.Request) {
	result, err := s.service.GitStatus(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, gitJSON{Content: result.Content, Truncated: result.Truncated})
}

func (s *Server) gitDiff(writer http.ResponseWriter, request *http.Request) {
	result, err := s.service.GitDiff(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, gitJSON{Content: result.Content, Truncated: result.Truncated})
}

func (s *Server) listCapsuleFiles(writer http.ResponseWriter, request *http.Request) {
	if !s.capabilities.Browse {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	limit := 0
	if value := request.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 512 {
			writeError(writer, fmt.Errorf("%w: file limit must be between 1 and 512", domain.ErrInvalid))
			return
		}
	}
	result, err := s.service.ListWorkspaceFiles(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
		request.URL.Query().Get("path"), request.URL.Query().Get("after"), limit,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]workspaceFileEntryJSON, len(result.Items))
	for index, item := range result.Items {
		items[index] = workspaceFileEntryJSON{
			Name: item.Name, Type: item.Type, Size: item.Size, Executable: item.Executable,
		}
	}
	writeJSON(writer, http.StatusOK, workspaceFilePageJSON{
		Path: result.Path, Items: items, NextAfter: result.NextAfter,
	})
}

func (s *Server) readCapsuleFile(writer http.ResponseWriter, request *http.Request) {
	if !s.capabilities.Browse {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	result, err := s.service.ReadWorkspaceFile(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
		request.URL.Query().Get("path"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, workspaceFileJSON{
		Path: result.Path, Content: result.Content, Size: result.Size,
		Executable: result.Executable,
	})
}

func (s *Server) exportWorkspace(writer http.ResponseWriter, request *http.Request) {
	if !s.capabilities.Snapshot {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	result, err := s.service.ExportWorkspace(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	defer result.Archive.Close()
	encode := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	writer.Header().Set("Content-Type", "application/x-tar")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Meridian-Repository-URL", encode(result.RepositoryURL))
	writer.Header().Set("Meridian-Git-Branch", encode(result.Metadata.GitBranch))
	writer.Header().Set("Meridian-Git-Head", encode(result.Metadata.GitHEAD))
	writer.Header().Set("Meridian-Git-Dirty", encode(result.Metadata.GitDirtySummary))
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, result.Archive)
}

func (s *Server) inspectDelivery(writer http.ResponseWriter, request *http.Request) {
	if !s.capabilities.Delivery {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	result, err := s.service.InspectDelivery(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, deliveryInspectionJSON{
		CapsuleResourceVersion: int64(result.CapsuleResourceVersion),
		HEAD:                   result.HEAD, Branch: result.Branch, Dirty: result.Dirty,
		OriginURL: result.OriginURL, DefaultBranch: result.DefaultBranch, Tree: result.Tree,
	})
}

func (s *Server) createDelivery(writer http.ResponseWriter, request *http.Request) {
	if !s.capabilities.Delivery {
		writeError(writer, domain.ErrUnsupported)
		return
	}
	var input createDeliveryRequest
	if !decode(writer, request, &input) {
		return
	}
	result, err := s.service.CreateDelivery(request.Context(), app.CreateDeliveryInput{
		CapsuleID: domain.CapsuleID(request.PathValue("capsuleId")),
		Action:    domain.DeliveryAction(input.Action), Approved: input.Approved,
		ExpectedCapsuleVersion: domain.ResourceVersion(input.ExpectedResourceVersion),
		ExpectedHEAD:           input.ExpectedHead, ExpectedTree: input.ExpectedTree,
		RemoteBranch: input.RemoteBranch, CommitMessage: input.CommitMessage,
		PullRequestTitle: input.PullRequestTitle, PullRequestBody: input.PullRequestBody,
		BaseBranch: input.BaseBranch, IdempotencyKey: request.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(result.ResourceVersion))
	writeJSON(writer, http.StatusCreated, deliveryResponse(result))
}

func (s *Server) getDelivery(writer http.ResponseWriter, request *http.Request) {
	result, err := s.service.GetDelivery(
		request.Context(), domain.DeliveryID(request.PathValue("deliveryId")),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(result.ResourceVersion))
	writeJSON(writer, http.StatusOK, deliveryResponse(result))
}

func (s *Server) listDeliveries(writer http.ResponseWriter, request *http.Request) {
	offset, limit, err := pagination(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	page, err := s.service.ListDeliveries(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")), offset, limit,
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]deliveryJSON, len(page.Items))
	for index, item := range page.Items {
		items[index] = deliveryResponse(item)
	}
	response := deliveryPageJSON{Items: items}
	if page.NextOffset > 0 {
		response.NextCursor = encodeCursor(page.NextOffset)
	}
	writeJSON(writer, http.StatusOK, response)
}

type attachTicket struct {
	RunID     domain.RunID
	ExpiresAt time.Time
	After     uint64
}

func (s *Server) createAttachTicket(writer http.ResponseWriter, request *http.Request) {
	runID := domain.RunID(request.PathValue("runId"))
	if _, err := s.service.GetRun(request.Context(), runID); err != nil {
		writeError(writer, err)
		return
	}
	after, err := strconv.ParseUint(defaultQuery(request.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil {
		writeError(writer, fmt.Errorf("%w: invalid attach cursor", domain.ErrInvalid))
		return
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		writeError(writer, err)
		return
	}
	value := base64.RawURLEncoding.EncodeToString(random)
	expiry := time.Now().UTC().Add(30 * time.Second)
	if err := s.tickets.mint(
		value, attachTicket{RunID: runID, ExpiresAt: expiry, After: after}, time.Now().UTC(),
	); err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, attachTicketJSON{
		Ticket: value, ExpiresAt: expiry.Format(time.RFC3339Nano),
		WebSocketPath: "/runs/" + url.PathEscape(string(runID)) + "/attach",
	})
}

func (s *Server) attachRun(writer http.ResponseWriter, request *http.Request) {
	if !allowedOrigin(request.Header.Get("Origin")) {
		writeError(writer, fmt.Errorf("%w: WebSocket origin is not allowed", domain.ErrInvalid))
		return
	}
	ticketValue := request.URL.Query().Get("ticket")
	ticket, found := s.tickets.take(ticketValue, time.Now().UTC())
	if !found {
		writeError(writer, fmt.Errorf("%w: attach ticket is invalid", domain.ErrInvalid))
		return
	}
	if ticket.RunID != domain.RunID(request.PathValue("runId")) {
		writeError(writer, fmt.Errorf("%w: attach ticket is expired or out of scope", domain.ErrInvalid))
		return
	}
	upstream, err := s.service.AttachRun(request.Context(), ticket.RunID, ticket.After)
	if err != nil {
		writeError(writer, err)
		return
	}
	defer upstream.Close()
	if s.metrics != nil {
		s.metrics.PTY(1)
		defer s.metrics.PTY(-1)
	}
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost", "127.0.0.1", "[::1]"},
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(64 << 10)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	errorsChannel := make(chan error, 2)
	go func() {
		for {
			binary, value, err := upstream.Read(ctx)
			if err != nil {
				errorsChannel <- err
				return
			}
			messageType := websocket.MessageText
			if binary {
				messageType = websocket.MessageBinary
			}
			if err := connection.Write(ctx, messageType, value); err != nil {
				errorsChannel <- err
				return
			}
		}
	}()
	go func() {
		for {
			messageType, value, err := connection.Read(ctx)
			if err != nil {
				errorsChannel <- err
				return
			}
			if err := upstream.Write(ctx, messageType == websocket.MessageBinary, value); err != nil {
				errorsChannel <- err
				return
			}
		}
	}()
	<-errorsChannel
}

func allowedOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	return (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		(host == "localhost" || host == "127.0.0.1" || host == "::1")
}

func defaultQuery(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeRun(writer http.ResponseWriter, status int, run domain.Run) {
	writer.Header().Set("ETag", etag(run.ResourceVersion))
	writeJSON(writer, status, runResponse(run))
}

func writeCapsule(writer http.ResponseWriter, status int, capsule domain.Capsule) {
	writer.Header().Set("ETag", etag(capsule.ResourceVersion))
	writeJSON(writer, status, capsuleResponse(capsule))
}

func decode(writer http.ResponseWriter, request *http.Request, output any) bool {
	return decodeLimit(writer, request, output, 1<<20)
}
func decodeLimit(writer http.ResponseWriter, request *http.Request, output any, limit int64) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		writeError(writer, fmt.Errorf("%w: invalid JSON body", domain.ErrInvalid))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, fmt.Errorf("%w: request body must contain one JSON value", domain.ErrInvalid))
		return false
	}
	return true
}

func pagination(request *http.Request) (int, int, error) {
	offset := 0
	if raw := request.URL.Query().Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: invalid cursor", domain.ErrInvalid)
		}
		offset, err = strconv.Atoi(string(decoded))
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("%w: invalid cursor", domain.ErrInvalid)
		}
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, fmt.Errorf("%w: limit must be between 1 and 100", domain.ErrInvalid)
		}
	}
	return offset, limit, nil
}

func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func etag(version domain.ResourceVersion) string {
	return `"` + strconv.FormatInt(int64(version), 10) + `"`
}

func writeError(writer http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	message := "internal server error"
	switch {
	case errors.Is(err, domain.ErrInvalid):
		status, code, message = http.StatusBadRequest, "invalid_request", err.Error()
	case errors.Is(err, domain.ErrNotFound):
		status, code, message = http.StatusNotFound, "not_found", "resource not found"
	case errors.Is(err, domain.ErrConflict):
		status, code, message = http.StatusConflict, "conflict", err.Error()
	case errors.Is(err, domain.ErrCorrupt):
		status, code, message = http.StatusConflict, "artifact_corrupt", err.Error()
	case errors.Is(err, domain.ErrTranscriptCorrupt):
		status, code, message = http.StatusConflict, "transcript_corrupt", "encrypted transcript is corrupt"
	case errors.Is(err, domain.ErrKeyMismatch):
		status, code, message = http.StatusLocked, "transcript_locked", "transcript key is missing or does not match"
	case errors.Is(err, domain.ErrIllegalTransition):
		status, code, message = http.StatusUnprocessableEntity, "illegal_transition", err.Error()
	case errors.Is(err, domain.ErrUnsupported):
		status, code, message = http.StatusUnprocessableEntity, "unsupported", err.Error()
	case errors.Is(err, domain.ErrResourceExhausted):
		status, code, message = http.StatusTooManyRequests, "capacity_exhausted", err.Error()
	}
	writeJSON(writer, status, errorEnvelope{
		Error: apiError{Code: code, Message: message},
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

type probeResponse struct {
	Status string `json:"status"`
}

type capabilitiesJSON struct {
	ProviderVersion    string             `json:"providerVersion"`
	Attach             bool               `json:"attach"`
	Run                bool               `json:"run"`
	Structured         bool               `json:"structured"`
	StructuredProtocol string             `json:"structuredProtocol,omitempty"`
	PTYFallback        bool               `json:"ptyFallback"`
	Git                bool               `json:"git"`
	Pause              bool               `json:"pause"`
	Snapshot           bool               `json:"snapshot"`
	Clone              bool               `json:"clone"`
	Preview            bool               `json:"preview"`
	Browse             bool               `json:"browse"`
	Delivery           bool               `json:"delivery"`
	ResourceMetrics    bool               `json:"resourceMetrics"`
	HarnessImages      []harnessImageJSON `json:"harnessImages,omitempty"`
}

type createRequest struct {
	Setup   string `json:"setup,omitempty"`
	Name    string `json:"name"`
	Harness string `json:"harness,omitempty"`
}

type createProjectRequest struct {
	Name                string             `json:"name"`
	RepositoryURL       string             `json:"repositoryUrl,omitempty"`
	Setup               []string           `json:"setup,omitempty"`
	ImageReference      string             `json:"imageReference,omitempty"`
	HarnessImages       []harnessImageJSON `json:"harnessImages,omitempty"`
	GitSecretName       string             `json:"gitSecretName,omitempty"`
	HarnessSecretNames  []string           `json:"harnessSecretNames,omitempty"`
	GitPushSecretName   string             `json:"gitPushSecretName,omitempty"`
	GitHubAPISecretName string             `json:"githubAPISecretName,omitempty"`
	CommitAuthorName    string             `json:"commitAuthorName,omitempty"`
	CommitAuthorEmail   string             `json:"commitAuthorEmail,omitempty"`
	DefaultBaseBranch   string             `json:"defaultBaseBranch,omitempty"`
}

type harnessImageJSON struct {
	Name           string `json:"name"`
	ImageReference string `json:"imageReference"`
}

type patchProjectRequest struct {
	ExpectedResourceVersion int64              `json:"expectedResourceVersion"`
	HarnessImages           []harnessImageJSON `json:"harnessImages"`
}

type createDeliveryRequest struct {
	Action                  string `json:"action"`
	Approved                bool   `json:"approved"`
	ExpectedResourceVersion int64  `json:"expectedResourceVersion"`
	ExpectedHead            string `json:"expectedHead"`
	ExpectedTree            string `json:"expectedTree"`
	RemoteBranch            string `json:"remoteBranch"`
	CommitMessage           string `json:"commitMessage,omitempty"`
	PullRequestTitle        string `json:"pullRequestTitle,omitempty"`
	PullRequestBody         string `json:"pullRequestBody,omitempty"`
	BaseBranch              string `json:"baseBranch,omitempty"`
}

type putSecretRequest struct {
	Purpose                 string `json:"purpose"`
	Value                   string `json:"value,omitempty"`
	Username                string `json:"username,omitempty"`
	Password                string `json:"password,omitempty"`
	ExpectedResourceVersion int64  `json:"expectedResourceVersion,omitempty"`
}

type deleteSecretRequest struct {
	ExpectedResourceVersion int64 `json:"expectedResourceVersion"`
}

type lifecycleRequest struct {
	ExpectedResourceVersion int64 `json:"expectedResourceVersion"`
}

type captureMomentRequest struct {
	Name                    string `json:"name"`
	ExpectedResourceVersion int64  `json:"expectedResourceVersion"`
}

type rewindRequest struct {
	MomentID string `json:"momentId"`
	Name     string `json:"name"`
}

type startRunRequest struct {
	Harness string `json:"harness"`
	Prompt  string `json:"prompt"`
	Columns uint16 `json:"columns,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
}

type createThreadRequest struct {
	Harness      string `json:"harness"`
	FirstMessage string `json:"firstMessage,omitempty"`
	Start        bool   `json:"start,omitempty"`
}

type createProjectThreadRequest struct {
	Name    string `json:"name,omitempty"`
	Harness string `json:"harness"`
	Prompt  string `json:"prompt"`
}

type harnessProfileJSON struct {
	Name        string `json:"name"`
	Structured  bool   `json:"structured"`
	AdapterKind string `json:"adapterKind,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	PTY         bool   `json:"pty"`
}

type deleteThreadRequest struct {
	ExpectedResourceVersion int64  `json:"expectedResourceVersion"`
	Confirmation            string `json:"confirmation"`
}

type sendThreadMessageRequest struct {
	ExpectedResourceVersion int64  `json:"expectedResourceVersion"`
	Content                 string `json:"content"`
}

type threadResponseRequest struct {
	ExpectedResourceVersion int64   `json:"expectedResourceVersion"`
	ResponseTo              string  `json:"responseTo"`
	Choice                  *string `json:"choice,omitempty"`
	Input                   *string `json:"input,omitempty"`
}

type projectJSON struct {
	ID                  string             `json:"id"`
	Name                string             `json:"name"`
	RepositoryURL       string             `json:"repositoryUrl,omitempty"`
	Setup               []string           `json:"setup,omitempty"`
	ImageReference      string             `json:"imageReference,omitempty"`
	HarnessImages       []harnessImageJSON `json:"harnessImages,omitempty"`
	GitSecretName       string             `json:"gitSecretName,omitempty"`
	HarnessSecretNames  []string           `json:"harnessSecretNames,omitempty"`
	GitPushSecretName   string             `json:"gitPushSecretName,omitempty"`
	GitHubAPISecretName string             `json:"githubAPISecretName,omitempty"`
	CommitAuthorName    string             `json:"commitAuthorName,omitempty"`
	CommitAuthorEmail   string             `json:"commitAuthorEmail,omitempty"`
	DefaultBaseBranch   string             `json:"defaultBaseBranch,omitempty"`
	CreatedAt           string             `json:"createdAt"`
	UpdatedAt           string             `json:"updatedAt"`
	ResourceVersion     int64              `json:"resourceVersion"`
}

type secretJSON struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Purpose         string `json:"purpose"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	ResourceVersion int64  `json:"resourceVersion"`
}

type secretPageJSON struct {
	Items      []secretJSON `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

type capsuleJSON struct {
	Preparation     *domain.PreparationProgress `json:"preparation,omitempty"`
	ID              string                      `json:"id"`
	ProjectID       string                      `json:"projectId"`
	TimelineID      string                      `json:"timelineId"`
	OriginMomentID  string                      `json:"originMomentId,omitempty"`
	Name            string                      `json:"name"`
	Harness         string                      `json:"harness,omitempty"`
	State           string                      `json:"state"`
	DesiredState    string                      `json:"desiredState"`
	Failure         string                      `json:"failure,omitempty"`
	RestoreComplete bool                        `json:"restoreComplete"`
	Maintenance     string                      `json:"maintenance,omitempty"`
	CreatedAt       string                      `json:"createdAt"`
	UpdatedAt       string                      `json:"updatedAt"`
	ResourceVersion int64                       `json:"resourceVersion"`
}

type momentJSON struct {
	ID               string `json:"id"`
	ProjectID        string `json:"projectId"`
	CapsuleID        string `json:"capsuleId"`
	TimelineID       string `json:"timelineId"`
	ParentMomentID   string `json:"parentMomentId,omitempty"`
	Name             string `json:"name"`
	ArchiveSHA256    string `json:"archiveSha256"`
	ArchiveSize      int64  `json:"archiveSize"`
	ManifestSHA256   string `json:"manifestSha256"`
	ImageDigest      string `json:"imageDigest"`
	ProjectSetupHash string `json:"projectSetupHash"`
	GitBranch        string `json:"gitBranch,omitempty"`
	GitHEAD          string `json:"gitHead,omitempty"`
	GitDirtySummary  string `json:"gitDirtySummary,omitempty"`
	CreatedAt        string `json:"createdAt"`
	Final            bool   `json:"final"`
}

type momentPageJSON struct {
	Items      []momentJSON `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

type timelineJSON struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"projectId"`
	CapsuleID          string `json:"capsuleId"`
	ForkedFromMomentID string `json:"forkedFromMomentId,omitempty"`
	Reason             string `json:"reason"`
	CreatedAt          string `json:"createdAt"`
}

type timelineViewJSON struct {
	Timeline timelineJSON   `json:"timeline"`
	Ancestry []timelineJSON `json:"ancestry"`
}

type descendantJSON struct {
	Capsule  capsuleJSON  `json:"capsule"`
	Timeline timelineJSON `json:"timeline"`
	Reason   string       `json:"reason"`
}

type sealJSON struct {
	Capsule capsuleJSON `json:"capsule"`
	Moment  momentJSON  `json:"moment"`
}

type projectPageJSON struct {
	Items      []projectJSON `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type capsulePageJSON struct {
	Items      []capsuleJSON `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type runJSON struct {
	ID              string `json:"id"`
	CapsuleID       string `json:"capsuleId"`
	Harness         string `json:"harness"`
	State           string `json:"state"`
	ExitStatus      *int   `json:"exitStatus,omitempty"`
	Failure         string `json:"failure,omitempty"`
	CreatedAt       string `json:"createdAt"`
	StartedAt       string `json:"startedAt,omitempty"`
	FinishedAt      string `json:"finishedAt,omitempty"`
	UpdatedAt       string `json:"updatedAt"`
	ResourceVersion int64  `json:"resourceVersion"`
}

type runPageJSON struct {
	Items      []runJSON `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

type threadJSON struct {
	ID                  string              `json:"id"`
	CapsuleID           string              `json:"capsuleId"`
	State               string              `json:"state"`
	Harness             string              `json:"harness"`
	CurrentRunID        string              `json:"currentRunId,omitempty"`
	CurrentRunState     string              `json:"currentRunState,omitempty"`
	Awaiting            *threadAwaitingJSON `json:"awaiting,omitempty"`
	Protocol            string              `json:"protocol,omitempty"`
	StructuredSupported bool                `json:"structuredSupported"`
	EncryptedAtRest     bool                `json:"encryptedAtRest"`
	MessageCount        int64               `json:"messageCount"`
	EncryptedBytes      int64               `json:"encryptedBytes"`
	CreatedAt           string              `json:"createdAt"`
	UpdatedAt           string              `json:"updatedAt"`
	DeletedAt           string              `json:"deletedAt,omitempty"`
	ResourceVersion     int64               `json:"resourceVersion"`
}

type threadAwaitingJSON struct {
	Kind  string `json:"kind"`
	Since string `json:"since"`
}

type projectThreadIntentJSON struct {
	ID              string      `json:"id"`
	ProjectID       string      `json:"projectId"`
	CapsuleID       string      `json:"capsuleId"`
	CapsuleName     string      `json:"capsuleName"`
	ThreadID        string      `json:"threadId"`
	RunID           string      `json:"runId"`
	MessageID       string      `json:"messageId"`
	Harness         string      `json:"harness"`
	State           string      `json:"state"`
	FailureCode     string      `json:"failureCode,omitempty"`
	FailureMessage  string      `json:"failureMessage,omitempty"`
	Thread          *threadJSON `json:"thread,omitempty"`
	CurrentRun      *runJSON    `json:"currentRun,omitempty"`
	CreatedAt       string      `json:"createdAt"`
	UpdatedAt       string      `json:"updatedAt"`
	ResourceVersion int64       `json:"resourceVersion"`
}

type threadPageJSON struct {
	Items      []threadJSON `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

type threadMutationJSON struct {
	Thread     threadJSON `json:"thread"`
	CurrentRun *runJSON   `json:"currentRun,omitempty"`
	MessageID  string     `json:"messageId,omitempty"`
}

type runEventJSON struct {
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

type runEventPageJSON struct {
	Items      []runEventJSON `json:"items"`
	NextCursor int64          `json:"nextCursor"`
	More       bool           `json:"more"`
}

type gitJSON struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type workspaceFileEntryJSON struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}

type workspaceFilePageJSON struct {
	Path      string                   `json:"path,omitempty"`
	Items     []workspaceFileEntryJSON `json:"items"`
	NextAfter string                   `json:"nextAfter,omitempty"`
}

type workspaceFileJSON struct {
	Path       string `json:"path"`
	Content    []byte `json:"content"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}

type deliveryInspectionJSON struct {
	CapsuleResourceVersion int64  `json:"capsuleResourceVersion"`
	HEAD                   string `json:"head"`
	Branch                 string `json:"branch,omitempty"`
	Dirty                  bool   `json:"dirty"`
	OriginURL              string `json:"originUrl,omitempty"`
	DefaultBranch          string `json:"defaultBranch,omitempty"`
	Tree                   string `json:"tree"`
}

type deliveryJSON struct {
	ID                      string `json:"id"`
	CapsuleID               string `json:"capsuleId"`
	ProjectID               string `json:"projectId"`
	State                   string `json:"state"`
	Action                  string `json:"action"`
	Approved                bool   `json:"approved"`
	ApprovedAt              string `json:"approvedAt"`
	ExpectedResourceVersion int64  `json:"expectedResourceVersion"`
	ExpectedHead            string `json:"expectedHead"`
	ExpectedTree            string `json:"expectedTree"`
	RemoteBranch            string `json:"remoteBranch"`
	DestinationRef          string `json:"destinationRef"`
	BaseBranch              string `json:"baseBranch,omitempty"`
	CommitMessage           string `json:"commitMessage,omitempty"`
	PullRequestTitle        string `json:"pullRequestTitle,omitempty"`
	PullRequestBody         string `json:"pullRequestBody,omitempty"`
	ResultCommitSHA         string `json:"resultCommitSha,omitempty"`
	ResultPullRequestURL    string `json:"resultPullRequestUrl,omitempty"`
	ResultPullRequestNumber int64  `json:"resultPullRequestNumber,omitempty"`
	Failure                 string `json:"failure,omitempty"`
	CreatedAt               string `json:"createdAt"`
	UpdatedAt               string `json:"updatedAt"`
	ResourceVersion         int64  `json:"resourceVersion"`
}

type deliveryPageJSON struct {
	Items      []deliveryJSON `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type attachTicketJSON struct {
	Ticket        string `json:"ticket"`
	ExpiresAt     string `json:"expiresAt"`
	WebSocketPath string `json:"webSocketPath"`
}

type previewPortJSON struct {
	Port uint16 `json:"port"`
}

type previewPortPageJSON struct {
	Items []previewPortJSON `json:"items"`
}

type previewTicketJSON struct {
	URL         string `json:"url"`
	ExpiresAt   string `json:"expiresAt"`
	ReusePolicy string `json:"reusePolicy"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

func projectResponse(project domain.Project) projectJSON {
	return projectJSON{
		ID:                  string(project.ID),
		Name:                project.Name,
		RepositoryURL:       project.RepositoryURL,
		Setup:               project.Setup,
		ImageReference:      project.ImageReference,
		HarnessImages:       harnessImagesToJSON(project.HarnessImages),
		GitSecretName:       project.GitSecretName,
		HarnessSecretNames:  project.HarnessSecretNames,
		GitPushSecretName:   project.GitPushSecretName,
		GitHubAPISecretName: project.GitHubAPISecretName,
		CommitAuthorName:    project.CommitAuthorName,
		CommitAuthorEmail:   project.CommitAuthorEmail,
		DefaultBaseBranch:   project.DefaultBaseBranch,
		CreatedAt:           project.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:           project.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ResourceVersion:     int64(project.ResourceVersion),
	}
}

func deliveryResponse(delivery domain.Delivery) deliveryJSON {
	return deliveryJSON{
		ID: string(delivery.ID), CapsuleID: string(delivery.CapsuleID),
		ProjectID: string(delivery.ProjectID), State: string(delivery.State),
		Action: string(delivery.Action), Approved: delivery.Approved,
		ApprovedAt:              delivery.ApprovedAt.UTC().Format(time.RFC3339Nano),
		ExpectedResourceVersion: int64(delivery.ExpectedCapsuleVersion),
		ExpectedHead:            delivery.ExpectedHEAD, ExpectedTree: delivery.ExpectedTree,
		RemoteBranch: delivery.RemoteBranch, DestinationRef: delivery.DestinationRef,
		BaseBranch: delivery.BaseBranch, CommitMessage: delivery.CommitMessage,
		PullRequestTitle: delivery.PullRequestTitle, PullRequestBody: delivery.PullRequestBody,
		ResultCommitSHA:         delivery.ResultCommitSHA,
		ResultPullRequestURL:    delivery.ResultPullRequestURL,
		ResultPullRequestNumber: delivery.ResultPullRequestNumber,
		Failure:                 delivery.Failure, CreatedAt: delivery.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       delivery.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ResourceVersion: int64(delivery.ResourceVersion),
	}
}

func secretResponse(secret domain.Secret) secretJSON {
	return secretJSON{
		ID: string(secret.ID), Name: secret.Name, Purpose: string(secret.Purpose),
		CreatedAt:       secret.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       secret.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ResourceVersion: int64(secret.ResourceVersion),
	}
}

func capsuleResponse(capsule domain.Capsule) capsuleJSON {
	return capsuleJSON{
		Preparation:     capsule.Preparation,
		ID:              string(capsule.ID),
		ProjectID:       string(capsule.ProjectID),
		TimelineID:      string(capsule.TimelineID),
		OriginMomentID:  string(capsule.OriginMomentID),
		Name:            capsule.Name,
		Harness:         capsule.LauncherHarness,
		State:           string(capsule.State),
		DesiredState:    string(capsule.DesiredState),
		Failure:         capsule.Failure,
		RestoreComplete: capsule.RestoreComplete,
		Maintenance:     capsule.Maintenance,
		CreatedAt:       capsule.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       capsule.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ResourceVersion: int64(capsule.ResourceVersion),
	}
}

func momentResponse(moment domain.Moment) momentJSON {
	return momentJSON{
		ID: string(moment.ID), ProjectID: string(moment.ProjectID),
		CapsuleID: string(moment.CapsuleID), TimelineID: string(moment.TimelineID),
		ParentMomentID: string(moment.ParentMomentID), Name: moment.Name,
		ArchiveSHA256: moment.ArchiveSHA256, ArchiveSize: moment.ArchiveSize,
		ManifestSHA256: moment.ManifestSHA256, ImageDigest: moment.ImageDigest,
		ProjectSetupHash: moment.ProjectSetupHash, GitBranch: moment.GitBranch,
		GitHEAD: moment.GitHEAD, GitDirtySummary: moment.GitDirtySummary,
		CreatedAt: moment.CreatedAt.UTC().Format(time.RFC3339Nano), Final: moment.Final,
	}
}

func timelineResponse(timeline domain.Timeline) timelineJSON {
	return timelineJSON{
		ID: string(timeline.ID), ProjectID: string(timeline.ProjectID),
		CapsuleID:          string(timeline.CapsuleID),
		ForkedFromMomentID: string(timeline.ForkedFromMomentID),
		Reason:             string(timeline.Reason), CreatedAt: timeline.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func descendantResponse(result app.DescendantResult) descendantJSON {
	return descendantJSON{
		Capsule: capsuleResponse(result.Capsule), Timeline: timelineResponse(result.Timeline),
		Reason: string(result.Reason),
	}
}

func sealResponse(result app.SealResult) sealJSON {
	return sealJSON{Capsule: capsuleResponse(result.Capsule), Moment: momentResponse(result.Moment)}
}

func runResponse(run domain.Run) runJSON {
	result := runJSON{
		ID: string(run.ID), CapsuleID: string(run.CapsuleID), Harness: run.Harness,
		State: string(run.State), Failure: run.Failure,
		CreatedAt:       run.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       run.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ResourceVersion: int64(run.ResourceVersion),
	}
	if run.HasExitStatus {
		result.ExitStatus = &run.ExitStatus
	}
	if !run.StartedAt.IsZero() {
		result.StartedAt = run.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !run.FinishedAt.IsZero() {
		result.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}

func (s *Server) threadResponse(ctx context.Context, thread domain.Thread) threadJSON {
	result := threadJSON{
		ID: string(thread.ID), CapsuleID: string(thread.CapsuleID), State: string(thread.State),
		Harness: thread.AdapterID, CurrentRunID: string(thread.CurrentRunID),
		StructuredSupported: s.capabilities.Structured,
		EncryptedAtRest:     true, MessageCount: thread.MessageCount,
		EncryptedBytes:  thread.EncryptedBytes,
		CreatedAt:       thread.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       thread.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ResourceVersion: int64(thread.ResourceVersion),
	}
	if s.capabilities.Structured {
		result.Protocol = "meridian.adapter.v1"
	}
	if !thread.DeletedAt.IsZero() {
		result.DeletedAt = thread.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	if thread.CurrentRunID != "" {
		if run, err := s.service.GetRunStored(ctx, thread.CurrentRunID); err == nil {
			result.CurrentRunState = string(run.State)
		}
		if awaiting, err := s.service.ThreadAwaiting(ctx, thread); err == nil && awaiting != nil {
			result.Awaiting = &threadAwaitingJSON{
				Kind:  string(awaiting.Kind),
				Since: awaiting.Since.UTC().Format(time.RFC3339Nano),
			}
		}
	}
	return result
}

func (s *Server) threadMutationResponse(
	ctx context.Context,
	result app.ThreadMutationResult,
) threadMutationJSON {
	response := threadMutationJSON{
		Thread: s.threadResponse(ctx, result.Thread), MessageID: string(result.MessageID),
	}
	if result.Run != nil {
		run := runResponse(*result.Run)
		response.CurrentRun = &run
	}
	return response
}

func (s *Server) projectThreadIntentResponse(
	ctx context.Context,
	result app.ProjectThreadResult,
) projectThreadIntentJSON {
	intent := result.Intent
	response := projectThreadIntentJSON{
		ID: string(intent.ID), ProjectID: string(intent.ProjectID),
		CapsuleID: string(intent.CapsuleID), CapsuleName: intent.CapsuleName,
		ThreadID: string(intent.ThreadID), RunID: string(intent.RunID),
		MessageID: string(intent.MessageID), Harness: intent.Harness,
		State: string(intent.State), FailureCode: intent.FailureCode,
		FailureMessage:  intent.FailureMessage,
		CreatedAt:       intent.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       intent.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ResourceVersion: int64(intent.ResourceVersion),
	}
	if result.Thread != nil {
		thread := s.threadResponse(ctx, *result.Thread)
		response.Thread = &thread
	}
	if result.Run != nil {
		run := runResponse(*result.Run)
		response.CurrentRun = &run
	}
	return response
}

func harnessImagesFromRequest(items []harnessImageJSON) []domain.HarnessImage {
	if len(items) == 0 {
		return nil
	}
	result := make([]domain.HarnessImage, 0, len(items))
	for _, item := range items {
		result = append(result, domain.HarnessImage{
			Name: item.Name, ImageReference: item.ImageReference,
		})
	}
	return result
}

func harnessImagesToJSON(items []domain.HarnessImage) []harnessImageJSON {
	if len(items) == 0 {
		return nil
	}
	result := make([]harnessImageJSON, 0, len(items))
	for _, item := range items {
		result = append(result, harnessImageJSON{
			Name: item.Name, ImageReference: item.ImageReference,
		})
	}
	return result
}
