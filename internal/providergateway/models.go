package providergateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// ListModels reads model ids from an OpenAI-compatible GET /v1/models.
// The key is sent as a bearer token and is never included in an error.
func ListModels(ctx context.Context, upstream Upstream) ([]string, error) {
	base, err := NormalizeBaseURL(upstream.BaseURL, upstream.AllowPrivateHTTP)
	if err != nil {
		return nil, err
	}
	allow := dialable
	if upstream.AllowPrivateHTTP {
		allow = PrivateNetwork
	}
	client := &http.Client{
		Transport:     transport(allow, 20*time.Second),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       20 * time.Second,
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, errors.New("the endpoint did not return a model list")
	}
	if upstream.Key != "" {
		request.Header.Set("Authorization", "Bearer "+upstream.Key)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("the endpoint did not return a model list")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, errors.New("the endpoint rejected the key")
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("the endpoint did not return a model list")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, errors.New("the endpoint did not return a model list")
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("the endpoint did not return a model list")
	}
	ids := make([]string, 0, len(payload.Data))
	seen := map[string]bool{}
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" || seen[id] || len(id) > 256 || len(ids) >= 512 {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("the endpoint did not return a model list")
	}
	return ids, nil
}

// EndpointReasoning reads explicit per-model reasoning support. It never
// fails the caller: a missing or unreadable document means nothing was said.
// The key is not included in any error, and this function returns no error.
func EndpointReasoning(ctx context.Context, upstream Upstream) map[string]string {
	base, err := NormalizeBaseURL(upstream.BaseURL, upstream.AllowPrivateHTTP)
	if err != nil {
		return nil
	}
	allow := dialable
	if upstream.AllowPrivateHTTP {
		allow = PrivateNetwork
	}
	client := &http.Client{
		Transport:     transport(allow, 4*time.Second),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       4 * time.Second,
	}
	merged := map[string]string{}
	merge := func(flags map[string]string) {
		for id, state := range flags {
			if merged[id] == "yes" {
				continue
			}
			merged[id] = state
		}
	}
	if body, ok := getQuiet(ctx, client, upstream.Key, base+"/v1/models"); ok {
		merge(readReasoningFlags(body))
	}
	for _, path := range []string{"/model/info", "/v1/model/info"} {
		body, ok := getQuiet(ctx, client, upstream.Key, base+path)
		if !ok {
			continue
		}
		merge(readReasoningFlags(body))
		break
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func getQuiet(ctx context.Context, client *http.Client, key, target string) ([]byte, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	return body, true
}
