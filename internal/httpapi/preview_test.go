package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/coder/websocket"
)

func TestPreviewTicketDigestScopeExpiryAndRevocation(t *testing.T) {
	registry := newPreviewTicketRegistry(4)
	now := time.Now().UTC()
	token, err := registry.mint("capsule-one", 3000, now.Add(time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", registry.entries), token) {
		t.Fatal("registry retained preview token plaintext")
	}
	if _, status := registry.authorize(token, "capsule-two", 3000, now); status != "wrong_capsule" {
		t.Fatalf("cross-Capsule status = %q", status)
	}
	if _, status := registry.authorize(token, "capsule-one", 3001, now); status != "wrong_port" {
		t.Fatalf("wrong-port status = %q", status)
	}
	if _, status := registry.authorize("bad", "capsule-one", 3000, now); status != "malformed" {
		t.Fatalf("malformed status = %q", status)
	}
	if _, status := registry.authorize(token, "capsule-one", 3000, now.Add(2*time.Minute)); status != "expired" {
		t.Fatalf("expired status = %q", status)
	}

	token, err = registry.mint("capsule-one", 3000, now.Add(time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	registry.revokeCapsule("capsule-one")
	if _, status := registry.authorize(token, "capsule-one", 3000, now); status != "revoked" {
		t.Fatalf("revoked status = %q", status)
	}

	restarted := newPreviewTicketRegistry(4)
	if _, status := restarted.authorize(token, "capsule-one", 3000, now); status != "unknown" {
		t.Fatalf("daemon-restart status = %q", status)
	}

	expiring := previewTicket{ExpiresAt: time.Now().Add(20 * time.Millisecond), Revoked: make(chan struct{})}
	expiryContext, cancel := previewContext(context.Background(), expiring, 0)
	defer cancel()
	select {
	case <-expiryContext.Done():
	case <-time.After(time.Second):
		t.Fatal("active preview context did not expire")
	}
}

type previewClock struct{}

func (previewClock) Now() time.Time { return time.Now().UTC() }

type previewIDs struct {
	mu   sync.Mutex
	next int
}

func (i *previewIDs) NewID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return fmt.Sprintf("preview-%d", i.next)
}

type previewRuntime struct {
	t        *testing.T
	resource string
}

type previewMessage struct {
	binary bool
	value  []byte
}

type previewEchoAttachment struct {
	messages chan previewMessage
}

func (a *previewEchoAttachment) Read(ctx context.Context) (bool, []byte, error) {
	select {
	case <-ctx.Done():
		return false, nil, ctx.Err()
	case message := <-a.messages:
		return message.binary, message.value, nil
	}
}

func (a *previewEchoAttachment) Write(
	ctx context.Context,
	binary bool,
	value []byte,
) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case a.messages <- previewMessage{binary: binary, value: append([]byte(nil), value...)}:
		return nil
	}
}

func (*previewEchoAttachment) Close() error { return nil }

func (r *previewRuntime) DiscoverPreviewPorts(
	_ context.Context,
	resource string,
) ([]ports.PreviewPort, error) {
	r.resource = resource
	return []ports.PreviewPort{{Port: 3000}}, nil
}

func (r *previewRuntime) ForwardPreviewHTTP(
	_ context.Context,
	resource string,
	port uint16,
	request *http.Request,
) (ports.PreviewResponse, error) {
	if resource != r.resource || port != 3000 || request.URL.RequestURI() != "/asset?q=one" {
		r.t.Fatalf("preview target = %q %d %q", resource, port, request.URL.RequestURI())
	}
	for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer", "X-Meridian-Secret", "X-Hop"} {
		if request.Header.Get(name) != "" {
			r.t.Errorf("forwarded unsafe request header %s", name)
		}
	}
	return ports.PreviewResponse{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":      []string{"text/plain"},
			"Set-Cookie":        []string{"preview=secret"},
			"X-Meridian-Secret": []string{"secret"},
			"Connection":        []string{"X-Hop"},
			"X-Hop":             []string{"hop"},
		},
		Body: []byte("preview-ok"),
	}, nil
}

func (*previewRuntime) AttachPreview(
	_ context.Context,
	_ string,
	_ uint16,
	_ string,
	header http.Header,
) (ports.RuntimeAttachment, string, error) {
	if header.Get("Authorization") != "" || header.Get("Cookie") != "" {
		return nil, "", errors.New("unsafe WebSocket headers")
	}
	return &previewEchoAttachment{messages: make(chan previewMessage, 1)}, "preview.v1", nil
}

