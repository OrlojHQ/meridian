package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
)

func TestLoadTokenFilePermissions(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyz0123456789"
	for _, mode := range []os.FileMode{0o400, 0o440, 0o600, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(token), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			got, err := loadToken(path)
			if err != nil || got != token {
				t.Fatalf("loadToken() = %q, %v", got, err)
			}
		})
	}
}

func TestLoadTokenRejectsExposedAndNonRegularFiles(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o660, 0o700} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte("token"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := loadToken(path); err == nil {
				t.Fatal("exposed token file was accepted")
			}
		})
	}

	directory := filepath.Join(t.TempDir(), "token")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(directory); err == nil {
		t.Fatal("directory token path was accepted")
	}
}

func TestLoadTokenAcceptsContainedProjectionSymlinkOnly(t *testing.T) {
	root := t.TempDir()
	projection := filepath.Join(root, "..2026_08_24")
	if err := os.Mkdir(projection, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(projection, "token")
	if err := os.WriteFile(target, []byte("projected-token"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o440); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "token")
	if err := os.Symlink(filepath.Join("..2026_08_24", "token"), path); err != nil {
		t.Fatal(err)
	}
	if value, err := loadToken(path); err != nil || value != "projected-token" {
		t.Fatalf("contained projection = %q, %v", value, err)
	}

	outside := filepath.Join(t.TempDir(), "outside-token")
	if err := os.WriteFile(outside, []byte("outside"), 0o400); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external")
	if err := os.Symlink(outside, external); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(external); err == nil {
		t.Fatal("external token symlink was accepted")
	}
}

func TestLoadPodTLSIsAllOrNothingAndNeverLoopback(t *testing.T) {
	material, err := capsuleproto.GeneratePodTLS(capsuleproto.PodTLSServerName("meridian-one"), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	paths := map[string]string{}
	for _, key := range []string{capsuleproto.PodTLSServerCert, capsuleproto.PodTLSServerKey, capsuleproto.PodTLSClientCert} {
		path := filepath.Join(directory, key)
		if err := os.WriteFile(path, material[key], 0o400); err != nil {
			t.Fatal(err)
		}
		paths[key] = path
	}
	cert, key, client := paths[capsuleproto.PodTLSServerCert], paths[capsuleproto.PodTLSServerKey], paths[capsuleproto.PodTLSClientCert]
	if config, err := loadPodTLS("", "", "", ""); err != nil || config != nil {
		t.Fatalf("no TLS flags = %v, %v", config, err)
	}
	for name, flags := range map[string][4]string{
		"listen only":      {":7778", "", "", ""},
		"no client cert":   {":7778", cert, key, ""},
		"no listen":        {"", cert, key, client},
		"loopback":         {"127.0.0.1:7778", cert, key, client},
		"localhost":        {"localhost:7778", cert, key, client},
		"client key mixup": {":7778", cert, client, client},
	} {
		if _, err := loadPodTLS(flags[0], flags[1], flags[2], flags[3]); err == nil {
			t.Errorf("%s: pod TLS listener accepted", name)
		}
	}
	config, err := loadPodTLS(":7778", cert, key, client)
	if err != nil {
		t.Fatal(err)
	}
	if config.ClientAuth != tls.RequireAndVerifyClientCert || config.MinVersion != tls.VersionTLS13 {
		t.Fatalf("pod TLS listener config = %#v", config)
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPodTLS(":7778", cert, key, client); err == nil {
		t.Error("world-readable TLS key accepted")
	}
}
