// Package domain contains Meridian's provider-independent lifecycle model.
package domain

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ProjectID string
type CapsuleID string
type TimelineID string
type MomentID string
type MomentKind string
type RunID string
type ThreadID string
type ProjectThreadIntentID string
type ThreadMessageID string
type ThreadBlockID string
type EventID string
type ResourceVersion int64
type SecretID string
type SecretPurpose string
type DeliveryID string
type DeliveryState string
type DeliveryAction string

const (
	SecretGitHTTPS   SecretPurpose = "git_https"
	SecretGitPush    SecretPurpose = "git_push"
	SecretGitHubAPI  SecretPurpose = "github_api"
	SecretHarnessEnv SecretPurpose = "harness_env"
)

const MaxHarnessImages = 16

const (
	LocalCapsuleImage         = "meridian-capsule:dev"
	OfficialCapsuleRepository = "ghcr.io/orlojhq/meridian-capsule"
)

// DefaultCapsuleImage is the installation Capsule image for a meridiand binary.
// Release versions advertise the matching GHCR tag so operators do not build
// pack images locally. Development and snapshot builds keep the local :dev tag.
func DefaultCapsuleImage(version string) string {
	if tag := OfficialImageTag(version); tag != "" {
		return OfficialCapsuleRepository + ":" + tag
	}
	return LocalCapsuleImage
}

// OfficialImageTag is the GHCR tag for a stamped meridiand version, or empty
// when the binary is not a published release.
func OfficialImageTag(version string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" || version == "dev" || strings.HasSuffix(version, "-next") {
		return ""
	}
	if version[0] < '0' || version[0] > '9' || strings.ContainsAny(version, "/@ \t\r\n") {
		return ""
	}
	return "v" + version
}

// RegistryQualifiedImage is a reference Docker may pull. Short local names such
// as meridian-capsule:dev are never fetched from Docker Hub.
func RegistryQualifiedImage(ref string) bool {
	registry, _, _, _ := splitImageRef(ref)
	if registry == "" {
		return false
	}
	host, _, _ := strings.Cut(registry, "/")
	return strings.ContainsAny(host, ".:") || host == "localhost"
}

type HarnessImage struct {
	Name           string `json:"name"`
	ImageReference string `json:"imageReference"`
}

type Project struct {
	ID                  ProjectID
	Name                string
	RepositoryURL       string
	Setup               []string
	ImageReference      string
	HarnessImages       []HarnessImage
	GitSecretName       string
	HarnessSecretNames  []string
	GitPushSecretName   string
	GitHubAPISecretName string
	CommitAuthorName    string
	CommitAuthorEmail   string
	DefaultBaseBranch   string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ResourceVersion     ResourceVersion
}

func ValidHarnessName(name string) bool {
	return name != "" && len(name) <= 128 && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func ValidImageReference(image string) bool {
	return image != "" && len(image) <= 1024 && !strings.ContainsAny(image, "\x00\r\n")
}

func ParseHarnessImageSpec(spec string) (HarnessImage, error) {
	spec = strings.TrimSpace(spec)
	name, image, ok := strings.Cut(spec, "=")
	item := HarnessImage{Name: strings.TrimSpace(name), ImageReference: strings.TrimSpace(image)}
	if !ok || !ValidHarnessName(item.Name) || !ValidImageReference(item.ImageReference) {
		return HarnessImage{}, fmt.Errorf("%w: harness image must be name=image", ErrInvalid)
	}
	return item, nil
}

func ParseHarnessImageSpecs(raw string) ([]HarnessImage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n'
	})
	items := make([]HarnessImage, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		item, err := ParseHarnessImageSpec(part)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[item.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate harness image %q", ErrInvalid, item.Name)
		}
		seen[item.Name] = struct{}{}
		items = append(items, item)
	}
	if len(items) > MaxHarnessImages {
		return nil, fmt.Errorf("%w: too many harness images", ErrInvalid)
	}
	return items, nil
}