func TestPreviewHTTPFlowReuseCrossScopeAndDeletionRevocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ids := &previewIDs{}
	reconciler := app.NewReconciler(store, fake.New(fake.Options{}), previewClock{}, ids)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, previewClock{}, ids, reconciler)
	runtime := &previewRuntime{t: t}
	service.ConfigurePreview(runtime)
	project, err := service.CreateProject(ctx, "project", "project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for capsule.State != domain.CapsuleReady {
		capsule, err = service.GetCapsule(ctx, capsule.ID)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("Capsule readiness = %#v, %v", capsule, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	server := NewWithPreview(service, ports.ProviderCapabilities{
		Version: "preview/v1", Preview: true,
	}, "http://127.0.0.1:9191")
	discovery := httptest.NewRecorder()
	server.ServeHTTP(
		discovery,
		httptest.NewRequest(http.MethodGet, "/capsules/"+string(capsule.ID)+"/previews", nil),
	)
	if discovery.Code != http.StatusOK || !strings.Contains(discovery.Body.String(), "3000") {
		t.Fatalf("discovery = %d %s", discovery.Code, discovery.Body.String())
	}
	issued := httptest.NewRecorder()
	server.ServeHTTP(
		issued,
		httptest.NewRequest(
			http.MethodPost,
			"/capsules/"+string(capsule.ID)+"/previews/3000/tickets",
			nil,
		),
	)
	if issued.Code != http.StatusCreated {
		t.Fatalf("ticket = %d %s", issued.Code, issued.Body.String())
	}
	var ticket previewTicketJSON
	if err := json.Unmarshal(issued.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(ticket.URL)
	if err != nil || parsed.Host != "127.0.0.1:9191" ||
		ticket.ReusePolicy != "reusable_until_expiry" {
		t.Fatalf("ticket = %#v, %v", ticket, err)
	}
	path := strings.TrimSuffix(parsed.Path, "/") + "/asset?q=one"
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "attacker.invalid"
		request.Header.Set("Authorization", "Bearer public-secret")
		request.Header.Set("Cookie", "session=secret")
		request.Header.Set("Proxy-Authorization", "Basic secret")
		request.Header.Set("Referer", ticket.URL+"sensitive")
		request.Header.Set("X-Meridian-Secret", "secret")
		request.Header.Set("Connection", "X-Hop")
		request.Header.Set("X-Hop", "hop")
		response := httptest.NewRecorder()
		server.PreviewHandler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != "preview-ok" {
			t.Fatalf("preview attempt %d = %d %q", attempt, response.Code, response.Body.String())
		}
		if response.Header().Get("Set-Cookie") != "" ||
			response.Header().Get("X-Meridian-Secret") != "" ||
			response.Header().Get("X-Hop") != "" {
			t.Fatalf("unsafe preview response headers = %#v", response.Header())
		}
	}
	previewServer := httptest.NewServer(server.PreviewHandler())
	defer previewServer.Close()
	webSocketURL := "ws" + strings.TrimPrefix(previewServer.URL, "http") +
		strings.TrimSuffix(parsed.Path, "/") + "/socket"
	webSocketContext, stopWebSocket := context.WithTimeout(ctx, 2*time.Second)
	defer stopWebSocket()
	connection, response, err := websocket.Dial(webSocketContext, webSocketURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer browser-secret"},
			"Cookie":        []string{"session=secret"},
		},
		Subprotocols: []string{"preview.v1"},
	})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if connection.Subprotocol() != "preview.v1" {
		t.Fatalf("preview subprotocol = %q", connection.Subprotocol())
	}
	if err := connection.Write(webSocketContext, websocket.MessageBinary, []byte{0, 1, 2}); err != nil {
		t.Fatal(err)
	}
	messageType, value, err := connection.Read(webSocketContext)
	if err != nil || messageType != websocket.MessageBinary || !bytes.Equal(value, []byte{0, 1, 2}) {
		t.Fatalf("preview WebSocket echo = %d %v, %v", messageType, value, err)
	}

	parts := strings.Split(parsed.Path, "/")
	crossCapsule := append([]string(nil), parts...)
	crossCapsule[3] = "another-capsule"
	assertPreviewStatus(t, server, strings.Join(crossCapsule, "/"), http.StatusForbidden)
	wrongPort := append([]string(nil), parts...)
	wrongPort[4] = strconv.Itoa(3001)
	assertPreviewStatus(t, server, strings.Join(wrongPort, "/"), http.StatusForbidden)
	assertPreviewStatus(t, server, "/p/not-a-token/capsule/3000/", http.StatusBadRequest)

	deleteBody := fmt.Sprintf(`{"expectedResourceVersion":%d}`, capsule.ResourceVersion)
	deleteRequest := httptest.NewRequest(
		http.MethodPost, "/capsules/"+string(capsule.ID)+"/delete", bytes.NewBufferString(deleteBody),
	)
	deleteRequest.Header.Set("Content-Type", "application/json")
	deleteRequest.Header.Set("Idempotency-Key", "delete")
	deleteResponse := httptest.NewRecorder()
	server.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusAccepted {
		t.Fatalf("delete = %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	assertPreviewStatus(t, server, parsed.Path, http.StatusGone)
	revokedContext, stopRevoked := context.WithTimeout(context.Background(), time.Second)
	defer stopRevoked()
	if _, _, err := connection.Read(revokedContext); err == nil {
		t.Fatal("revoked preview WebSocket remained open")
	}
}

func assertPreviewStatus(t *testing.T, server *Server, path string, want int) {
	t.Helper()
	response := httptest.NewRecorder()
	server.PreviewHandler().ServeHTTP(
		response, httptest.NewRequest(http.MethodGet, path, nil),
	)
	if response.Code != want {
		t.Fatalf("%s status = %d, want %d: %s", path, response.Code, want, response.Body.String())
	}
}
