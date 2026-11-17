package gitgateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const upstreamToken = "gho_upstreamtokenvalue"

// sourceHost serves a bare repository over smart HTTP with git http-backend
// and accepts only the upstream token, like GitHub does.
type sourceHost struct {
	server *httptest.Server
	bare   string

	mu    sync.Mutex
	auths []string
}

func newSourceHost(t *testing.T) *sourceHost {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "repo.git")
	run(t, root, nil, "init", "--quiet", "--bare", bare)
	run(t, bare, nil, "config", "http.receivepack", "true")
	host := &sourceHost{bare: bare}
	backend := &cgi.Handler{
		Path: git, Args: []string{"http-backend"}, Root: "/",
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1"},
	}
	host.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, _ := r.BasicAuth()
		host.mu.Lock()
		host.auths = append(host.auths, user+":"+password)
		host.mu.Unlock()
		if user != "x-access-token" || password != upstreamToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="source"`)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.ContentLength < 0 {
			// Go's CGI handler refuses chunked bodies; GitHub accepts them.
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "body", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			r.TransferEncoding = nil
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(host.server.Close)
	return host
}

func (h *sourceHost) upstream() string { return h.server.URL + "/repo.git" }

func (h *sourceHost) ref(t *testing.T, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", h.bare, "rev-parse", "--verify", "--quiet", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func run(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"), env...)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// workspace is the Capsule side: a repository with one commit that carries a
// 2 MiB incompressible file, larger than Git's default post buffer.
func workspace(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, nil, "init", "--quiet", "-b", "main")
	blob := make([]byte, 2<<20)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "large.bin"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, dir, nil, "add", "large.bin")
	run(t, dir, []string{"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.test",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.test"}, "commit", "--quiet", "-m", "work")
	return dir, run(t, dir, nil, "rev-parse", "HEAD")
}

// push runs a real git push through the gateway, the way capsuled does.
func push(t *testing.T, dir, gateway, caFile, capability, refspec string) (string, error) {
	t.Helper()
	askpass := filepath.Join(t.TempDir(), "askpass")
	script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' \"$GW_USER\";;\n*) printf '%s\\n' \"$GW_PASS\";;\nesac\n"
	if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-c", "credential.helper=", "-c", "core.askPass="+askpass,
		"-c", "http.followRedirects=false", "-c", "http.postBuffer="+strconv.Itoa(PostBufferBytes),
		"push", "--porcelain", "--no-verify", "--",
		gateway+RepositoryPath, refspec)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_SSL_CAINFO="+caFile, "GW_USER="+Username, "GW_PASS="+capability)
	out, err := command.CombinedOutput()
	return string(out), err
}

func gatewayServer(t *testing.T, host *sourceHost) (*Gateway, string, string) {
	t.Helper()
	gateway := New(host.server.Client())
	server := httptest.NewTLSServer(gateway)
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return gateway, server.URL, caFile
}

func TestApprovedPushReachesTheSourceHostAndNothingElse(t *testing.T) {
	host := newSourceHost(t)
	dir, head := workspace(t)
	gateway, url, caFile := gatewayServer(t, host)
	const ref = "refs/heads/review/topic"
	credential := Credential{Username: "x-access-token", Password: upstreamToken}

	current, err := gateway.RemoteRef(context.Background(), host.upstream(), credential, ref)
	if err != nil || current != "" {
		t.Fatalf("remote ref before push = %q, %v", current, err)
	}
	if _, err := gateway.RemoteRef(context.Background(), host.upstream(), Credential{Username: "x-access-token", Password: "wrong"}, ref); err == nil {
		t.Fatal("a rejected credential read the remote ref")
	}

	capability, revoke, err := gateway.Issue(Grant{
		Upstream: host.upstream(), Credential: credential, SourceCommit: head, DestinationRef: ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()

	// A different branch with the right capability is refused, and the
	// refusal does not use the capability up.
	if out, err := push(t, dir, url, caFile, capability, head+":refs/heads/main"); err == nil {
		t.Fatalf("push to an unapproved branch succeeded:\n%s", out)
	}
	if got := host.ref(t, "refs/heads/main"); got != "" {
		t.Fatalf("unapproved branch reached the source host: %s", got)
	}

	out, err := push(t, dir, url, caFile, capability, head+":"+ref)
	if err != nil {
		t.Fatalf("approved push failed: %v\n%s", err, out)
	}
	if strings.Contains(out, upstreamToken) {
		t.Fatalf("push output contains the upstream token:\n%s", out)
	}
	if got := host.ref(t, ref); got != head {
		t.Fatalf("source host %s = %q, want %s", ref, got, head)
	}
	current, err = gateway.RemoteRef(context.Background(), host.upstream(), credential, ref)
	if err != nil || current != head {
		t.Fatalf("remote ref after push = %q, %v", current, err)
	}

	// The capability works once.
	if out, err := push(t, dir, url, caFile, capability, head+":refs/heads/review/again"); err == nil {
		t.Fatalf("a used capability pushed again:\n%s", out)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	for _, auth := range host.auths {
		if strings.Contains(auth, capability) {
			t.Fatal("the capability was sent to the source host")
		}
	}
}

func TestCapabilityExpiresAndRevokes(t *testing.T) {
	host := newSourceHost(t)
	dir, head := workspace(t)
	gateway, url, caFile := gatewayServer(t, host)
	grant := Grant{
		Upstream: host.upstream(), Credential: Credential{Username: "x-access-token", Password: upstreamToken},
		SourceCommit: head, DestinationRef: "refs/heads/review/late",
	}
	now := time.Now()
	gateway.now = func() time.Time { return now }
	expired, revokeExpired, err := gateway.Issue(grant)
	if err != nil {
		t.Fatal(err)
	}
	defer revokeExpired()
	gateway.now = func() time.Time { return now.Add(CapabilityLifetime + time.Second) }
	if out, err := push(t, dir, url, caFile, expired, head+":"+grant.DestinationRef); err == nil {
		t.Fatalf("an expired capability pushed:\n%s", out)
	}
	gateway.now = time.Now
	revoked, revoke, err := gateway.Issue(grant)
	if err != nil {
		t.Fatal(err)
	}
	revoke()
	if out, err := push(t, dir, url, caFile, revoked, head+":"+grant.DestinationRef); err == nil {
		t.Fatalf("a revoked capability pushed:\n%s", out)
	}
	if got := host.ref(t, grant.DestinationRef); got != "" {
		t.Fatalf("source host has %s", got)
	}
}

func TestCheckCommandAllowsOnlyTheApprovedUpdate(t *testing.T) {
	source := strings.Repeat("a", 40)
	zero := strings.Repeat("0", 40)
	grant := Grant{SourceCommit: source, DestinationRef: "refs/heads/review/topic"}
	ok := command{old: zero, new: source, ref: grant.DestinationRef}
	if err := checkCommand([]command{ok}, []string{"report-status", "side-band-64k"}, grant); err != nil {
		t.Fatalf("approved update refused: %v", err)
	}
	for name, item := range map[string]struct {
		commands     []command
		capabilities []string
		grant        Grant
	}{
		"two refs":          {[]command{ok, {old: zero, new: source, ref: "refs/heads/other"}}, nil, grant},
		"no refs":           {nil, nil, grant},
		"other ref":         {[]command{{old: zero, new: source, ref: "refs/heads/main"}}, nil, grant},
		"other commit":      {[]command{{old: zero, new: strings.Repeat("b", 40), ref: grant.DestinationRef}}, nil, grant},
		"delete":            {[]command{{old: source, new: zero, ref: grant.DestinationRef}}, nil, grant},
		"existing branch":   {[]command{{old: strings.Repeat("c", 40), new: source, ref: grant.DestinationRef}}, nil, grant},
		"push options":      {[]command{ok}, []string{"report-status", "push-options"}, grant},
		"wrong expectation": {[]command{ok}, nil, Grant{SourceCommit: source, DestinationRef: grant.DestinationRef, ExpectedOld: strings.Repeat("d", 40)}},
	} {
		if err := checkCommand(item.commands, item.capabilities, item.grant); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func pkt(payload string) string {
	return fmt.Sprintf("%04x", len(payload)+4) + payload
}

func TestReadCommandSection(t *testing.T) {
	source := strings.Repeat("a", 40)
	zero := strings.Repeat("0", 40)
	body := pkt("shallow "+strings.Repeat("e", 40)+"\n") +
		pkt(zero+" "+source+" refs/heads/review/topic\x00report-status agent=git/2.42\n") + "0000PACKDATA"
	section, err := readCommandSection(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(section.commands) != 1 || section.commands[0].ref != "refs/heads/review/topic" ||
		len(section.shallow) != 1 || len(section.capabilities) != 2 {
		t.Fatalf("section = %+v", section)
	}
	if !bytes.Equal(section.raw, []byte(strings.TrimSuffix(body, "PACKDATA"))) {
		t.Fatalf("raw = %q", section.raw)
	}
	for name, input := range map[string]string{
		"signed":         pkt("push-cert\x00report-status\n") + "0000",
		"bad length":     "zzzz",
		"short":          "0003",
		"bad command":    pkt("not a command\n") + "0000",
		"late caps":      pkt(zero+" "+source+" refs/heads/a\x00report-status\n") + pkt(zero+" "+source+" refs/heads/b\x00x\n") + "0000",
		"no flush":       pkt(zero + " " + source + " refs/heads/a\n"),
		"oversized area": strings.Repeat(pkt(zero+" "+source+" refs/heads/"+strings.Repeat("x", 900)+"\n"), 80) + "0000",
	} {
		if _, err := readCommandSection(strings.NewReader(input)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFindRef(t *testing.T) {
	head := strings.Repeat("a", 40)
	advertisement := pkt("# service=git-receive-pack\n") + "0000" +
		pkt(strings.Repeat("b", 40)+" refs/heads/main\x00report-status delete-refs\n") +
		pkt(head+" refs/heads/review/topic\n") + "0000"
	got, err := findRef(strings.NewReader(advertisement), "refs/heads/review/topic")
	if err != nil || got != head {
		t.Fatalf("findRef = %q, %v", got, err)
	}
	if got, err := findRef(strings.NewReader(advertisement), "refs/heads/missing"); err != nil || got != "" {
		t.Fatalf("missing ref = %q, %v", got, err)
	}
	empty := pkt("# service=git-receive-pack\n") + "0000" +
		pkt(strings.Repeat("0", 40)+" capabilities^{}\x00report-status\n") + "0000"
	if got, err := findRef(strings.NewReader(empty), "refs/heads/main"); err != nil || got != "" {
		t.Fatalf("empty repository = %q, %v", got, err)
	}
	if _, err := findRef(strings.NewReader("<html>"), "refs/heads/main"); err == nil {
		t.Fatal("a non-Git answer was accepted")
	}
}

func TestIssueValidatesTheGrant(t *testing.T) {
	gateway := New(nil)
	base := Grant{
		Upstream: "https://github.com/octocat/hello", Credential: Credential{Username: "x-access-token", Password: "t0ken"},
		SourceCommit: strings.Repeat("a", 40), DestinationRef: "refs/heads/review/topic",
	}
	if _, revoke, err := gateway.Issue(base); err != nil {
		t.Fatal(err)
	} else {
		revoke()
	}
	for name, change := range map[string]func(*Grant){
		"http":        func(g *Grant) { g.Upstream = "http://github.com/octocat/hello" },
		"userinfo":    func(g *Grant) { g.Upstream = "https://user@github.com/octocat/hello" },
		"no password": func(g *Grant) { g.Credential.Password = "" },
		"short sha":   func(g *Grant) { g.SourceCommit = "abc" },
		"tag":         func(g *Grant) { g.DestinationRef = "refs/tags/v1" },
	} {
		grant := base
		change(&grant)
		if _, _, err := gateway.Issue(grant); err == nil {
			t.Errorf("%s: issued", name)
		}
	}
	gateway.Close()
	if _, _, err := gateway.Issue(base); err == nil {
		t.Fatal("a closed gateway issued a capability")
	}
}

// Git checks its credentials with a lone flush before a large chunked push.
// The gateway answers it without using up the capability.
func TestCredentialProbeDoesNotUseTheCapability(t *testing.T) {
	host := newSourceHost(t)
	_, head := workspace(t)
	gateway, url, _ := gatewayServer(t, host)
	capability, revoke, err := gateway.Issue(Grant{
		Upstream: host.upstream(), Credential: Credential{Username: "x-access-token", Password: upstreamToken},
		SourceCommit: head, DestinationRef: "refs/heads/review/topic",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: host.server.Client().Transport.(*http.Transport).TLSClientConfig}}
	post := func(body string) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, url+RepositoryPath+"/git-receive-pack", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", receivePackRequest)
		request.SetBasicAuth(Username, capability)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}
	if status := post("0000"); status != http.StatusOK {
		t.Fatalf("probe status = %d", status)
	}
	if status := post("0000"); status != http.StatusOK {
		t.Fatalf("second probe status = %d: the probe used the capability", status)
	}
	if status := post("0000extra"); status != http.StatusBadRequest {
		t.Fatalf("flush followed by data status = %d", status)
	}
}