func (p Project) ImageForHarness(harness string) (string, error) {
	harness = strings.TrimSpace(harness)
	if !ValidHarnessName(harness) {
		return "", fmt.Errorf("%w: harness pack is invalid", ErrInvalid)
	}
	if len(p.HarnessImages) == 0 {
		if len(p.ImageReference) > 1024 || strings.ContainsAny(p.ImageReference, "\x00\r\n") {
			return "", fmt.Errorf("%w: image reference is invalid", ErrInvalid)
		}
		return p.ImageReference, nil
	}
	for _, item := range p.HarnessImages {
		if item.Name == harness {
			return item.ImageReference, nil
		}
	}
	return "", fmt.Errorf("%w: harness %q is not allowlisted on this Project", ErrInvalid, harness)
}

func (p Project) HarnessNames() []string {
	names := make([]string, 0, len(p.HarnessImages))
	for _, item := range p.HarnessImages {
		names = append(names, item.Name)
	}
	return names
}

// InstallationHarnessImages is the daemon-advertised catalog of official
// harness packs. Spawn still uses only names applied on a Project.
// packTag overrides the tag copied from defaultImage so a digest-pinned
// Capsule image can still advertise release-tagged official packs.
func InstallationHarnessImages(defaultImage, packTag string) []HarnessImage {
	defaultImage = strings.TrimSpace(defaultImage)
	if defaultImage == "" {
		defaultImage = LocalCapsuleImage
	}
	return []HarnessImage{
		{Name: "mock", ImageReference: defaultImage},
		{Name: "opencode", ImageReference: officialPackImage(defaultImage, "meridian-capsule-opencode", packTag)},
		{Name: "pi", ImageReference: officialPackImage(defaultImage, "meridian-capsule-pi", packTag)},
		{Name: "claude", ImageReference: officialPackImage(defaultImage, "meridian-capsule-claude", packTag)},
		{Name: "codex", ImageReference: officialPackImage(defaultImage, "meridian-capsule-codex", packTag)},
	}
}

func officialPackImage(defaultImage, repository, packTag string) string {
	registry, _, derivedTag, _ := splitImageRef(defaultImage)
	tag := strings.TrimSpace(packTag)
	if tag == "" {
		tag = derivedTag
	}
	if tag == "" {
		tag = "dev"
	}
	if registry != "" {
		return registry + "/" + repository + ":" + tag
	}
	return repository + ":" + tag
}

func splitImageRef(ref string) (registry, name, tag, digest string) {
	ref = strings.TrimSpace(ref)
	if index := strings.LastIndex(ref, "@"); index >= 0 {
		digest = ref[index+1:]
		ref = ref[:index]
	}
	if index := strings.LastIndex(ref, ":"); index >= 0 && !strings.Contains(ref[index+1:], "/") {
		tag = ref[index+1:]
		ref = ref[:index]
	}
	if index := strings.LastIndex(ref, "/"); index >= 0 {
		return ref[:index], ref[index+1:], tag, digest
	}
	return "", ref, tag, digest
}

const (
	DeliveryQueued     DeliveryState = "queued"
	DeliveryCommitting DeliveryState = "committing"
	DeliveryPushing    DeliveryState = "pushing"
	DeliveryOpeningPR  DeliveryState = "opening_pr"
	DeliverySucceeded  DeliveryState = "succeeded"
	DeliveryFailed     DeliveryState = "failed"

	DeliveryPush            DeliveryAction = "push"
	DeliveryOpenPullRequest DeliveryAction = "open_pull_request"
)

// Delivery binds an explicit approval to exact reviewed Git objects. It
// contains bounded metadata only; credentials, diffs, and source content are
// deliberately absent.
type Delivery struct {
	ID                      DeliveryID
	CapsuleID               CapsuleID
	ProjectID               ProjectID
	State                   DeliveryState
	Action                  DeliveryAction
	Approved                bool
	ApprovedAt              time.Time
	ExpectedCapsuleVersion  ResourceVersion
	ExpectedHEAD            string
	ExpectedTree            string
	RemoteBranch            string
	DestinationRef          string
	BaseBranch              string
	CommitMessage           string
	PullRequestTitle        string
	PullRequestBody         string
	ResultCommitSHA         string
	ResultPullRequestURL    string
	ResultPullRequestNumber int64
	Failure                 string
	IdempotencyKey          string
	CreatedAt               time.Time
	UpdatedAt               time.Time
	ResourceVersion         ResourceVersion
}

