package docker

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func TestDockerProviderRunsChecksWithoutCapsuleCredentials(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule-integration:dev"
	}
	provider, err := New(Config{
		Image: image, StateDir: t.TempDir(), OperationTimeout: 30 * time.Second, SetupTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	ctx := context.Background()
	resource, err := provider.Create(ctx, ports.CreateCapsuleRequest{
		CapsuleID:     domain.CapsuleID(fmt.Sprintf("checks-%d", time.Now().UnixNano())),
		RepositoryURL: "file:///fixture", ImageReference: image,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = provider.Delete(context.Background(), resource.ID)
	})
	result, err := provider.RunChecks(ctx, resource.ID, ports.CheckRequest{
		Commands: []ports.CheckCommand{{
			Name: "env", Timeout: 30 * time.Second,
			Argv: []string{"sh", "-c", `printf '%s|%s' "$MERIDIAN_CAPSULE_TOKEN" hello`},
		}, {
			Name: "sleep", Timeout: time.Second,
			Argv: []string{"sleep", "30"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results = %#v", result.Results)
	}
	output := string(result.Results[0].Output)
	if output != "|hello" {
		t.Fatalf("check environment = %q", output)
	}
	if !result.Results[1].TimedOut {
		t.Fatalf("hung check was not killed: %#v", result.Results[1])
	}
	if strings.Contains(output, "docker.sock") {
		t.Fatalf("docker socket leaked into check output: %q", output)
	}
}
