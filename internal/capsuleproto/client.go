package capsuleproto

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/coder/websocket"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type Error struct {
	Status int
	Code   string
}

type CaptureResponse struct {
	Archive io.ReadCloser
	Branch  string
	HEAD    string
	Dirty   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("capsuled request failed with status %d", e.Status)
}

func NewClient(baseURL, token string, client *http.Client) (*Client, error) {
	if !strings.HasPrefix(baseURL, "http://127.0.0.1:") {
		return nil, fmt.Errorf("capsuled endpoint must use loopback HTTP")
	}
	if len(token) < 32 {
		return nil, fmt.Errorf("capsuled token is invalid")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    client,
	}, nil
}

func (c *Client) Health(ctx context.Context) (ProbeResponse, error) {
	var response ProbeResponse
	if err := c.call(ctx, http.MethodGet, HealthPath, nil, &response, false); err != nil {
		return ProbeResponse{}, err
	}
	if response.Version != Version || response.Status != "ok" {
		return ProbeResponse{}, fmt.Errorf("capsuled returned incompatible health response")
	}
	return response, nil
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var response StatusResponse
	if err := c.call(ctx, http.MethodGet, StatusPath, nil, &response, true); err != nil {
		return StatusResponse{}, err
	}
	if response.Version != Version {
		return StatusResponse{}, fmt.Errorf("capsuled protocol version mismatch")
	}
	return response, nil
}

func (c *Client) Prepare(ctx context.Context, request PrepareRequest) (PrepareResponse, error) {
	var response PrepareResponse
	if err := c.call(ctx, http.MethodPost, PreparePath, request, &response, true); err != nil {
		return PrepareResponse{}, err
	}
	if response.Status != StateReady {
		return PrepareResponse{}, fmt.Errorf("capsuled preparation is %s", response.Status)
	}
	return response, nil
}

func (c *Client) StartRun(ctx context.Context, request RunStartRequest) (RunStatusResponse, error) {
	var response RunStatusResponse
	err := c.call(ctx, http.MethodPost, RunStartPath, request, &response, true)
	return response, err
}

func (c *Client) RunStatus(ctx context.Context, runID string) (RunStatusResponse, error) {
	var response RunStatusResponse
	err := c.call(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(runID), nil, &response, true)
	return response, err
}

func (c *Client) CancelRun(ctx context.Context, runID string) (RunStatusResponse, error) {
	var response RunStatusResponse
	err := c.call(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(runID)+"/cancel", struct{}{}, &response, true)
	return response, err
}

func (c *Client) RunEvents(ctx context.Context, runID string, after uint64) (RunEventsResponse, error) {
	var response RunEventsResponse
	path := "/v1/runs/" + url.PathEscape(runID) + "/events?after=" + strconv.FormatUint(after, 10)
	err := c.call(ctx, http.MethodGet, path, nil, &response, true)
	return response, err
}

func (c *Client) StartStructured(
	ctx context.Context,
	request StructuredStartRequest,
) (StructuredStatusResponse, error) {
	var response StructuredStatusResponse
	err := c.callLimit(
		ctx, http.MethodPost, "/v1/structured/sessions", request, &response, true,
		adapterproto.MaxFrameBytes+defaultBodyLimit,
	)
	if err == nil {
		err = validateStructuredStatus(response)
	}
	return response, err
}

func (c *Client) StructuredStatus(
	ctx context.Context,
	runID string,
) (StructuredStatusResponse, error) {
	var response StructuredStatusResponse
	err := c.call(ctx, http.MethodGet,
		"/v1/structured/sessions/"+url.PathEscape(runID), nil, &response, true)
	if err == nil {
		err = validateStructuredStatus(response)
	}
	return response, err
}

func (c *Client) SendStructured(
	ctx context.Context,
	runID string,
	frame adapterproto.Frame,
) error {
	return c.callLimit(
		ctx, http.MethodPost,
		"/v1/structured/sessions/"+url.PathEscape(runID)+"/frames",
		StructuredSendRequest{Frame: frame}, &map[string]string{}, true,
		defaultBodyLimit,
	)
}

