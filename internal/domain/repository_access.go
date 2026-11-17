package domain

import (
	"fmt"
	"net/url"
	"strings"
)

// RepositoryAccessError is a terminal clone failure. Error is safe to show.
type RepositoryAccessError struct {
	Owner string
	Name  string
}

func (e *RepositoryAccessError) Error() string {
	if e == nil {
		return RepositoryAccess.Message()
	}
	return RepositoryAccessMessage(e.Owner, e.Name)
}

func (e *RepositoryAccessError) Unwrap() error { return ErrRepositoryAccess }

// RepositoryAccessMessage is the sentence shown for a GitHub repository the
// station could not read. Owner and name are included only when they are
// plain repository slugs.
func RepositoryAccessMessage(owner, name string) string {
	if !plainSlug(owner) || !plainSlug(name) {
		return RepositoryAccess.Message()
	}
	return fmt.Sprintf("Meridian couldn't access %s/%s. It may be private.", owner, name)
}

// RepositoryAccessFromURL builds that error from a credential-free GitHub URL.
// A URL that is not a GitHub repository still returns the generic sentence.
func RepositoryAccessFromURL(repositoryURL string) error {
	owner, name := githubSlug(repositoryURL)
	return &RepositoryAccessError{Owner: owner, Name: name}
}

func githubSlug(repositoryURL string) (string, string) {
	parsed, err := url.Parse(repositoryURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil {
		return "", ""
	}
	trimmed := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	owner, name, ok := strings.Cut(trimmed, "/")
	if !ok || strings.Contains(name, "/") {
		return "", ""
	}
	return owner, name
}

// RedactFailureText drops a failure that embeds a URL userinfo, which is
// where a git credential would appear. Other text is length-bounded.
func RedactFailureText(text string) string {
	if strings.Contains(text, "://") && strings.Contains(text, "@") {
		return "Capsule setup failed"
	}
	if len(text) > 300 {
		return text[:300]
	}
	return text
}

func plainSlug(value string) bool {
	if value == "" || len(value) > 100 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}
