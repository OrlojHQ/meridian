package capsuleproto

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testGatewayCA = "-----BEGIN CERTIFICATE-----\nMIIBfixture\n-----END CERTIFICATE-----\n"

// A gateway push sends the one-time capability to the Station gateway and
// nothing to the source host: no ls-remote, no credential, the gateway CA.
func TestDeliveryPushThroughTheStationGateway(t *testing.T) {
	workspace := deliveryRepository(t)
	head := gitOutput(t, workspace, "rev-parse", "HEAD")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "push")
	script := `#!/bin/sh
for argument do
  case "$argument" in
    ls-remote) echo ls-remote >>"$PUSH_RECORD"; exit 40;;
    push)
      username="$("$GIT_ASKPASS" "Username for HTTPS")" || exit 31
      password="$("$GIT_ASKPASS" "Password for HTTPS")" || exit 32
      {
        printf 'user=%s\npass=%s\n' "$username" "$password"
        printf 'ca=%s\n' "$(cat "$GIT_SSL_CAINFO")"
        printf '%s\n' "$@"
      } >"$PUSH_RECORD"
      exit 0;;
  esac
done
exec "$REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("PUSH_RECORD", record)
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), ".meridian-gateway-ca-*"))
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())

	capability := strings.Repeat("ab", 32)
	gateway := &GitGatewayPush{
		URL: "https://gateway.invalid:8443/git-gateway/push.git", CA: testGatewayCA,
		Username: "meridian", Capability: capability,
	}
	const destination = "refs/heads/review/topic"
	response, err := client.DeliveryPush(context.Background(), DeliveryPushRequest{
		SourceCommit: head, DestinationRef: destination, Gateway: gateway,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Commit != head || response.DestinationRef != destination {
		t.Fatalf("response = %#v", response)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	value := string(raw)
	for _, want := range []string{
		"user=meridian\n", "pass=" + capability + "\n", "ca=" + strings.TrimSuffix(testGatewayCA, "\n"),
		"http.postBuffer=67108864", "http.followRedirects=false", "--no-verify",
		gateway.URL, head + ":" + destination,
	} {
		if !strings.Contains(value, want) {
			t.Fatalf("push record is missing %q:\n%s", want, value)
		}
	}
	if strings.Contains(value, "ls-remote") || strings.Contains(value, "example.invalid/repository.git") ||
		strings.Contains(value, "--force") {
		t.Fatalf("gateway push touched the source host or forced:\n%s", value)
	}
	argumentLines := value[strings.Index(value, "\n-c\n"):]
	if strings.Contains(argumentLines, capability) {
		t.Fatalf("capability appeared in Git arguments:\n%s", value)
	}
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), ".meridian-gateway-ca-*"))
	if len(after) != len(before) {
		t.Fatalf("gateway CA file leaked: before=%v after=%v", before, after)
	}
	// The push named a URL, so git did not move the tracking ref. capsuled
	// does, or inspection would count the pushed commit as unpushed.
	if tracking := gitOutput(t, workspace, "rev-parse", "refs/remotes/origin/review/topic"); tracking != head {
		t.Fatalf("tracking ref after push = %q, want %q", tracking, head)
	}

	old := head
	for name, request := range map[string]DeliveryPushRequest{
		"both credentials": {SourceCommit: head, DestinationRef: destination, Gateway: gateway,
			GitCredential: &GitHTTPSCredential{Username: "u", Password: "p"}},
		"neither":           {SourceCommit: head, DestinationRef: destination},
		"expected old ref":  {SourceCommit: head, DestinationRef: destination, Gateway: gateway, ExpectedOldRef: &old},
		"http gateway":      {SourceCommit: head, DestinationRef: destination, Gateway: withGateway(gateway, func(g *GitGatewayPush) { g.URL = "http://gateway.invalid/git-gateway/push.git" })},
		"other path":        {SourceCommit: head, DestinationRef: destination, Gateway: withGateway(gateway, func(g *GitGatewayPush) { g.URL = "https://github.com/octocat/hello.git" })},
		"userinfo":          {SourceCommit: head, DestinationRef: destination, Gateway: withGateway(gateway, func(g *GitGatewayPush) { g.URL = "https://u:p@gateway.invalid/git-gateway/push.git" })},
		"short capability":  {SourceCommit: head, DestinationRef: destination, Gateway: withGateway(gateway, func(g *GitGatewayPush) { g.Capability = "abc" })},
		"not a certificate": {SourceCommit: head, DestinationRef: destination, Gateway: withGateway(gateway, func(g *GitGatewayPush) { g.CA = "hello" })},
	} {
		_, err := client.DeliveryPush(context.Background(), request)
		var protocolError *Error
		if !errors.As(err, &protocolError) || protocolError.Status != http.StatusBadRequest {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func withGateway(base *GitGatewayPush, change func(*GitGatewayPush)) *GitGatewayPush {
	copied := *base
	change(&copied)
	return &copied
}