func (c *Client) StructuredEvents(
	ctx context.Context,
	runID string,
	after uint64,
	wait time.Duration,
) (StructuredEventsResponse, error) {
	if wait < 0 || wait > structuredMaxWait {
		return StructuredEventsResponse{}, errors.New("structured event wait is invalid")
	}
	path := "/v1/structured/sessions/" + url.PathEscape(runID) + "/events?after=" +
		strconv.FormatUint(after, 10) + "&waitMillis=" +
		strconv.FormatInt(wait.Milliseconds(), 10)
	var response StructuredEventsResponse
	err := c.callLimit(
		ctx, http.MethodGet, path, nil, &response, true,
		int64(adapterproto.MaxFrameBytes)*int64(defaultRunLimit),
	)
	if err == nil {
		err = validateStructuredEvents(response, after)
	}
	return response, err
}

func (c *Client) CancelStructured(
	ctx context.Context,
	runID string,
) (StructuredStatusResponse, error) {
	var response StructuredStatusResponse
	err := c.call(ctx, http.MethodPost,
		"/v1/structured/sessions/"+url.PathEscape(runID)+"/cancel",
		struct{}{}, &response, true)
	if err == nil {
		err = validateStructuredStatus(response)
	}
	return response, err
}

func (c *Client) HarnessProfiles(ctx context.Context) (HarnessProfilesResponse, error) {
	var response HarnessProfilesResponse
	err := c.call(ctx, http.MethodGet, "/v1/harness-profiles", nil, &response, true)
	return response, err
}

func (c *Client) GitStatus(ctx context.Context) (GitResponse, error) {
	var response GitResponse
	err := c.call(ctx, http.MethodGet, GitStatusPath, nil, &response, true)
	return response, err
}

func (c *Client) GitDiff(ctx context.Context) (GitResponse, error) {
	var response GitResponse
	err := c.call(ctx, http.MethodGet, GitDiffPath, nil, &response, true)
	return response, err
}

func (c *Client) CaptureWorkspace(ctx context.Context) (CaptureResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+CapturePath, nil)
	if err != nil {
		return CaptureResponse{}, err
	}
	c.authenticateRequest(request)
	response, err := c.http.Do(request)
	if err != nil {
		return CaptureResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		return CaptureResponse{}, &Error{Status: response.StatusCode}
	}
	decode := func(name string) (string, error) {
		value, err := base64.RawURLEncoding.DecodeString(response.Header.Get(name))
		return string(value), err
	}
	branch, branchErr := decode("Meridian-Git-Branch")
	head, headErr := decode("Meridian-Git-Head")
	dirty, dirtyErr := decode("Meridian-Git-Dirty")
	if err := errors.Join(branchErr, headErr, dirtyErr); err != nil {
		_ = response.Body.Close()
		return CaptureResponse{}, fmt.Errorf("decode capture metadata: %w", err)
	}
	return CaptureResponse{Archive: response.Body, Branch: branch, HEAD: head, Dirty: dirty}, nil
}

func (c *Client) RestoreWorkspace(ctx context.Context, digest string, archive io.Reader) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+RestorePath, archive)
	if err != nil {
		return err
	}
	c.authenticateRequest(request)
	request.Header.Set("Content-Type", "application/x-tar")
	request.Header.Set("Meridian-Archive-SHA256", digest)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&envelope)
		return &Error{Status: response.StatusCode, Code: envelope.Error.Code}
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, defaultBodyLimit))
	return err
}

func (c *Client) BrowseList(
	ctx context.Context,
	request BrowseListRequest,
) (BrowseListResponse, error) {
	var response BrowseListResponse
	err := c.callLimit(
		ctx, http.MethodPost, BrowseListPath, request, &response, true,
		int64(maxBrowseEntries)*(maxBrowseNameBytes*6+128),
	)
	return response, err
}

func (c *Client) BrowseRead(
	ctx context.Context,
	request BrowseReadRequest,
) (BrowseReadResponse, error) {
	var response BrowseReadResponse
	err := c.callLimit(
		ctx, http.MethodPost, BrowseReadPath, request, &response, true,
		2*maxBrowseFileBytes,
	)
	return response, err
}