func (s DeliveryState) Valid() bool {
	switch s {
	case DeliveryQueued, DeliveryCommitting, DeliveryPushing, DeliveryOpeningPR,
		DeliverySucceeded, DeliveryFailed:
		return true
	default:
		return false
	}
}

func (s DeliveryState) Terminal() bool {
	return s == DeliverySucceeded || s == DeliveryFailed
}

func (a DeliveryAction) Valid() bool {
	return a == DeliveryPush || a == DeliveryOpenPullRequest
}

func CanTransitionDelivery(from, to DeliveryState) bool {
	if from == to {
		return true
	}
	if to == DeliveryFailed && !from.Terminal() {
		return true
	}
	switch from {
	case DeliveryQueued:
		return to == DeliveryCommitting || to == DeliveryPushing
	case DeliveryCommitting:
		return to == DeliveryPushing
	case DeliveryPushing:
		return to == DeliveryOpeningPR || to == DeliverySucceeded
	case DeliveryOpeningPR:
		return to == DeliverySucceeded
	default:
		return false
	}
}

func (d *Delivery) Transition(to DeliveryState, now time.Time) error {
	if !to.Valid() || !CanTransitionDelivery(d.State, to) {
		return fmt.Errorf("%w: Delivery %s to %s", ErrIllegalTransition, d.State, to)
	}
	d.State = to
	d.UpdatedAt = now.UTC()
	d.ResourceVersion++
	return nil
}

func (d Delivery) Validate() error {
	if d.ID == "" || d.CapsuleID == "" || d.ProjectID == "" || !d.State.Valid() ||
		!d.Action.Valid() || !d.Approved || d.ApprovedAt.IsZero() ||
		d.ExpectedCapsuleVersion <= 0 || d.ExpectedHEAD == "" || d.ExpectedTree == "" ||
		d.RemoteBranch == "" || d.DestinationRef != "refs/heads/"+d.RemoteBranch ||
		d.IdempotencyKey == "" || d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() ||
		d.ResourceVersion <= 0 {
		return fmt.Errorf("%w: invalid Delivery", ErrInvalid)
	}
	if len(d.ExpectedHEAD) > 128 || len(d.ExpectedTree) > 128 ||
		len(d.RemoteBranch) > 255 || len(d.BaseBranch) > 255 ||
		len(d.CommitMessage) > 16<<10 || len(d.PullRequestTitle) > 512 ||
		len(d.PullRequestBody) > 64<<10 || len(d.Failure) > 512 ||
		len(d.IdempotencyKey) > 200 || d.ResultPullRequestNumber < 0 {
		return fmt.Errorf("%w: Delivery metadata exceeds limits", ErrInvalid)
	}
	if d.Action == DeliveryPush &&
		(d.PullRequestTitle != "" || d.PullRequestBody != "" ||
			d.ResultPullRequestURL != "" || d.ResultPullRequestNumber != 0) {
		return fmt.Errorf("%w: push Delivery contains pull request metadata", ErrInvalid)
	}
	if d.State == DeliverySucceeded && d.ResultCommitSHA == "" {
		return fmt.Errorf("%w: succeeded Delivery has no resulting commit", ErrInvalid)
	}
	if (d.State == DeliveryFailed) != (d.Failure != "") {
		return fmt.Errorf("%w: invalid Delivery failure", ErrInvalid)
	}
	return nil
}

// Secret contains only an authenticated ciphertext envelope and metadata.
// Plaintext values must never cross the persistence port.
type Secret struct {
	ID              SecretID
	Name            string
	Purpose         SecretPurpose
	EnvelopeVersion uint16
	KEKID           string
	KEKVersion      uint32
	Nonce           []byte
	Ciphertext      []byte
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResourceVersion ResourceVersion
}

