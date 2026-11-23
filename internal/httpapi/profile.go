package httpapi

import (
	"net/http"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

// A profile import carries up to one bundle per harness, each bounded at
// 512 KiB of content before JSON escaping, plus bounded reports.
const maxProfileImportBody = 16 << 20

type profileResponse struct {
	GitName             string                 `json:"gitName"`
	GitEmail            string                 `json:"gitEmail"`
	CapsuleGitHubAccess bool                   `json:"capsuleGitHubAccess"`
	DefaultConnections  map[string]string      `json:"defaultConnections"`
	Setups              []domain.HarnessSetup  `json:"setups"`
	ResourceVersion     domain.ResourceVersion `json:"resourceVersion"`
	UpdatedAt           *time.Time             `json:"updatedAt,omitempty"`
}

func profileJSON(profile app.Profile) profileResponse {
	result := profileResponse{
		GitName: profile.GitName, GitEmail: profile.GitEmail,
		CapsuleGitHubAccess: profile.CapsuleGitHubAccess,
		DefaultConnections:  profile.DefaultConnections, Setups: profile.Setups,
		ResourceVersion: profile.ResourceVersion,
	}
	if result.DefaultConnections == nil {
		result.DefaultConnections = map[string]string{}
	}
	if result.Setups == nil {
		result.Setups = []domain.HarnessSetup{}
	}
	if !profile.UpdatedAt.IsZero() {
		updated := profile.UpdatedAt
		result.UpdatedAt = &updated
	}
	return result
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := s.service.GetProfile(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profileJSON(profile))
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	var input app.ProfileUpdate
	if !decode(w, r, &input) {
		return
	}
	profile, err := s.service.UpdateProfile(r.Context(), input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profileJSON(profile))
}

func (s *Server) importProfile(w http.ResponseWriter, r *http.Request) {
	var input app.ProfileImport
	if !decodeLimit(w, r, &input, maxProfileImportBody) {
		return
	}
	result, err := s.service.ImportProfile(r.Context(), input, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, err)
		return
	}
	saved := result.Saved
	if saved == nil {
		saved = []domain.HarnessSetup{}
	}
	unchanged := result.Unchanged
	if unchanged == nil {
		unchanged = []string{}
	}
	writeJSON(w, http.StatusCreated, struct {
		Profile   profileResponse       `json:"profile"`
		Saved     []domain.HarnessSetup `json:"saved"`
		Unchanged []string              `json:"unchanged"`
	}{profileJSON(result.Profile), saved, unchanged})
}

type setupRevisionRefResponse struct {
	SetupID   string    `json:"setupId"`
	SetupName string    `json:"setupName"`
	Revision  string    `json:"revision"`
	SavedAt   time.Time `json:"savedAt"`
}

type connectionDiagnosticResponse struct {
	Granted      bool   `json:"granted"`
	ConnectionID string `json:"connectionId,omitempty"`
	Name         string `json:"name,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Revoked      bool   `json:"revoked"`
	NeedsSignIn  bool   `json:"needsSignIn"`
}

type harnessDiagnosticResponse struct {
	Harness         string                       `json:"harness"`
	Pinned          *setupRevisionRefResponse    `json:"pinned,omitempty"`
	Current         *setupRevisionRefResponse    `json:"current,omitempty"`
	ProjectOverride string                       `json:"projectOverride"`
	FilesAdded      []string                     `json:"filesAdded"`
	FilesRemoved    []string                     `json:"filesRemoved"`
	HasReport       bool                         `json:"hasReport"`
	Exclusions      []harnesssetup.Issue         `json:"exclusions"`
	Warnings        []harnesssetup.Issue         `json:"warnings"`
	Connection      connectionDiagnosticResponse `json:"connection"`
}

type commitAuthorResponse struct {
	Source       string `json:"source"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	ProfileName  string `json:"profileName"`
	ProfileEmail string `json:"profileEmail"`
}

type profileDiagnosticResponse struct {
	CapsuleID      domain.CapsuleID            `json:"capsuleId"`
	NothingPinned  bool                        `json:"nothingPinned"`
	MatchesProfile bool                        `json:"matchesProfile"`
	Harnesses      []harnessDiagnosticResponse `json:"harnesses"`
	CommitAuthor   commitAuthorResponse        `json:"commitAuthor"`
}

func revisionRefJSON(ref *app.SetupRevisionRef) *setupRevisionRefResponse {
	if ref == nil {
		return nil
	}
	return &setupRevisionRefResponse{SetupID: ref.SetupID, SetupName: ref.SetupName, Revision: ref.Revision, SavedAt: ref.SavedAt}
}

func nonNilIssues(items []harnesssetup.Issue) []harnesssetup.Issue {
	if items == nil {
		return []harnesssetup.Issue{}
	}
	return items
}

func nonNilStrings(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

func diagnosticJSON(diagnostic app.ProfileDiagnostic) profileDiagnosticResponse {
	result := profileDiagnosticResponse{
		CapsuleID: diagnostic.CapsuleID, NothingPinned: diagnostic.NothingPinned, MatchesProfile: diagnostic.MatchesProfile,
		Harnesses: []harnessDiagnosticResponse{},
		CommitAuthor: commitAuthorResponse{
			Source: diagnostic.CommitAuthor.Source, Name: diagnostic.CommitAuthor.Name, Email: diagnostic.CommitAuthor.Email,
			ProfileName: diagnostic.CommitAuthor.ProfileName, ProfileEmail: diagnostic.CommitAuthor.ProfileEmail,
		},
	}
	for _, row := range diagnostic.Harnesses {
		result.Harnesses = append(result.Harnesses, harnessDiagnosticResponse{
			Harness: row.Harness, Pinned: revisionRefJSON(row.Pinned), Current: revisionRefJSON(row.Current),
			ProjectOverride: row.ProjectOverride, FilesAdded: nonNilStrings(row.FilesAdded), FilesRemoved: nonNilStrings(row.FilesRemoved),
			HasReport: row.HasReport, Exclusions: nonNilIssues(row.Exclusions), Warnings: nonNilIssues(row.Warnings),
			Connection: connectionDiagnosticResponse{
				Granted: row.Connection.Granted, ConnectionID: row.Connection.ConnectionID, Name: row.Connection.Name,
				Provider: row.Connection.Provider, Revoked: row.Connection.Revoked, NeedsSignIn: row.Connection.NeedsSignIn,
			},
		})
	}
	return result
}

// The diagnostic holds config key paths and laptop paths from the owner's
// import reports, so it is never cached.
func (s *Server) capsuleProfileDiagnostic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	diagnostic, err := s.service.CapsuleProfileDiagnostic(r.Context(), domain.CapsuleID(r.PathValue("capsuleId")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, diagnosticJSON(diagnostic))
}

func (s *Server) updateCapsuleProfile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	diagnostic, err := s.service.UpdateCapsuleProfile(r.Context(), domain.CapsuleID(r.PathValue("capsuleId")), r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, diagnosticJSON(diagnostic))
}