func (c *Client) DeliveryState(ctx context.Context) (DeliveryStateResponse, error) {
	var response DeliveryStateResponse
	err := c.call(ctx, http.MethodGet, DeliveryPath+"/state", nil, &response, true)
	return response, err
}

func (c *Client) DeliveryCommit(
	ctx context.Context,
	request DeliveryCommitRequest,
) (DeliveryCommitResponse, error) {
	var response DeliveryCommitResponse
	err := c.call(
		ctx, http.MethodPost, DeliveryPath+"/commit", request, &response, true,
	)
	return response, err
}

func (c *Client) DeliveryPush(
	ctx context.Context,
	request DeliveryPushRequest,
) (DeliveryPushResponse, error) {
	var response DeliveryPushResponse
	err := c.call(
		ctx, http.MethodPost, DeliveryPath+"/push", request, &response, true,
	)
	return response, err
}

func (c *Client) Attach(ctx context.Context, runID string, after uint64) (*websocket.Conn, error) {
	endpoint := strings.Replace(c.baseURL, "http://", "ws://", 1) +
		"/v1/runs/" + url.PathEscape(runID) + "/attach?after=" + strconv.FormatUint(after, 10)
	headers := http.Header{}
	headers.Set(VersionHeader, Version)
	headers.Set("Authorization", "Bearer "+c.token)
	connection, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient: c.http,
		HTTPHeader: headers,
	})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return connection, err
}

func (c *Client) PreviewPorts(ctx context.Context) (PreviewPortsResponse, error) {
	var response PreviewPortsResponse
	err := c.call(ctx, http.MethodGet, PreviewsPath, nil, &response, true)
	return response, err
}

func (c *Client) ForwardPreviewHTTP(
	ctx context.Context,
	port uint16,
	input *http.Request,
) (int, http.Header, []byte, error) {
	if input == nil || input.URL == nil {
		return 0, nil, nil, errors.New("preview request is required")
	}
	path := PreviewsPath + "/" + strconv.Itoa(int(port)) + input.URL.EscapedPath()
	if input.URL.RawQuery != "" {
		path += "?" + input.URL.RawQuery
	}
	request, err := http.NewRequestWithContext(ctx, input.Method, c.baseURL+path, input.Body)
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header = input.Header.Clone()
	c.authenticateRequest(request)
	transport := c.http.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	response, err := (&http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}).Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	body, err := readBoundedBody(response.Body, maxPreviewBodyBytes)
	if err != nil {
		return 0, nil, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 600 {
		return 0, nil, nil, &Error{Status: response.StatusCode}
	}
	return response.StatusCode, response.Header.Clone(), body, nil
}

func (c *Client) AttachPreview(
	ctx context.Context,
	port uint16,
	path string,
	header http.Header,
) (*websocket.Conn, error) {
	endpoint := strings.Replace(c.baseURL, "http://", "ws://", 1) +
		PreviewsPath + "/" + strconv.Itoa(int(port)) + path
	headers := header.Clone()
	subprotocols := websocketSubprotocols(header)
	headers.Del("Sec-WebSocket-Protocol")
	headers.Set(VersionHeader, Version)
	headers.Set("Authorization", "Bearer "+c.token)
	connection, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPClient:   c.http,
		HTTPHeader:   headers,
		Subprotocols: subprotocols,
	})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return connection, err
}

func (c *Client) call(
	ctx context.Context,
	method, path string,
	input, output any,
	authenticated bool,
) error {
	return c.callLimit(ctx, method, path, input, output, authenticated, defaultBodyLimit)
}

func (c *Client) callLimit(
	ctx context.Context,
	method, path string,
	input, output any,
	authenticated bool,
	responseLimit int64,
) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		c.authenticateRequest(request)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&envelope)
		return &Error{Status: response.StatusCode, Code: envelope.Error.Code}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, responseLimit))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode capsuled response: %w", err)
	}
	return nil
}

func (c *Client) authenticateRequest(request *http.Request) {
	request.Header.Set(VersionHeader, Version)
	request.Header.Set("Authorization", "Bearer "+c.token)
}
