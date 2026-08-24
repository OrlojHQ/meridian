package capsuleproto

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPreviewDiscoveryAndHTTPForwardingAreScopedAndSanitized(t *testing.T) {
	var observed map[string]string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		observed = map[string]string{
			"host": request.Host, "path": request.URL.RequestURI(), "body": string(body),
			"authorization": request.Header.Get("Authorization"),
			"cookie":        request.Header.Get("Cookie"),
			"referer":       request.Header.Get("Referer"),
			"meridian":      request.Header.Get("X-Meridian-Secret"),
			"hop":           request.Header.Get("X-Hop"),
		}
		writer.Header().Set("Set-Cookie", "preview=secret")
		writer.Header().Set("X-Meridian-Secret", "response-secret")
		writer.Header().Set("Keep-Alive", "timeout=30")
		writer.Header().Set("X-Safe", "yes")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("forwarded"))
	}))
	defer upstream.Close()
	port := serverPort(t, upstream.URL)

	server, err := NewServer(ServerConfig{
		Token: testToken, Workspace: t.TempDir(), PreviewBodyLimit: 1024,
		PreviewPortDiscovery: func() ([]uint16, error) { return []uint16{port}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	protocol := httptest.NewServer(server.Handler())
	defer protocol.Close()
	client, err := NewClient(protocol.URL, testToken, protocol.Client())
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := client.PreviewPorts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range discovered.Items {
		found = found || item.Port == port
	}
	if !found {
		t.Fatalf("port %d was not discovered: %#v", port, discovered.Items)
	}

	request, _ := http.NewRequest(
		http.MethodPost, "http://attacker.invalid/hello?q=one", strings.NewReader("payload"),
	)
	request.Header.Set("Authorization", "Bearer browser-secret")
	request.Header.Set("Cookie", "session=secret")
	request.Header.Set("Referer", "http://127.0.0.1/p/secret/")
	request.Header.Set("X-Meridian-Secret", "request-secret")
	request.Header.Set("Connection", "X-Hop")
	request.Header.Set("X-Hop", "request-hop")
	request.Header.Set("X-Safe", "yes")
	status, header, body, err := client.ForwardPreviewHTTP(context.Background(), port, request)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusCreated || string(body) != "forwarded" || header.Get("X-Safe") != "yes" {
		t.Fatalf("response = %d %#v %q", status, header, body)
	}
	if header.Get("Set-Cookie") != "" || header.Get("X-Meridian-Secret") != "" ||
		header.Get("Keep-Alive") != "" {
		t.Fatalf("unsafe response headers = %#v", header)
	}
	if observed["host"] != "127.0.0.1:"+strconv.Itoa(int(port)) ||
		observed["path"] != "/hello?q=one" || observed["body"] != "payload" ||
		observed["authorization"] != "" || observed["cookie"] != "" ||
		observed["referer"] != "" || observed["meridian"] != "" || observed["hop"] != "" {
		t.Fatalf("upstream request = %#v", observed)
	}

	unused := port + 1
	if unused == 7777 {
		unused++
	}
	request, _ = http.NewRequest(http.MethodGet, "http://preview.invalid/", nil)
	status, _, _, err = client.ForwardPreviewHTTP(context.Background(), unused, request)
	if err != nil || status != http.StatusBadRequest {
		t.Fatalf("undiscovered port response = %d, %v", status, err)
	}
}

func TestPreviewForwardingBodyAndTimeoutBounds(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/slow" {
			<-request.Context().Done()
			return
		}
		_, _ = writer.Write(bytes.Repeat([]byte("x"), 65))
	}))
	defer upstream.Close()
	port := serverPort(t, upstream.URL)
	server, err := NewServer(ServerConfig{
		Token: testToken, Workspace: t.TempDir(), PreviewBodyLimit: 64,
		PreviewTimeout:       20 * time.Millisecond,
		PreviewPortDiscovery: func() ([]uint16, error) { return []uint16{port}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	protocol := httptest.NewServer(server.Handler())
	defer protocol.Close()
	client, _ := NewClient(protocol.URL, testToken, protocol.Client())

	request, _ := http.NewRequest(
		http.MethodPost, "http://preview.invalid/", bytes.NewReader(bytes.Repeat([]byte("x"), 65)),
	)
	status, _, _, err := client.ForwardPreviewHTTP(context.Background(), port, request)
	if err != nil || status != http.StatusRequestEntityTooLarge {
		t.Fatalf("request bound response = %d, %v", status, err)
	}
	request, _ = http.NewRequest(http.MethodGet, "http://preview.invalid/", nil)
	status, _, _, err = client.ForwardPreviewHTTP(context.Background(), port, request)
	if err != nil || status != http.StatusBadGateway {
		t.Fatalf("response bound response = %d, %v", status, err)
	}
	request, _ = http.NewRequest(http.MethodGet, "http://preview.invalid/slow", nil)
	started := time.Now()
	status, _, _, err = client.ForwardPreviewHTTP(context.Background(), port, request)
	if err != nil || status != http.StatusBadGateway || time.Since(started) > time.Second {
		t.Fatalf("timeout response = %d after %s, %v", status, time.Since(started), err)
	}
}

func TestPreviewWebSocketForwarding(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
			Subprotocols: []string{"preview.v1"},
		})
		if err != nil {
			return
		}
		defer connection.CloseNow()
		messageType, value, err := connection.Read(request.Context())
		if err == nil {
			_ = connection.Write(request.Context(), messageType, append([]byte("echo:"), value...))
		}
	}))
	defer upstream.Close()
	port := serverPort(t, upstream.URL)
	server, err := NewServer(ServerConfig{
		Token: testToken, Workspace: t.TempDir(),
		PreviewPortDiscovery: func() ([]uint16, error) { return []uint16{port}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	protocol := httptest.NewServer(server.Handler())
	defer protocol.Close()
	client, _ := NewClient(protocol.URL, testToken, protocol.Client())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, err := client.AttachPreview(
		ctx, port, "/socket?scope=one",
		http.Header{"Sec-Websocket-Protocol": []string{"preview.v1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if connection.Subprotocol() != "preview.v1" {
		t.Fatalf("subprotocol = %q", connection.Subprotocol())
	}
	if err := connection.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	messageType, value, err := connection.Read(ctx)
	if err != nil || messageType != websocket.MessageText || string(value) != "echo:hello" {
		t.Fatalf("echo = %d %q, %v", messageType, value, err)
	}
}

func serverPort(t *testing.T, rawURL string) uint16 {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(value)
}