func (p SecretPurpose) Valid() bool {
	switch p {
	case SecretGitHTTPS, SecretGitPush, SecretGitHubAPI, SecretHarnessEnv:
		return true
	default:
		return false
	}
}

func (s Secret) Validate() error {
	if s.ID == "" || s.Name == "" || !s.Purpose.Valid() || s.EnvelopeVersion == 0 ||
		s.KEKID == "" || s.KEKVersion == 0 || len(s.Nonce) != 12 ||
		len(s.Ciphertext) == 0 || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() ||
		s.ResourceVersion <= 0 {
		return fmt.Errorf("%w: invalid named secret envelope", ErrInvalid)
	}
	return nil
}

type CapsuleState string

const (
	CapsuleCreating  CapsuleState = "Creating"
	CapsulePreparing CapsuleState = "Preparing"
	CapsuleReady     CapsuleState = "Ready"
	CapsulePaused    CapsuleState = "Paused"
	CapsuleFailed    CapsuleState = "Failed"
	CapsuleSealed    CapsuleState = "Sealed"
	CapsuleDeleting  CapsuleState = "Deleting"
	CapsuleDeleted   CapsuleState = "Deleted"
)

type CapsuleIntent string

const (
	IntentReady   CapsuleIntent = "Ready"
	IntentPaused  CapsuleIntent = "Paused"
	IntentDeleted CapsuleIntent = "Deleted"
	IntentSealed  CapsuleIntent = "Sealed"
)

type Capsule struct {
	Preparation        *PreparationProgress
	ID                 CapsuleID
	ProjectID          ProjectID
	TimelineID         TimelineID
	Name               string
	LauncherHarness    string
	State              CapsuleState
	DesiredState       CapsuleIntent
	ProviderResourceID string
	OriginMomentID     MomentID
	RestoreComplete    bool
	Maintenance        string
	Failure            string
	ImageReference     string
	LastActivityAt     time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ResourceVersion    ResourceVersion
}

func (c Capsule) WorkspaceImage(project Project) string {
	if strings.TrimSpace(c.ImageReference) != "" {
		return c.ImageReference
	}
	return project.ImageReference
}

type TimelineReason string

const (
	TimelineRoot   TimelineReason = "root"
	TimelineShard  TimelineReason = "shard"
	TimelineRewind TimelineReason = "rewind"
)

type Timeline struct {
	ID                 TimelineID
	ProjectID          ProjectID
	CapsuleID          CapsuleID
	ForkedFromMomentID MomentID
	Reason             TimelineReason
	CreatedAt          time.Time
}

type Moment struct {
	ID               MomentID
	ProjectID        ProjectID
	CapsuleID        CapsuleID
	TimelineID       TimelineID
	ParentMomentID   MomentID
	Name             string
	ArchiveSHA256    string
	ArchiveSize      int64
	ManifestSHA256   string
	ImageDigest      string
	ProjectSetupHash string
	GitBranch        string
	GitHEAD          string
	GitDirtySummary  string
	CreatedAt        time.Time
	Final            bool
	Kind             MomentKind
}

const (
	MomentTimeline   MomentKind = "timeline"
	MomentSetupCache MomentKind = "setup_cache"
)

// SetupMomentCache is the single replaceable cache root for a Project.
// Its Moment is internal and immutable; replacing the root makes the prior
// internal Moment unreachable by cache lookup and eligible for later CAS GC.
type SetupMomentCache struct {
	ProjectID  ProjectID
	ConfigHash string
	MomentID   MomentID
	CreatedAt  time.Time
}

func (r TimelineReason) Valid() bool {
	return r == TimelineRoot || r == TimelineShard || r == TimelineRewind
}

func (t Timeline) Validate() error {
	if t.ID == "" || t.ProjectID == "" || t.CapsuleID == "" || !t.Reason.Valid() {
		return fmt.Errorf("%w: invalid Timeline", ErrInvalid)
	}
	if t.Reason == TimelineRoot && t.ForkedFromMomentID != "" {
		return fmt.Errorf("%w: root Timeline cannot have a fork Moment", ErrInvalid)
	}
	if t.Reason != TimelineRoot && t.ForkedFromMomentID == "" {
		return fmt.Errorf("%w: descendant Timeline requires a fork Moment", ErrInvalid)
	}
	return nil
}

