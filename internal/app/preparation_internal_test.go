package app

import (
	"github.com/OrlojHQ/meridian/internal/domain"
	"strings"
	"testing"
)

func TestPreparationIdentityInvalidatesEveryByteAffectingInput(t *testing.T) {
	original := domain.Preparation{RepositoryURL: "https://example.test/repo", SourceRevision: strings.Repeat("a", 40), ImageDigest: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Setup: []string{"sh", ".meridian/setup"}}
	if !cacheablePreparation(original) {
		t.Fatal("expected immutable identity")
	}
	for _, mutate := range []func(*domain.Preparation){
		func(p *domain.Preparation) { p.RepositoryURL += "-other" },
		func(p *domain.Preparation) { p.SourceRevision = strings.Repeat("b", 40) },
		func(p *domain.Preparation) { p.ImageDigest = "sha256:" + strings.Repeat("b", 64) },
		func(p *domain.Preparation) { p.Platform = "linux/arm64" },
		func(p *domain.Preparation) { p.Generation++ },
		func(p *domain.Preparation) { p.Setup = []string{"sh", ".meridian/setup-v2"} },
	} {
		changed := original
		mutate(&changed)
		if preparationHash(changed) == preparationHash(original) {
			t.Fatal("changed input reused identity")
		}
	}
	for _, image := range []string{"", "tag", "sha256:bad", "repo:latest"} {
		p := original
		p.ImageDigest = image
		if cacheablePreparation(p) {
			t.Fatalf("accepted %q", image)
		}
	}
	original.SourceRevision = ""
	if cacheablePreparation(original) {
		t.Fatal("unknown source is cacheable")
	}
}
