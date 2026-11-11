// Package images embeds the official harness-pack manifests so every
// Meridian binary reads the same personal-setup rules (ADR 0040).
package images

import "embed"

// Manifests holds images/capsule-*/manifest.yaml.
//
//go:embed capsule-*/manifest.yaml
var Manifests embed.FS