func (m Moment) Validate() error {
	kind := m.Kind
	if kind == "" {
		kind = MomentTimeline
	}
	if m.ID == "" || m.ProjectID == "" || m.CapsuleID == "" || m.TimelineID == "" ||
		m.Name == "" || len(m.Name) > 128 || !validSHA256(m.ArchiveSHA256) ||
		!validSHA256(m.ManifestSHA256) || m.ArchiveSize < 0 || m.ImageDigest == "" ||
		!validSHA256(m.ProjectSetupHash) ||
		(kind != MomentTimeline && kind != MomentSetupCache) {
		return fmt.Errorf("%w: invalid Moment", ErrInvalid)
	}
	return nil
}

func (c SetupMomentCache) Validate() error {
	if c.ProjectID == "" || c.MomentID == "" || !validSHA256(c.ConfigHash) ||
		c.CreatedAt.IsZero() {
		return fmt.Errorf("%w: invalid setup Moment cache", ErrInvalid)
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

type Event struct {
	ID              EventID
	Sequence        int64
	AggregateType   string
	AggregateID     string
	Type            string
	Timestamp       time.Time
	ResourceVersion ResourceVersion
	Data            []byte
}

type RunState string

const (
	RunQueued     RunState = "Queued"
	RunStarting   RunState = "Starting"
	RunRunning    RunState = "Running"
	RunSucceeded  RunState = "Succeeded"
	RunFailed     RunState = "Failed"
	RunCancelling RunState = "Cancelling"
	RunCancelled  RunState = "Cancelled"
)

type Run struct {
	ID              RunID
	CapsuleID       CapsuleID
	Harness         string
	State           RunState
	ExitStatus      int
	HasExitStatus   bool
	Failure         string
	EventCursor     uint64
	CreatedAt       time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
	UpdatedAt       time.Time
	ResourceVersion ResourceVersion
}

// ThreadState is the durable lifecycle of an explicit structured agent thread.
// Only Active threads accept appended messages.
type ThreadState string

const (
	ThreadActive   ThreadState = "active"
	ThreadPaused   ThreadState = "paused"
	ThreadArchived ThreadState = "archived"
	ThreadDeleted  ThreadState = "deleted"
)

type Thread struct {
	ID              ThreadID
	CapsuleID       CapsuleID
	State           ThreadState
	CurrentRunID    RunID
	AdapterID       string
	WrappedDEK      []byte
	KEKID           string
	KEKVersion      uint32
	EnvelopeVersion uint16
	MessageCount    int64
	EncryptedBytes  int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       time.Time
	ResourceVersion ResourceVersion
}

type ThreadMessageRole string

const (
	ThreadRoleSystem    ThreadMessageRole = "system"
	ThreadRoleUser      ThreadMessageRole = "user"
	ThreadRoleAssistant ThreadMessageRole = "assistant"
	ThreadRoleTool      ThreadMessageRole = "tool"
)

type ThreadMessageKind string

const (
	ThreadMessagePrompt     ThreadMessageKind = "prompt"
	ThreadMessageResponse   ThreadMessageKind = "response"
	ThreadMessageToolCall   ThreadMessageKind = "tool_call"
	ThreadMessageToolResult ThreadMessageKind = "tool_result"
	ThreadMessageStatus     ThreadMessageKind = "status"
)

type ThreadBlockKind string

const (
	ThreadBlockText       ThreadBlockKind = "text"
	ThreadBlockJSON       ThreadBlockKind = "json"
	ThreadBlockToolCall   ThreadBlockKind = "tool_call"
	ThreadBlockToolResult ThreadBlockKind = "tool_result"
	ThreadBlockError      ThreadBlockKind = "error"
)

// ThreadMessage and ThreadBlock contain encrypted envelopes only. Plaintext
// transcript content must never cross the persistence port.
type ThreadMessage struct {
	ID        ThreadMessageID
	ThreadID  ThreadID
	Sequence  int64
	Role      ThreadMessageRole
	Kind      ThreadMessageKind
	CreatedAt time.Time
	Blocks    []ThreadBlock
}

type ThreadBlock struct {
	ID              ThreadBlockID
	ThreadID        ThreadID
	MessageID       ThreadMessageID
	MessageSequence int64
	Sequence        int64
	Kind            ThreadBlockKind
	EnvelopeVersion uint16
	Ciphertext      []byte
	CreatedAt       time.Time
}

// ThreadDelivery is a content-free durable acknowledgement for one controller
// frame. ControllerID is stable across retries and is also used by capsuled as
// defense-in-depth deduplication.
type ThreadDelivery struct {
	ThreadID     ThreadID
	MessageID    ThreadMessageID
	RunID        RunID
	ControllerID string
	DeliveredAt  time.Time
	CreatedAt    time.Time
}

type ProjectThreadIntentState string

const (
	ProjectThreadProvisioning ProjectThreadIntentState = "provisioning"
	ProjectThreadReady        ProjectThreadIntentState = "ready"
	ProjectThreadFailed       ProjectThreadIntentState = "failed"
)

// ProjectThreadIntent is the durable bridge between a Project-level session
// request and the ordinary Capsule-scoped Thread aggregate. PendingMessage is
// always an authenticated transcript envelope; plaintext never crosses the
// persistence port.
type ProjectThreadIntent struct {
	ID              ProjectThreadIntentID
	ProjectID       ProjectID
	CapsuleID       CapsuleID
	ThreadID        ThreadID
	RunID           RunID
	MessageID       ThreadMessageID
	CapsuleName     string
	RequestedName   string
	Harness         string
	IdempotencyKey  string
	State           ProjectThreadIntentState
	FailureCode     string
	FailureMessage  string
	WrappedDEK      []byte
	KEKID           string
	KEKVersion      uint32
	EnvelopeVersion uint16
	PendingMessage  ThreadMessage
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResourceVersion ResourceVersion
}

func (s ProjectThreadIntentState) Valid() bool {
	return s == ProjectThreadProvisioning || s == ProjectThreadReady || s == ProjectThreadFailed
}

func (i ProjectThreadIntent) Validate() error {
	if i.ID == "" || i.ProjectID == "" || i.CapsuleID == "" || i.ThreadID == "" ||
		i.RunID == "" || i.MessageID == "" || i.CapsuleName == "" ||
		len(i.CapsuleName) > 128 || len(i.RequestedName) > 128 ||
		i.Harness == "" || len(i.Harness) > 128 || i.IdempotencyKey == "" ||
		len(i.IdempotencyKey) > 200 || !i.State.Valid() || i.KEKID == "" ||
		i.KEKVersion == 0 || i.EnvelopeVersion == 0 || i.CreatedAt.IsZero() ||
		i.UpdatedAt.IsZero() || i.ResourceVersion <= 0 {
		return fmt.Errorf("%w: invalid Project Thread intent", ErrInvalid)
	}
	if i.PendingMessage.ID != i.MessageID || i.PendingMessage.ThreadID != i.ThreadID ||
		i.PendingMessage.Sequence != 1 || i.PendingMessage.Role != ThreadRoleUser ||
		i.PendingMessage.Kind != ThreadMessagePrompt {
		return fmt.Errorf("%w: invalid Project Thread pending message", ErrInvalid)
	}
	if err := i.PendingMessage.Validate(); err != nil {
		return err
	}
	switch i.State {
	case ProjectThreadProvisioning, ProjectThreadFailed:
		if len(i.WrappedDEK) != 64 {
			return fmt.Errorf("%w: missing Project Thread key envelope", ErrInvalid)
		}
	case ProjectThreadReady:
		if len(i.WrappedDEK) != 0 {
			return fmt.Errorf("%w: promoted Project Thread retained a duplicate key envelope", ErrInvalid)
		}
	}
	if len(i.FailureCode) > 64 || len(i.FailureMessage) > 256 ||
		(i.State == ProjectThreadFailed) != (i.FailureCode != "") {
		return fmt.Errorf("%w: invalid Project Thread failure metadata", ErrInvalid)
	}
	return nil
}

var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("resource version conflict")
	ErrIllegalTransition = errors.New("illegal lifecycle transition")
	ErrInvalid           = errors.New("invalid input")
	ErrUnsupported       = errors.New("provider capability unsupported")
	ErrResourceExhausted = errors.New("resource capacity exhausted")
	ErrCorrupt           = errors.New("artifact corruption")
	ErrTranscriptCorrupt = errors.New("transcript corruption")
	ErrKeyMismatch       = errors.New("transcript key mismatch")
)

func (s CapsuleState) Valid() bool {
	switch s {
	case CapsuleCreating, CapsulePreparing, CapsuleReady, CapsulePaused,
		CapsuleFailed, CapsuleSealed, CapsuleDeleting, CapsuleDeleted:
		return true
	default:
		return false
	}
}

func (s CapsuleState) Terminal() bool {
	return s == CapsuleSealed || s == CapsuleDeleted
}

func (s RunState) Valid() bool {
	switch s {
	case RunQueued, RunStarting, RunRunning, RunSucceeded, RunFailed, RunCancelling, RunCancelled:
		return true
	default:
		return false
	}
}

func (s RunState) Terminal() bool {
	return s == RunSucceeded || s == RunFailed || s == RunCancelled
}

func (s ThreadState) Valid() bool {
	switch s {
	case ThreadActive, ThreadPaused, ThreadArchived, ThreadDeleted:
		return true
	default:
		return false
	}
}

func (r ThreadMessageRole) Valid() bool {
	switch r {
	case ThreadRoleSystem, ThreadRoleUser, ThreadRoleAssistant, ThreadRoleTool:
		return true
	default:
		return false
	}
}

func (k ThreadMessageKind) Valid() bool {
	switch k {
	case ThreadMessagePrompt, ThreadMessageResponse, ThreadMessageToolCall,
		ThreadMessageToolResult, ThreadMessageStatus:
		return true
	default:
		return false
	}
}

func (k ThreadBlockKind) Valid() bool {
	switch k {
	case ThreadBlockText, ThreadBlockJSON, ThreadBlockToolCall, ThreadBlockToolResult, ThreadBlockError:
		return true
	default:
		return false
	}
}

func CanTransitionThread(from, to ThreadState) bool {
	if from == to {
		return true
	}
	switch from {
	case ThreadActive:
		return to == ThreadPaused || to == ThreadArchived || to == ThreadDeleted
	case ThreadPaused:
		return to == ThreadActive || to == ThreadArchived || to == ThreadDeleted
	case ThreadArchived:
		return to == ThreadDeleted
	default:
		return false
	}
}

func (t *Thread) Transition(to ThreadState, now time.Time) error {
	if !to.Valid() {
		return fmt.Errorf("%w: unknown Thread state", ErrInvalid)
	}
	if !CanTransitionThread(t.State, to) {
		return fmt.Errorf("%w: Thread lifecycle transition", ErrIllegalTransition)
	}
	t.State = to
	t.UpdatedAt = now.UTC()
	t.ResourceVersion++
	if to == ThreadDeleted {
		t.DeletedAt = now.UTC()
		t.WrappedDEK = nil
		t.CurrentRunID = ""
	}
	return nil
}

func (t Thread) Validate() error {
	if t.ID == "" || t.CapsuleID == "" || !t.State.Valid() ||
		len(t.AdapterID) == 0 || len(t.AdapterID) > 128 ||
		t.ResourceVersion <= 0 || t.MessageCount < 0 || t.EncryptedBytes < 0 ||
		t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: invalid Thread", ErrInvalid)
	}
	if t.State == ThreadDeleted {
		if len(t.WrappedDEK) != 0 || t.DeletedAt.IsZero() {
			return fmt.Errorf("%w: invalid deleted Thread", ErrInvalid)
		}
		return nil
	}
	if len(t.WrappedDEK) == 0 || t.KEKID == "" || len(t.KEKID) > 128 ||
		t.KEKVersion == 0 || t.EnvelopeVersion == 0 || !t.DeletedAt.IsZero() {
		return fmt.Errorf("%w: invalid Thread key envelope", ErrInvalid)
	}
	return nil
}

