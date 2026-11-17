package capsuleproto

import (
	"errors"
	"strings"
	"testing"
)

func TestCloneAccessFamiliesAreTerminalAndRedacted(t *testing.T) {
	cases := []string{
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled",
		"fatal: Authentication failed for 'https://github.com/octocat/hidden.git/'",
		"remote: Repository not found.",
		"fatal: unable to access 'https://x-access-token:secret@github.com/octocat/hidden.git/': The requested URL returned error: 403",
		"fatal: unable to access 'https://github.com/octocat/hidden.git/': The requested URL returned error: 401",
		"fatal: unable to access 'https://github.com/octocat/hidden.git/': The requested URL returned error: 404",
		"remote: Invalid username or token. Password authentication is not supported.",
	}
	for _, output := range cases {
		err := classifyCloneError([]byte(output), errors.New("exit status 128"))
		if !errors.Is(err, errRepositoryAccess) {
			t.Fatalf("classify %q = %v", output, err)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "github.com") {
			t.Fatalf("classified error leaked git output: %q", err.Error())
		}
	}
	if cloneDenied([]byte("fatal: unable to access 'https://github.com/octocat/hidden.git/': Could not resolve host")) {
		t.Fatal("a network failure was treated as an access failure")
	}
}
