package harnesssetup

import "strings"

// GitHubAccess is one Capsule session's use of the owner's GitHub
// connection (ADR 0061). Login may be shown to the agent. Token is the
// connection token: it may be placed in that session's environment and must
// not be logged, stored, or written into a file.
type GitHubAccess struct {
	Login string `json:"login,omitempty"`
	Token string `json:"token,omitempty"`
}

// Usable reports whether Token can be placed in an environment and a
// credential-helper response. A control character or space could add another
// environment entry or another helper line.
func (a GitHubAccess) Usable() bool {
	if a.Token == "" || len(a.Token) > 512 {
		return false
	}
	for i := 0; i < len(a.Token); i++ {
		if a.Token[i] <= 0x20 || a.Token[i] > 0x7e {
			return false
		}
	}
	return true
}

// LoginForBrief is Login when it is safe to interpolate into the harness
// brief. An empty result means the brief should name the account without a
// login, so a surprising value cannot change the instruction.
func (a GitHubAccess) LoginForBrief() string {
	return GitHubLoginForBrief(a.Login)
}

// GitHubLoginForBrief applies the same login grammar as LoginForBrief.
func GitHubLoginForBrief(login string) string {
	if len(login) < 1 || len(login) > 39 {
		return ""
	}
	for i, r := range login {
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok || (r == '-' && (i == 0 || i == len(login)-1)) {
			return ""
		}
	}
	return login
}

// Wipe drops the caller's reference to the token. The underlying bytes stay
// until the garbage collector reclaims them, as with other session secrets.
func (a *GitHubAccess) Wipe() {
	if a == nil || a.Token == "" {
		return
	}
	a.Token = ""
}

// ContainsToken reports whether value holds the token. An empty token never
// matches, so a missing credential does not redact everything.
func (a GitHubAccess) ContainsToken(value string) bool {
	return a.Token != "" && strings.Contains(value, a.Token)
}
