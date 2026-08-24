package capsuleproto

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	maxDiscoveredPreviewPorts = 32
	maxPreviewCandidates      = 64
	maxPreviewHeaders         = 64
	maxPreviewHeaderBytes     = 32 << 10
	maxPreviewBodyBytes       = 8 << 20
	maxPreviewPathBytes       = 4096
	previewRequestTimeout     = 15 * time.Second
)

type PreviewPort struct {
	Port uint16 `json:"port"`
}

type PreviewPortsResponse struct {
	Items []PreviewPort `json:"items"`
}

func (s *Server) previewPorts(writer http.ResponseWriter, _ *http.Request) {
	ports, err := s.config.PreviewPortDiscovery()
	if err != nil {
		writeProtocolError(writer, http.StatusInternalServerError, "preview_discovery_failed")
		return
	}
	items := make([]PreviewPort, len(ports))
	for index, port := range ports {
		items[index] = PreviewPort{Port: port}
	}
	writeJSON(writer, http.StatusOK, PreviewPortsResponse{Items: items})
}

func (s *Server) forwardPreview(writer http.ResponseWriter, request *http.Request) {
	port, pathValue, err := s.previewTarget(request)
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_preview_target")
		return
	}
	if !isWebSocketUpgrade(request) {
		s.forwardPreviewHTTP(writer, request, port, pathValue)
		return
	}
	s.forwardPreviewWebSocket(writer, request, port, pathValue)
}

func (s *Server) forwardPreviewHTTP(
	writer http.ResponseWriter,
	request *http.Request,
	port uint16,
	pathValue string,
) {
	header, err := sanitizePreviewHeader(request.Header, false)
	if err != nil {
		writeProtocolError(writer, http.StatusRequestHeaderFieldsTooLarge, "preview_headers_too_large")
		return
	}
	body, err := readBoundedBody(request.Body, s.config.PreviewBodyLimit)
	if err != nil {
		writeProtocolError(writer, http.StatusRequestEntityTooLarge, "preview_body_too_large")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.config.PreviewTimeout)
	defer cancel()
	target := &url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))),
		Path:     pathValue,
		RawQuery: request.URL.RawQuery,
	}
	upstreamRequest, err := http.NewRequestWithContext(
		ctx, request.Method, target.String(), bytes.NewReader(body),
	)
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_preview_request")
		return
	}
	upstreamRequest.Header = header
	transport := previewTransport(port)
	response, err := transport.RoundTrip(upstreamRequest)
	if err != nil {
		writeProtocolError(writer, http.StatusBadGateway, "preview_upstream_unavailable")
		return
	}
	defer response.Body.Close()
	responseHeader, err := sanitizePreviewHeader(response.Header, true)
	if err != nil {
		writeProtocolError(writer, http.StatusBadGateway, "preview_response_headers_too_large")
		return
	}
	responseBody, err := readBoundedBody(response.Body, s.config.PreviewBodyLimit)
	if err != nil {
		writeProtocolError(writer, http.StatusBadGateway, "preview_response_too_large")
		return
	}
	for name, values := range responseHeader {
		for _, value := range values {
			writer.Header().Add(name, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(responseBody)
}

func (s *Server) forwardPreviewWebSocket(
	writer http.ResponseWriter,
	request *http.Request,
	port uint16,
	pathValue string,
) {
	header, err := sanitizePreviewHeader(request.Header, false)
	if err != nil {
		writeProtocolError(writer, http.StatusRequestHeaderFieldsTooLarge, "preview_headers_too_large")
		return
	}
	endpoint := url.URL{
		Scheme:   "ws",
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))),
		Path:     pathValue,
		RawQuery: request.URL.RawQuery,
	}
	subprotocols := websocketSubprotocols(request.Header)
	upstream, response, err := websocket.Dial(request.Context(), endpoint.String(), &websocket.DialOptions{
		HTTPClient:   &http.Client{Transport: previewTransport(port)},
		HTTPHeader:   header,
		Subprotocols: subprotocols,
	})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		writeProtocolError(writer, http.StatusBadGateway, "preview_websocket_unavailable")
		return
	}
	defer upstream.CloseNow()
	accepted := []string(nil)
	if selected := upstream.Subprotocol(); selected != "" {
		accepted = []string{selected}
	}
	downstream, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
		Subprotocols: accepted,
	})
	if err != nil {
		return
	}
	defer downstream.CloseNow()
	downstream.SetReadLimit(s.config.PreviewBodyLimit)
	upstream.SetReadLimit(s.config.PreviewBodyLimit)
	bridgeWebSockets(request.Context(), downstream, upstream)
}

