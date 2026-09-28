package domain_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harness"
)

func TestInstallationHarnessPackModesMatchOfficialManifests(t *testing.T) {
	for _, pack := range domain.InstallationHarnessImages("meridian-capsule:dev", "") {
		if pack.Name == "mock" {
			// The supervisor image installs no trusted profile, so the
			// repository decides and the catalog must not claim a mode.
			if pack.InteractionModes != nil {
				t.Fatalf("mock declares modes %v", pack.InteractionModes)
			}
			continue
		}
		path := filepath.Join("..", "..", "images", "capsule-"+pack.Name, "project.yaml")
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		config, err := harness.Parse(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		var modes []domain.HarnessInteractionMode
		for _, profile := range config.Harnesses {
			if profile.Name != pack.Name {
				continue
			}
			mode := domain.HarnessInteractionNative
			if profile.Interaction == harness.InteractionStructured {
				mode = domain.HarnessInteractionStructured
			}
			if !slices.Contains(modes, mode) {
				modes = append(modes, mode)
			}
		}
		slices.Sort(modes)
		declared := slices.Clone(pack.InteractionModes)
		slices.Sort(declared)
		if !slices.Equal(modes, declared) {
			t.Fatalf("%s declares %v but %s provides %v", pack.Name, declared, path, modes)
		}
	}
}

func TestInstallationHarnessPacksMatchPackDirectories(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "images", "capsule-*", "pack.env"))
	if err != nil {
		t.Fatal(err)
	}
	var built []string
	for _, match := range matches {
		built = append(built, strings.TrimPrefix(filepath.Base(filepath.Dir(match)), "capsule-"))
	}
	var advertised []string
	for _, pack := range domain.InstallationHarnessImages("meridian-capsule:dev", "") {
		if pack.Name != "mock" {
			advertised = append(advertised, pack.Name)
		}
	}
	slices.Sort(built)
	slices.Sort(advertised)
	if !slices.Equal(built, advertised) {
		t.Fatalf("catalog advertises %v but images/capsule-*/pack.env builds %v", advertised, built)
	}
}

func TestStructuredUnsupported(t *testing.T) {
	packs := domain.InstallationHarnessImages("meridian-capsule:dev", "")
	for _, name := range []string{"claude", "codex", "opencode", "pi"} {
		image := domain.HarnessImage{Name: name, ImageReference: "meridian-capsule-" + name + ":dev"}
		if !domain.StructuredUnsupported(packs, image) {
			t.Fatalf("%s must be known to reject structured sessions", name)
		}
	}
	for _, image := range []domain.HarnessImage{
		// mock's profiles come from the repository.
		{Name: "mock", ImageReference: "meridian-capsule:dev"},
		// A custom image under an official name is not the catalog pack.
		{Name: "claude", ImageReference: "example.test/custom-claude:1"},
		// A Project may pin an older tag than the catalog advertises.
		{Name: "pi", ImageReference: "meridian-capsule-pi:v0.1.0"},
		{Name: "custom", ImageReference: "example.test/custom:1"},
	} {
		if domain.StructuredUnsupported(packs, image) {
			t.Fatalf("%#v must stay undecided until its Capsule reports profiles", image)
		}
	}
	structured := []domain.HarnessPack{{
		HarnessImage: domain.HarnessImage{Name: "agent", ImageReference: "agent:1"},
		InteractionModes: []domain.HarnessInteractionMode{
			domain.HarnessInteractionNative, domain.HarnessInteractionStructured,
		},
	}}
	if domain.StructuredUnsupported(structured, structured[0].HarnessImage) {
		t.Fatal("a pack declaring structured must be allowed")
	}
	if domain.StructuredUnsupported(nil, structured[0].HarnessImage) {
		t.Fatal("an empty catalog must not reject anything")
	}
}