func (m ThreadMessage) Validate() error {
	if m.ID == "" || m.ThreadID == "" || m.Sequence <= 0 ||
		!m.Role.Valid() || !m.Kind.Valid() || m.CreatedAt.IsZero() ||
		len(m.Blocks) == 0 {
		return fmt.Errorf("%w: invalid ThreadMessage", ErrInvalid)
	}
	for index := range m.Blocks {
		block := &m.Blocks[index]
		if block.ID == "" || block.ThreadID != m.ThreadID || block.MessageID != m.ID ||
			block.MessageSequence != m.Sequence || block.Sequence != int64(index+1) ||
			!block.Kind.Valid() || block.EnvelopeVersion == 0 ||
			len(block.Ciphertext) == 0 || block.CreatedAt.IsZero() {
			return fmt.Errorf("%w: invalid ThreadBlock", ErrInvalid)
		}
	}
	return nil
}

func CanTransitionRun(from, to RunState) bool {
	if from == to {
		return true
	}
	switch from {
	case RunQueued:
		return to == RunStarting || to == RunCancelling || to == RunFailed
	case RunStarting:
		return to == RunRunning || to == RunSucceeded || to == RunFailed || to == RunCancelling || to == RunCancelled
	case RunRunning:
		return to == RunSucceeded || to == RunFailed || to == RunCancelling || to == RunCancelled
	case RunCancelling:
		return to == RunCancelled || to == RunFailed
	default:
		return false
	}
}

