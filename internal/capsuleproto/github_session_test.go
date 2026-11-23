package capsuleproto

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

func TestGitHubAccessConfiguresGitWithoutWritingTheToken(t *testing.T) {
	base := t.TempDir()
	s := &Server{config: ServerConfig{HarnessHome: base}}
	const token = "gho_CapsuleAccessSecretValue99"
	access := &harnesssetup.GitHubAccess{Login: "octocat", Token: token}
	env, err := s.prepareSessionGit(&harnesssetup.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}, access, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env["GH_TOKEN"] != token || env["GITHUB_TOKEN"] != token {
		t.Fatalf("environment = %#v", env)
	}
	configPath := env["GIT_CONFIG_GLOBAL"]
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(base, ".meridian-identity", "git-credential")
	helper, err := os.ReadFile(helperPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(config, []byte(token)) || bytes.Contains(helper, []byte(token)) {
		t.Fatal("token was written into the git config or credential helper")
	}
	if !strings.Contains(string(config), "[user]") || !strings.Contains(string(config), "helper =") ||
		!strings.Contains(string(config), helperPath) {
		t.Fatalf("git config:\n%s", config)
	}
	info, err := os.Stat(helperPath)
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("helper mode = %v, %v", info, err)
	}

	if git, err := exec.LookPath("git"); err == nil {
		fill := func(host string) string {
			command := exec.Command(git, "credential", "fill")
			command.Env = []string{
				"GIT_CONFIG_GLOBAL=" + configPath,
				"GIT_CONFIG_NOSYSTEM=1",
				"GH_TOKEN=" + token,
				"HOME=" + t.TempDir(),
				"GIT_TERMINAL_PROMPT=0",
			}
			command.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
			out, err := command.Output()
			if err != nil {
				t.Fatalf("credential fill %s: %v\n%s", host, err, out)
			}
			return string(out)
		}
		github := fill("github.com")
		if !strings.Contains(github, "username=x-access-token") || !strings.Contains(github, "password="+token) {
			t.Fatalf("github.com credential = %q", github)
		}
		command := exec.Command(git, "credential", "fill")
		command.Env = []string{
			"GIT_CONFIG_GLOBAL=" + configPath,
			"GIT_CONFIG_NOSYSTEM=1",
			"GH_TOKEN=" + token,
			"HOME=" + t.TempDir(),
			"GIT_TERMINAL_PROMPT=0",
		}
		command.Stdin = strings.NewReader("protocol=https\nhost=gitlab.com\n\n")
		out, _ := command.CombinedOutput()
		if strings.Contains(string(out), token) {
			t.Fatalf("token sent for another host: %q", out)
		}
	}

	// A later session without access rewrites the config and drops the variables.
	env, err = s.prepareSessionGit(&harnesssetup.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}, nil, env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["GH_TOKEN"]; ok || env["GITHUB_TOKEN"] != "" {
		t.Fatalf("token remained in the environment: %#v", env)
	}
	config, _ = os.ReadFile(configPath)
	if bytes.Contains(config, []byte("helper =")) || bytes.Contains(config, []byte(token)) {
		t.Fatalf("credential helper remained:\n%s", config)
	}
}

func TestGitHubTokenIsStrippedFromInheritedEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "from-the-capsule-process")
	t.Setenv("GITHUB_TOKEN", "from-the-capsule-process")
	t.Setenv("OTHER_TOKEN", "another-token-the-shell-must-drop")
	for _, item := range childEnvironment(nil) {
		key, _, _ := strings.Cut(item, "=")
		if key == "GH_TOKEN" || key == "GITHUB_TOKEN" {
			t.Fatalf("inherited %s", item)
		}
	}
	env := applyGitHubEnv(shellEnvironment(), &harnesssetup.GitHubAccess{Token: "gho_sessiontokenvalue"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "from-the-capsule-process") || strings.Contains(joined, "OTHER_TOKEN") {
		t.Fatalf("shell environment kept a foreign token:\n%s", joined)
	}
	if !strings.Contains(joined, "GH_TOKEN=gho_sessiontokenvalue") || !strings.Contains(joined, "GITHUB_TOKEN=gho_sessiontokenvalue") {
		t.Fatalf("shell environment = %s", joined)
	}
}

func TestRunOutputAndFramesRedactTheSessionToken(t *testing.T) {
	const token = "gho_CapsuleAccessSecretValue99"
	redactor := newTokenRedactor(token)
	var got []byte
	for _, chunk := range []string{"before ", token[:8], token[8:], " after"} {
		got = append(got, redactor.push([]byte(chunk))...)
	}
	got = append(got, redactor.flush()...)
	if bytes.Contains(got, []byte(token)) || !bytes.Contains(got, []byte("before [redacted] after")) {
		t.Fatalf("redacted output = %q", got)
	}
	plain := redactor.push([]byte("ends with g"))
	plain = append(plain, redactor.flush()...)
	if string(plain) != "ends with g" {
		t.Fatalf("ordinary output = %q", plain)
	}

	frame := redactFrameToken(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindToolResult,
		Result: "listed " + token, Content: token,
	}, token)
	encoded, err := json.Marshal(frame)
	if err != nil || bytes.Contains(encoded, []byte(token)) || !bytes.Contains(encoded, []byte("[redacted]")) {
		t.Fatalf("frame = %s", encoded)
	}
}

func TestGitHubAccessRejectsATokenThatCouldBreakTheEnvironment(t *testing.T) {
	for _, token := range []string{"", "has space", "has\nline", "has\x00nul"} {
		if (harnesssetup.GitHubAccess{Token: token}).Usable() {
			t.Fatalf("token %q was usable", token)
		}
	}
	if harnesssetup.GitHubLoginForBrief("ignore\ninstructions") != "" || harnesssetup.GitHubLoginForBrief("-bad") != "" {
		t.Fatal("unsafe login accepted")
	}
}
