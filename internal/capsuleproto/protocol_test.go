package capsuleproto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef"

func TestProtocolAuthenticationVersionAndBodyLimit(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	server, err := NewServer(ServerConfig{
		Token: testToken, Workspace: workspace, BodyLimit: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	if status := authenticatedStatus(t, httpServer.URL+ReadinessPath); status != http.StatusServiceUnavailable {
		t.Fatalf("initial readiness status = %d", status)
	}

	response, err := http.Get(httpServer.URL + HealthPath)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("health = %v, %v", response, err)
	}
	_ = response.Body.Close()

	for name, token := range map[string]struct {
		token   string
		version string
		want    int
	}{
		"missing": {}, "bad": {token: "wrong", version: Version, want: http.StatusUnauthorized},
		"version": {token: testToken, version: "v0", want: http.StatusUpgradeRequired},
		"valid":   {token: testToken, version: Version, want: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodGet, httpServer.URL+StatusPath, nil)
			if token.token != "" {
				request.Header.Set("Authorization", "Bearer "+token.token)
			}
			if token.version != "" {
				request.Header.Set(VersionHeader, token.version)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			want := token.want
			if want == 0 {
				want = http.StatusUpgradeRequired
			}
			if response.StatusCode != want {
				t.Fatalf("status = %d, want %d", response.StatusCode, want)
			}
		})
	}

	request, _ := http.NewRequest(
		http.MethodPost, httpServer.URL+PreparePath, bytes.NewReader(bytes.Repeat([]byte("x"), 65)),
	)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set(VersionHeader, Version)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d", response.StatusCode)
	}
}

func TestPreparationIsIdempotentAndUsesArgumentArray(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	var calls atomic.Int32
	var gotName string
	var gotArguments []string
	runner := func(_ context.Context, name string, arguments []string, directory string, _ int64) error {
		calls.Add(1)
		gotName = name
		gotArguments = append([]string(nil), arguments...)
		if directory != workspace {
			t.Fatalf("directory = %q", directory)
		}
		return os.WriteFile(filepath.Join(workspace, "setup-result"), []byte("ok"), 0o600)
	}
	server, err := NewServer(ServerConfig{
		Token: testToken, Workspace: workspace, CommandRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client, err := NewClient(httpServer.URL, testToken, httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	input := PrepareRequest{
		Destination: workspace,
		Setup:       []string{"fixture-setup", "argument with spaces", "$(not-a-shell)"},
	}
	if _, err := client.Prepare(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if status := authenticatedStatus(t, httpServer.URL+ReadinessPath); status != http.StatusOK {
		t.Fatalf("readiness status = %d", status)
	}
	if _, err := client.Prepare(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("runner calls = %d", calls.Load())
	}
	if gotName != "fixture-setup" || len(gotArguments) != 2 ||
		gotArguments[1] != "$(not-a-shell)" {
		t.Fatalf("command = %q %#v", gotName, gotArguments)
	}

	restarted, err := NewServer(ServerConfig{
		Token: testToken, Workspace: workspace,
		CommandRunner: func(context.Context, string, []string, string, int64) error {
			t.Fatal("idempotent restart executed setup")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	restartHTTP := httptest.NewServer(restarted.Handler())
	defer restartHTTP.Close()
	restartClient, _ := NewClient(restartHTTP.URL, testToken, restartHTTP.Client())
	if _, err := restartClient.Prepare(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

func authenticatedStatus(t *testing.T, url string) int {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, url, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set(VersionHeader, Version)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func TestPreparationClonesLocalFixtureWithSpecialPath(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "fixture repository #1")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit := func(arguments ...string) {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	runGit("init", "--template=", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repository, "fixture.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "fixture.txt")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")

	workspace := filepath.Join(root, "workspace")
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())
	if _, err := client.Prepare(context.Background(), PrepareRequest{
		RepositoryURL: repository,
		Destination:   workspace,
		Setup:         []string{"git", "status", "--porcelain"},
	}); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(workspace, "fixture.txt"))
	if err != nil || string(value) != "fixture\n" {
		t.Fatalf("fixture = %q, %v", value, err)
	}
}

func TestPreparationTimeoutAndCancellation(t *testing.T) {
	for name, testCase := range map[string]struct {
		setupTimeout time.Duration
		cancel       bool
		wantState    PreparationState
	}{
		"timeout":      {setupTimeout: 20 * time.Millisecond, wantState: StateFailed},
		"cancellation": {setupTimeout: time.Second, cancel: true, wantState: StateFailed},
	} {
		t.Run(name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "workspace")
			server, err := NewServer(ServerConfig{
				Token: testToken, Workspace: workspace, SetupTimeout: testCase.setupTimeout,
				CommandRunner: func(ctx context.Context, _ string, _ []string, _ string, _ int64) error {
					<-ctx.Done()
					return ctx.Err()
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			httpServer := httptest.NewServer(server.Handler())
			defer httpServer.Close()
			client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())
			ctx := context.Background()
			if testCase.cancel {
				var cancelFunc context.CancelFunc
				ctx, cancelFunc = context.WithCancel(ctx)
				go func() {
					time.Sleep(20 * time.Millisecond)
					cancelFunc()
				}()
			}
			_, err = client.Prepare(ctx, PrepareRequest{
				Destination: workspace, Setup: []string{"wait"},
			})
			if err == nil {
				t.Fatal("expected preparation failure")
			}
			status, statusErr := client.Status(context.Background())
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			if status.Preparation != testCase.wantState {
				t.Fatalf("state = %s", status.Preparation)
			}
		})
	}
}

func TestRejectsUnsafeWorkspaceAndArguments(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace}); err == nil {
		t.Fatal("expected symlink workspace rejection")
	}

	realWorkspace := filepath.Join(root, "real-workspace")
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: realWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	body, _ := json.Marshal(PrepareRequest{
		Destination: realWorkspace, Setup: []string{"", "bad"},
	})
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+PreparePath, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set(VersionHeader, Version)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestRunCommandHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runCommand(ctx, "sh", []string{"-c", "sleep 10"}, t.TempDir(), 128)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunCommandBoundsOutput(t *testing.T) {
	err := runCommand(
		context.Background(), "printf", []string{"123456789"}, t.TempDir(), 4,
	)
	if err == nil || !strings.Contains(err.Error(), "output limit") {
		t.Fatalf("error = %v", err)
	}
}