func (r *Run) Transition(to RunState, now time.Time) error {
	if !to.Valid() {
		return fmt.Errorf("%w: unknown Run state %q", ErrInvalid, to)
	}
	if !CanTransitionRun(r.State, to) {
		return fmt.Errorf("%w: Run %s to %s", ErrIllegalTransition, r.State, to)
	}
	r.State = to
	r.UpdatedAt = now.UTC()
	r.ResourceVersion++
	if to == RunStarting && r.StartedAt.IsZero() {
		r.StartedAt = now.UTC()
	}
	if to.Terminal() {
		r.FinishedAt = now.UTC()
	}
	return nil
}

func CanTransition(from, to CapsuleState) bool {
	if from == to {
		return true
	}
	switch from {
	case CapsuleCreating:
		return to == CapsulePreparing || to == CapsuleFailed || to == CapsuleDeleting
	case CapsulePreparing:
		return to == CapsuleReady || to == CapsuleFailed || to == CapsuleDeleting
	case CapsuleReady:
		return to == CapsulePaused || to == CapsuleFailed || to == CapsuleSealed || to == CapsuleDeleting
	case CapsulePaused:
		return to == CapsuleReady || to == CapsuleFailed || to == CapsuleSealed || to == CapsuleDeleting
	case CapsuleFailed:
		return to == CapsuleCreating || to == CapsulePreparing || to == CapsuleReady ||
			to == CapsulePaused || to == CapsuleDeleting
	case CapsuleDeleting:
		return to == CapsuleDeleted || to == CapsuleFailed
	case CapsuleSealed, CapsuleDeleted:
		return false
	default:
		return false
	}
}

func (c *Capsule) Transition(to CapsuleState, now time.Time) error {
	if !to.Valid() {
		return fmt.Errorf("%w: unknown state %q", ErrInvalid, to)
	}
	if !CanTransition(c.State, to) {
		return fmt.Errorf("%w: %s to %s", ErrIllegalTransition, c.State, to)
	}
	c.State = to
	c.UpdatedAt = now.UTC()
	c.ResourceVersion++
	return nil
}

func (c Capsule) CanMutate() error {
	if c.State.Terminal() {
		return fmt.Errorf("%w: capsule is %s", ErrIllegalTransition, c.State)
	}
	if c.Maintenance != "" {
		return fmt.Errorf("%w: capsule maintenance %q is active", ErrConflict, c.Maintenance)
	}
	return nil
}
