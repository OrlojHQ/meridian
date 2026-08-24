package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/coder/websocket"
)

const (
	previewTicketTTL        = 2 * time.Minute
	previewTicketRetention  = time.Minute
	maxPreviewTickets       = 256
	maxPreviewRequestBody   = 8 << 20
	maxPreviewResponseBody  = 8 << 20
	maxPreviewHeaderCount   = 64
	maxPreviewHeaderBytes   = 32 << 10
	previewRequestTimeout   = 15 * time.Second
	previewTokenEncodedSize = 43
)

type previewTicket struct {
	CapsuleID domain.CapsuleID
	Port      uint16
	ExpiresAt time.Time
	Revoked   chan struct{}
	IsRevoked bool
}

type previewTicketRegistry struct {
	mu      sync.Mutex
	max     int
	entries map[[sha256.Size]byte]*previewTicket
}

func newPreviewTicketRegistry(max int) *previewTicketRegistry {
	if max <= 0 {
		max = maxPreviewTickets
	}
	return &previewTicketRegistry{
		max:     max,
		entries: make(map[[sha256.Size]byte]*previewTicket),
	}
}

func (r *previewTicketRegistry) mint(
	capsuleID domain.CapsuleID,
	port uint16,
	expiresAt, now time.Time,
) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(random)
	digest := sha256.Sum256([]byte(value))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	if len(r.entries) >= r.max {
		return "", domain.ErrResourceExhausted
	}
	r.entries[digest] = &previewTicket{
		CapsuleID: capsuleID, Port: port, ExpiresAt: expiresAt, Revoked: make(chan struct{}),
	}
	return value, nil
}

func (r *previewTicketRegistry) authorize(
	value string,
	capsuleID domain.CapsuleID,
	port uint16,
	now time.Time,
) (previewTicket, string) {
	if !validPreviewToken(value) {
		return previewTicket{}, "malformed"
	}
	digest := sha256.Sum256([]byte(value))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	entry, ok := r.entries[digest]
	if !ok {
		return previewTicket{}, "unknown"
	}
	if entry.CapsuleID != capsuleID {
		return previewTicket{}, "wrong_capsule"
	}
	if entry.Port != port {
		return previewTicket{}, "wrong_port"
	}
	if entry.IsRevoked {
		return previewTicket{}, "revoked"
	}
	if !entry.ExpiresAt.After(now) {
		return previewTicket{}, "expired"
	}
	return *entry, ""
}

func (r *previewTicketRegistry) revokeCapsule(capsuleID domain.CapsuleID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ticket := range r.entries {
		if ticket.CapsuleID == capsuleID && !ticket.IsRevoked {
			ticket.IsRevoked = true
			close(ticket.Revoked)
		}
	}
}

func (r *previewTicketRegistry) pruneLocked(now time.Time) {
	for digest, ticket := range r.entries {
		if now.After(ticket.ExpiresAt.Add(previewTicketRetention)) {
			if !ticket.IsRevoked {
				close(ticket.Revoked)
			}
			delete(r.entries, digest)
		}
	}
}

