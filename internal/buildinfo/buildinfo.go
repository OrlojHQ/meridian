// Package buildinfo exposes version metadata shared by Meridian binaries.
package buildinfo

import "fmt"

// These values may be replaced at build time with -ldflags -X.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info is the structured build metadata for a Meridian binary.
type Info struct {
	Version string
	Commit  string
	Date    string
}

// Current returns the metadata embedded in the running binary.
func Current() Info {
	return Info{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
	}
}

// String returns stable, human-readable build metadata.
func (i Info) String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", i.Version, i.Commit, i.Date)
}
