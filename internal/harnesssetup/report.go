package harnesssetup

import (
	"errors"
	"regexp"
	"unicode"
	"unicode/utf8"
)

// Report is what an import left out and what it kept with a warning (ADR
// 0049). It is the importing client's account, so it is bounded and treated
// as display text. It is sealed with its revision and never holds file
// contents.
type Report struct {
	Issues   []Issue `json:"issues"`
	Warnings []Issue `json:"warnings"`
}

const (
	maxReportEntries = 512
	maxReportPath    = 1024
	maxReportReason  = 512
)

// reportHomePath matches a path under a user's home directory inside a report
// entry, such as the project directory in a Codex trust key
// (`config.toml:projects:/Users/ada/work/acme:trust_level`). It stops at the
// next colon, the separator report locations use.
var reportHomePath = regexp.MustCompile(`(?:/Users/|/home/|/root/|\b[A-Za-z]:\\Users\\)[^:]*`)

// RedactPath replaces any home-directory path in a report entry with "~/…",
// so a saved report never carries laptop usernames or project names.
func RedactPath(p string) string {
	return reportHomePath.ReplaceAllString(p, "~/…")
}

// Redacted returns the report with home-directory paths removed from every
// entry. Reports are redacted before they leave the laptop and again before
// the server seals them.
func (r Report) Redacted() Report {
	redact := func(items []Issue) []Issue {
		out := make([]Issue, 0, len(items))
		for _, item := range items {
			out = append(out, Issue{Path: RedactPath(item.Path), Reason: RedactPath(item.Reason)})
		}
		return out
	}
	return Report{Issues: redact(r.Issues), Warnings: redact(r.Warnings)}
}

// Report returns the preview's exclusions and warnings.
func (p Preview) Report() Report {
	report := Report{Issues: append([]Issue{}, p.Issues...), Warnings: append([]Issue{}, p.Warnings...)}
	return report
}

// Validate bounds a report before it is sealed.
func (r Report) Validate() error {
	if len(r.Issues)+len(r.Warnings) > maxReportEntries {
		return errors.New("import report has too many entries")
	}
	for _, list := range [][]Issue{r.Issues, r.Warnings} {
		for _, issue := range list {
			if !reportText(issue.Path, maxReportPath) || issue.Reason == "" || !reportText(issue.Reason, maxReportReason) {
				return errors.New("import report entry is not bounded display text")
			}
		}
	}
	return nil
}

func reportText(value string, limit int) bool {
	if len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
