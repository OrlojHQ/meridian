package capsuleproto

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

func TestGitIdentityIsAGeneratedUserSection(t *testing.T) {
	base := t.TempDir()
	s := &Server{config: ServerConfig{HarnessHome: base}}

	if env, err := s.prepareGitIdentity(nil, map[string]string{"HOME": "/h"}); err != nil || len(env) != 1 {
		t.Fatalf("no identity = %v, %v", env, err)
	}

	env, err := s.prepareGitIdentity(&harnesssetup.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := env["GIT_CONFIG_GLOBAL"]
	if path != filepath.Join(base, ".meridian-identity", "gitconfig") {
		t.Fatalf("GIT_CONFIG_GLOBAL = %q", path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("identity file = %v, %v", info, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "[") != 1 || !strings.Contains(string(content), "[user]") ||
		!strings.Contains(string(content), "\tname = \"Ada Lovelace\"\n") {
		t.Fatalf("identity file:\n%s", content)
	}
	if git, err := exec.LookPath("git"); err == nil {
		command := exec.Command(git, "config", "--global", "--get", "user.email")
		command.Env = []string{"GIT_CONFIG_GLOBAL=" + path, "GIT_CONFIG_NOSYSTEM=1", "HOME=" + t.TempDir()}
		out, err := command.Output()
		if err != nil || strings.TrimSpace(string(out)) != "ada@example.com" {
			t.Fatalf("git reads user.email = %q, %v", out, err)
		}
	}

	// A later session gets the updated identity, and nothing else stays
	// behind in the directory.
	if _, err := s.prepareGitIdentity(&harnesssetup.GitIdentity{Name: "Ada L", Email: "ada@example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(path)
	if !strings.Contains(string(content), `"Ada L"`) {
		t.Fatalf("rewritten identity:\n%s", content)
	}
	entries, err := os.ReadDir(filepath.Join(base, ".meridian-identity"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("identity directory = %v, %v", entries, err)
	}

	// Values that could end the quoted string, add a section, or need
	// escaping are refused, as is an empty identity.
	for _, identity := range []harnesssetup.GitIdentity{
		{Name: "Ada\"\n[core]\n\tsshCommand = \"evil", Email: "a@x"},
		{Name: `Ada\`, Email: "a@x"},
		{Name: "Ada", Email: "a@x\r"},
		{Name: "Ada", Email: ""},
		{},
	} {
		if _, err := s.prepareGitIdentity(&identity, nil); err == nil {
			t.Fatalf("identity %+v was written", identity)
		}
	}
}

// A Capsule process may replace the identity directory with a link. The
// file is written through a rooted handle, so it never lands outside the
// Capsule home.
func TestGitIdentityStaysInsideTheHome(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, ".meridian-identity")); err != nil {
		t.Fatal(err)
	}
	s := &Server{config: ServerConfig{HarnessHome: base}}
	if _, err := s.prepareGitIdentity(&harnesssetup.GitIdentity{Name: "Ada", Email: "ada@example.com"}, nil); err == nil {
		t.Fatal("identity written through a link")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory = %v, %v", entries, err)
	}
}