func bridgeWebSockets(ctx context.Context, left, right *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 2)
	copyMessages := func(destination, source *websocket.Conn) {
		for {
			messageType, value, err := source.Read(ctx)
			if err == nil {
				err = destination.Write(ctx, messageType, value)
			}
			if err != nil {
				results <- err
				return
			}
		}
	}
	go copyMessages(left, right)
	go copyMessages(right, left)
	<-results
}

func (s *Server) previewTarget(request *http.Request) (uint16, string, error) {
	value, err := strconv.ParseUint(request.PathValue("port"), 10, 16)
	if err != nil || value < 1024 || value == 7777 {
		return 0, "", errors.New("invalid preview port")
	}
	port := uint16(value)
	discovered, err := s.config.PreviewPortDiscovery()
	if err != nil {
		return 0, "", err
	}
	index := sort.Search(len(discovered), func(index int) bool { return discovered[index] >= port })
	if index == len(discovered) || discovered[index] != port {
		return 0, "", errors.New("preview port is not listening")
	}
	pathValue := "/" + request.PathValue("path")
	if err := validatePreviewPath(pathValue, request.URL.RawQuery); err != nil {
		return 0, "", err
	}
	return port, pathValue, nil
}

func validatePreviewPath(pathValue, rawQuery string) error {
	if len(pathValue)+len(rawQuery) > maxPreviewPathBytes ||
		strings.ContainsAny(pathValue+rawQuery, "\x00\r\n\\") {
		return errors.New("invalid preview path")
	}
	for _, segment := range strings.Split(pathValue, "/") {
		if segment == "." || segment == ".." {
			return errors.New("invalid preview path segment")
		}
	}
	return nil
}

func discoverListeningPorts() ([]uint16, error) {
	seen := make(map[uint16]struct{})
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(io.LimitReader(file, 256<<10))
		scanner.Buffer(make([]byte, 4096), 64<<10)
		first := true
		for scanner.Scan() {
			if first {
				first = false
				continue
			}
			fields := strings.Fields(scanner.Text())
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			_, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			value, parseErr := strconv.ParseUint(portHex, 16, 16)
			if parseErr != nil || value < 1024 || value == 7777 {
				continue
			}
			seen[uint16(value)] = struct{}{}
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	candidates := make([]uint16, 0, len(seen))
	for port := range seen {
		candidates = append(candidates, port)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	if len(candidates) > maxPreviewCandidates {
		candidates = candidates[:maxPreviewCandidates]
	}
	result := make([]uint16, 0, min(len(candidates), maxDiscoveredPreviewPorts))
	for _, port := range candidates {
		connection, err := net.DialTimeout(
			"tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 25*time.Millisecond,
		)
		if err != nil {
			continue
		}
		_ = connection.Close()
		result = append(result, port)
		if len(result) == maxDiscoveredPreviewPorts {
			break
		}
	}
	return result, nil
}

func previewTransport(port uint16) *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: maxPreviewHeaderBytes,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(
				ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))),
			)
		},
	}
}

func sanitizePreviewHeader(input http.Header, response bool) (http.Header, error) {
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
		if previewHeaderBlocked(canonical, response) {
			continue
		}
		if _, blocked := connectionTokens[canonical]; blocked {
			continue
		}
		for _, value := range values {
			count++
			size += len(canonical) + len(value)
			if count > maxPreviewHeaders || size > maxPreviewHeaderBytes ||
				strings.ContainsAny(canonical+value, "\x00\r\n") {
				return nil, errors.New("preview headers exceed bounds")
			}
			output.Add(canonical, value)
		}
	}
	return output, nil
}

func previewHeaderBlocked(name string, response bool) bool {
	if strings.HasPrefix(strings.ToLower(name), "x-meridian-") {
		return true
	}
	switch name {
	case "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie",
		"Referer",
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Trailer",
		"Transfer-Encoding", "Upgrade", "Te", "Sec-Websocket-Key",
		"Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Accept",
		"Sec-Websocket-Protocol",
		VersionHeader:
		return true
	case "Location":
		return response
	default:
		return false
	}
}

func readBoundedBody(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	value, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > limit {
		return nil, errors.New("body exceeds preview limit")
	}
	return value, nil
}

func isWebSocketUpgrade(request *http.Request) bool {
	return strings.EqualFold(request.Header.Get("Upgrade"), "websocket") &&
		headerContainsToken(request.Header, "Connection", "upgrade")
}

func websocketSubprotocols(header http.Header) []string {
	var result []string
	for _, value := range header.Values("Sec-WebSocket-Protocol") {
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" && len(item) <= 128 {
				result = append(result, item)
			}
		}
	}
	return result
}

func headerContainsToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, item := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(item), token) {
				return true
			}
		}
	}
	return false
}