func validPreviewToken(value string) bool {
	if len(value) != previewTokenEncodedSize {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func (s *Server) PreviewHandler() http.Handler {
	return http.HandlerFunc(s.servePreview)
}

func (s *Server) servePreview(writer http.ResponseWriter, request *http.Request) {
	setPreviewSecurityHeaders(writer.Header())
	token, capsuleID, port, pathValue, ok := parsePreviewPath(request.URL.Path)
	if !ok {
		http.Error(writer, "invalid preview path", http.StatusBadRequest)
		return
	}
	ticket, status := s.previews.authorize(token, capsuleID, port, time.Now().UTC())
	if status != "" {
		writePreviewAuthorizationError(writer, status)
		return
	}
	if s.metrics != nil {
		s.metrics.Preview(1)
		defer s.metrics.Preview(-1)
	}
	if !allowedOrigin(request.Header.Get("Origin")) {
		http.Error(writer, "preview origin is not allowed", http.StatusForbidden)
		return
	}
	if request.Method == http.MethodConnect || request.Method == http.MethodTrace {
		http.Error(writer, "preview method is not allowed", http.StatusMethodNotAllowed)
		return
	}
	pathWithQuery := pathValue
	if request.URL.RawQuery != "" {
		pathWithQuery += "?" + request.URL.RawQuery
	}
	if isWebSocketUpgrade(request) {
		s.proxyPreviewWebSocket(writer, request, ticket, pathWithQuery)
		return
	}
	s.proxyPreviewHTTP(writer, request, ticket, pathValue)
}

func (s *Server) proxyPreviewHTTP(
	writer http.ResponseWriter,
	request *http.Request,
	ticket previewTicket,
	pathValue string,
) {
	if request.ContentLength > maxPreviewRequestBody {
		http.Error(writer, "preview request body is too large", http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxPreviewRequestBody+1))
	if err != nil || len(body) > maxPreviewRequestBody {
		http.Error(writer, "preview request body is too large", http.StatusRequestEntityTooLarge)
		return
	}
	header, err := sanitizePublicPreviewHeader(request.Header, false)
	if err != nil {
		http.Error(writer, "preview request headers are too large", http.StatusRequestHeaderFieldsTooLarge)
		return
	}
	ctx, cancel := previewContext(request.Context(), ticket, previewRequestTimeout)
	defer cancel()
	target := &url.URL{Path: pathValue, RawQuery: request.URL.RawQuery}
	upstreamRequest, err := http.NewRequestWithContext(
		ctx, request.Method, target.String(), bytes.NewReader(body),
	)
	if err != nil {
		http.Error(writer, "invalid preview request", http.StatusBadRequest)
		return
	}
	upstreamRequest.Header = header
	response, err := s.service.ForwardPreviewHTTP(
		ctx, ticket.CapsuleID, ticket.Port, upstreamRequest,
	)
	if err != nil {
		writePreviewProxyError(writer, err)
		return
	}
	if len(response.Body) > maxPreviewResponseBody {
		http.Error(writer, "preview response is too large", http.StatusBadGateway)
		return
	}
	responseHeader, err := sanitizePublicPreviewHeader(response.Header, true)
	if err != nil {
		http.Error(writer, "preview response headers are too large", http.StatusBadGateway)
		return
	}
	setPreviewSecurityHeaders(responseHeader)
	for name, values := range responseHeader {
		for _, value := range values {
			writer.Header().Add(name, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(response.Body)
}

func (s *Server) proxyPreviewWebSocket(
	writer http.ResponseWriter,
	request *http.Request,
	ticket previewTicket,
	pathValue string,
) {
	header, err := sanitizePublicPreviewHeader(request.Header, false)
	if err != nil {
		http.Error(writer, "preview request headers are too large", http.StatusRequestHeaderFieldsTooLarge)
		return
	}
	if protocols, ok := boundedWebSocketSubprotocols(request.Header); !ok {
		http.Error(writer, "preview WebSocket protocols are invalid", http.StatusBadRequest)
		return
	} else if len(protocols) > 0 {
		header["Sec-Websocket-Protocol"] = protocols
	}
	ctx, cancel := previewContext(request.Context(), ticket, 0)
	defer cancel()
	upstream, subprotocol, err := s.service.AttachPreview(
		ctx, ticket.CapsuleID, ticket.Port, pathValue, header,
	)
	if err != nil {
		writePreviewProxyError(writer, err)
		return
	}
	defer upstream.Close()
	accepted := []string(nil)
	if subprotocol != "" {
		accepted = []string{subprotocol}
	}
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
		Subprotocols: accepted,
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(maxPreviewRequestBody)
	errorsChannel := make(chan error, 2)
	go func() {
		for {
			binary, value, err := upstream.Read(ctx)
			if err == nil {
				messageType := websocket.MessageText
				if binary {
					messageType = websocket.MessageBinary
				}
				err = connection.Write(ctx, messageType, value)
			}
			if err != nil {
				errorsChannel <- err
				return
			}
		}
	}()
	go func() {
		for {
			messageType, value, err := connection.Read(ctx)
			if err == nil {
				err = upstream.Write(ctx, messageType == websocket.MessageBinary, value)
			}
			if err != nil {
				errorsChannel <- err
				return
			}
		}
	}()
	<-errorsChannel
}

func parsePreviewPath(value string) (string, domain.CapsuleID, uint16, string, bool) {
	if !strings.HasPrefix(value, "/p/") || len(value) > 8192 ||
		strings.ContainsAny(value, "\x00\r\n\\") {
		return "", "", 0, "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "/p/"), "/", 4)
	if len(parts) < 3 || !validPreviewToken(parts[0]) ||
		parts[1] == "" || len(parts[1]) > 128 {
		return "", "", 0, "", false
	}
	portValue, err := strconv.ParseUint(parts[2], 10, 16)
	if err != nil || portValue < 1024 || portValue == 7777 {
		return "", "", 0, "", false
	}
	pathValue := "/"
	if len(parts) == 4 {
		pathValue += parts[3]
	}
	for _, segment := range strings.Split(pathValue, "/") {
		if segment == "." || segment == ".." {
			return "", "", 0, "", false
		}
	}
	return parts[0], domain.CapsuleID(parts[1]), uint16(portValue), pathValue, true
}

func previewContext(
	parent context.Context,
	ticket previewTicket,
	maxDuration time.Duration,
) (context.Context, context.CancelFunc) {
	deadline := ticket.ExpiresAt
	if maxDuration > 0 {
		requestDeadline := time.Now().Add(maxDuration)
		if requestDeadline.Before(deadline) {
			deadline = requestDeadline
		}
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	go func() {
		select {
		case <-ticket.Revoked:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func sanitizePublicPreviewHeader(input http.Header, response bool) (http.Header, error) {
	connectionTokens := make(map[string]struct{})
	for _, value := range input.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			connectionTokens[textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(token))] = struct{}{}
		}
	}
	output := make(http.Header)
	count, size := 0, 0
	for name, values := range input {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if blockedPublicPreviewHeader(canonical, response) {
			continue
		}
		if _, blocked := connectionTokens[canonical]; blocked {
			continue
		}
		for _, value := range values {
			count++
			size += len(canonical) + len(value)
			if count > maxPreviewHeaderCount || size > maxPreviewHeaderBytes ||
				strings.ContainsAny(canonical+value, "\x00\r\n") {
				return nil, errors.New("preview headers exceed bounds")
			}
			output.Add(canonical, value)
		}
	}
	return output, nil
}

func blockedPublicPreviewHeader(name string, response bool) bool {
	if strings.HasPrefix(strings.ToLower(name), "x-meridian-") {
		return true
	}
	switch name {
	case "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie",
		"Referer",
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Trailer",
		"Transfer-Encoding", "Upgrade", "Te", "Sec-Websocket-Key",
		"Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Accept",
		"Sec-Websocket-Protocol":
		return true
	case "Location":
		return response
	default:
		return false
	}
}

func setPreviewSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "SAMEORIGIN")
}

func writePreviewAuthorizationError(writer http.ResponseWriter, status string) {
	switch status {
	case "malformed":
		http.Error(writer, "malformed preview token or path", http.StatusBadRequest)
	case "expired", "revoked":
		http.Error(writer, "preview link expired or revoked", http.StatusGone)
	case "wrong_capsule", "wrong_port":
		http.Error(writer, "preview link is out of scope", http.StatusForbidden)
	default:
		http.Error(writer, "preview link not found", http.StatusNotFound)
	}
}

func writePreviewProxyError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		http.Error(writer, "preview request timed out, expired, or was revoked", http.StatusGatewayTimeout)
	case errors.Is(err, domain.ErrNotFound):
		http.Error(writer, "preview port is no longer available", http.StatusNotFound)
	case errors.Is(err, domain.ErrIllegalTransition):
		http.Error(writer, "preview Capsule is not available", http.StatusGone)
	case errors.Is(err, domain.ErrUnsupported):
		http.Error(writer, "previews are unsupported", http.StatusUnprocessableEntity)
	default:
		http.Error(writer, "preview upstream failed", http.StatusBadGateway)
	}
}

func isWebSocketUpgrade(request *http.Request) bool {
	return strings.EqualFold(request.Header.Get("Upgrade"), "websocket") &&
		headerHasToken(request.Header, "Connection", "upgrade")
}

func headerHasToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, item := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(item), token) {
				return true
			}
		}
	}
	return false
}

func boundedWebSocketSubprotocols(header http.Header) ([]string, bool) {
	var result []string
	total := 0
	for _, value := range header.Values("Sec-WebSocket-Protocol") {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			total += len(item)
			if item == "" || len(item) > 128 || total > 1024 ||
				strings.ContainsAny(item, "\x00\r\n") {
				return nil, false
			}
			result = append(result, item)
		}
	}
	return result, true
}

func previewURL(baseURL, token string, capsuleID domain.CapsuleID, port uint16) string {
	return strings.TrimRight(baseURL, "/") + "/p/" + token + "/" +
		url.PathEscape(string(capsuleID)) + "/" + strconv.Itoa(int(port)) + "/"
}
