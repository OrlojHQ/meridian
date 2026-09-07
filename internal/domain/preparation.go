package domain

import "time"

// Preparation freezes project inputs before allocating a runtime. It contains no
// resolved credentials and is private control-plane metadata.
type Preparation struct {
	Legacy         bool      `json:"legacy,omitempty"`
	ReuseDisabled  bool      `json:"reuseDisabled"`
	Generation     int64     `json:"generation"`
	CapsuleID      CapsuleID `json:"capsuleId"`
	RepositoryURL  string    `json:"repositoryUrl"`
	ImageReference string    `json:"imageReference"`
	GitSecretName  string    `json:"gitSecretName,omitempty"`
	Setup          []string  `json:"setup"`
	Stage          string    `json:"stage"`
	SourceRevision string    `json:"sourceRevision,omitempty"`
	ImageDigest    string    `json:"imageDigest,omitempty"`
	Platform       string    `json:"platform,omitempty"`
	CacheKey       string    `json:"cacheKey,omitempty"`
	Reused         bool      `json:"reused"`
	CacheMiss      string    `json:"cacheMiss,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// PreparationProgress is the public content-free view of preparation.
type PreparationProgress struct {
	Stage          string    `json:"stage"`
	Reused         bool      `json:"reused"`
	Detail         string    `json:"detail,omitempty"`
	SourceRevision string    `json:"sourceRevision,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (p Preparation) Progress() *PreparationProgress {
	return &PreparationProgress{Stage: p.Stage, Reused: p.Reused, Detail: p.CacheMiss, SourceRevision: p.SourceRevision, UpdatedAt: p.UpdatedAt}
}

type PreparationPolicy struct {
	Disabled   bool  `json:"disabled"`
	Generation int64 `json:"generation"`
}
