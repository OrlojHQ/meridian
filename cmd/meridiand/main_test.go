package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestAgentSandboxProviderFlagsAreRegistered(t *testing.T) {
	command := newRootCommand()
	for _, name := range []string{
		"provider",
		"api-token-file",
		"allow-non-loopback-listen",
		"capsule-idle-pause",
		"capsule-idle-scan-interval",
		"runtime-refresh-interval",
		"agentsandbox-kubeconfig",
		"agentsandbox-context",
		"agentsandbox-in-cluster",
		"agentsandbox-namespace",
		"agentsandbox-name-prefix",
		"agentsandbox-image",
		"official-pack-tag",
		"agentsandbox-runtime-class",
		"agentsandbox-storage-class",
		"agentsandbox-volume-size",
		"agentsandbox-ttl",
		"agentsandbox-operation-timeout",
		"agentsandbox-setup-timeout",
		"metrics-listen",
		"allow-unsafe-metrics-listen",
		"otel-otlp-endpoint",
		"otel-otlp-insecure",
	} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s is not registered", name)
		}
	}
	provider := command.Flags().Lookup("provider")
	if provider == nil || provider.Usage != "Capsule provider (docker, agentsandbox, or fake for simulated Capsules)" ||
		provider.DefValue != "docker" {
		t.Fatalf("provider flag must default to docker and advertise every provider: %#v", provider)
	}
	image := command.Flags().Lookup("docker-image")
	if image == nil || image.DefValue != domain.LocalCapsuleImage {
		t.Fatalf("development docker-image default = %#v", image)
	}
}

func TestInitDataDirectoryRejectsSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "data")
	command := newRootCommand()
	command.SetArgs([]string{"init-data-dir", "--path", path})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("initialized directory = %v, %v", info, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, path); err != nil {
		t.Fatal(err)
	}
	command = newRootCommand()
	command.SetArgs([]string{"init-data-dir", "--path", path})
	if err := command.Execute(); err == nil {
		t.Fatal("symlink data directory unexpectedly accepted")
	}
}
