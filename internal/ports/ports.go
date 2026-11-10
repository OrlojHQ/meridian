// Package ports defines the narrow boundaries used by the application layer.
package ports

import (
	"context"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"io"
	"net/http"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

type Page struct {
	Offset int
	Limit  int
}

type Reader interface {
	GetPreparationPolicy(context.Context, domain.ProjectID) (domain.PreparationPolicy, error)
	GetProjectThreadIntentByCapsule(context.Context, domain.CapsuleID) (domain.ProjectThreadIntent, error)
	GetPreparation(context.Context, domain.CapsuleID) (domain.Preparation, error)
	ProjectHarnessSetups(context.Context, domain.ProjectID) (map[string]string, error)
	ListProviderConnections(context.Context) ([]domain.ProviderConnection, error)
	GetProviderConnection(context.Context, string) (domain.ProviderConnection, error)
	GetProjectProviderConnection(context.Context, domain.ProjectID, string) (string, error)
	ListHarnessSetups(context.Context) ([]domain.HarnessSetup, error)
	GetHarnessSetupRevision(context.Context, string) (domain.HarnessSetupRevision, error)
	ListHarnessSetupRevisions(context.Context, string) ([]domain.HarnessSetupRevision, error)
	GetPinnedHarnessSetup(context.Context, domain.CapsuleID, string) (string, error)
	GetProject(context.Context, domain.ProjectID) (domain.Project, error)
	ListProjects(context.Context, Page) ([]domain.Project, bool, error)
	ListProjectsInDeletion(context.Context) ([]domain.Project, error)
	ArtifactReferenced(context.Context, string) (bool, error)
	GetSecret(context.Context, string) (domain.Secret, error)
	ListSecrets(context.Context, Page) ([]domain.Secret, bool, error)
	GetCapsule(context.Context, domain.CapsuleID) (domain.Capsule, error)
	ListCapsules(context.Context, domain.ProjectID, domain.CapsuleRole, Page) ([]domain.Capsule, bool, error)
	CountLiveCapsules(context.Context) (int, error)
	ListIdleCapsules(context.Context, time.Time, int) ([]domain.Capsule, error)
	ListRecoverableCapsules(context.Context) ([]domain.Capsule, error)
	GetRun(context.Context, domain.RunID) (domain.Run, error)
	ListRuns(context.Context, domain.CapsuleID, Page) ([]domain.Run, bool, error)
	LatestRun(context.Context, domain.CapsuleID) (domain.Run, error)
	ListRecoverableRuns(context.Context) ([]domain.Run, error)
	ListRunEvents(context.Context, domain.RunID, int64, int) ([]domain.Event, bool, error)
	ListEvents(context.Context, string, string) ([]domain.Event, error)
	GetIdempotency(context.Context, string, string) (IdempotencyRecord, error)
	GetTimeline(context.Context, domain.TimelineID) (domain.Timeline, error)
	ListTimelineAncestry(context.Context, domain.TimelineID) ([]domain.Timeline, error)
	GetMoment(context.Context, domain.MomentID) (domain.Moment, error)
	ListMoments(context.Context, domain.TimelineID, Page) ([]domain.Moment, bool, error)
	LatestMoment(context.Context, domain.TimelineID) (domain.Moment, error)
	GetSetupMomentCache(context.Context, domain.ProjectID, string) (domain.SetupMomentCache, error)
	HasActiveRun(context.Context, domain.CapsuleID) (bool, error)
	GetThread(context.Context, domain.ThreadID) (domain.Thread, error)
	GetActiveThread(context.Context, domain.CapsuleID) (domain.Thread, error)
	ListThreads(context.Context, domain.CapsuleID, Page) ([]domain.Thread, bool, error)
	ListRecoverableThreads(context.Context) ([]domain.Thread, error)
	ListThreadMessages(context.Context, domain.ThreadID, int64, int) ([]domain.ThreadMessage, bool, error)
	GetThreadDelivery(context.Context, domain.ThreadMessageID) (domain.ThreadDelivery, error)
	FindUndeliveredUserMessage(context.Context, domain.ThreadID) (domain.ThreadMessage, error)
	GetProjectThreadIntent(context.Context, domain.ProjectThreadIntentID) (domain.ProjectThreadIntent, error)
	GetProjectThreadIntentByKey(context.Context, domain.ProjectID, string) (domain.ProjectThreadIntent, error)
	ListProvisioningProjectThreadIntents(context.Context) ([]domain.ProjectThreadIntent, error)
	GetDelivery(context.Context, domain.DeliveryID) (domain.Delivery, error)
	GetDeliveryByIdempotency(context.Context, domain.CapsuleID, string) (domain.Delivery, error)
	ListDeliveries(context.Context, domain.CapsuleID, Page) ([]domain.Delivery, bool, error)
	ListRecoverableDeliveries(context.Context) ([]domain.Delivery, error)
	GetCoordinationGrant(context.Context, domain.CoordinationGrantID) (domain.CoordinationGrant, error)
	GetCoordinationGrantByRootThread(context.Context, domain.ThreadID) (domain.CoordinationGrant, error)
	GetCoordinationGrantByRootCapsule(context.Context, domain.CapsuleID) (domain.CoordinationGrant, error)
	ListActiveCoordinationGrants(ctx context.Context, stoppedSince time.Time) ([]domain.CoordinationGrant, error)
	GetThreadLink(context.Context, domain.ThreadLinkID) (domain.ThreadLink, error)
	GetThreadLinkByChildThread(context.Context, domain.ThreadID) (domain.ThreadLink, error)
	ListThreadLinksByParent(context.Context, domain.ThreadID) ([]domain.ThreadLink, error)
	ListThreadLinksByGrant(context.Context, domain.CoordinationGrantID) ([]domain.ThreadLink, error)
	ListArchivableThreadLinks(context.Context, time.Time, int) ([]domain.ThreadLink, error)
	GetCoordinationRequest(context.Context, domain.CapsuleID, string) (domain.CoordinationRequest, error)
	GetCoordinationRequestByID(context.Context, domain.CoordinationRequestID) (domain.CoordinationRequest, error)
	ListCoordinationRequests(context.Context, domain.CoordinationRequestState) ([]domain.CoordinationRequest, error)
}

type Transaction interface {
	PutPreparationPolicy(context.Context, domain.ProjectID, domain.PreparationPolicy) error
	PutPreparation(context.Context, domain.Preparation) error
	CollectDeletedHarnessSetups(context.Context) error
	SetProjectHarnessSetup(context.Context, domain.ProjectID, string, string) error
	PutProviderConnection(context.Context, domain.ProviderConnection) error
	GrantProviderConnection(context.Context, domain.ProjectID, string, string) error
	PutHarnessSetup(context.Context, domain.HarnessSetup) error
	InsertHarnessSetupRevision(context.Context, domain.HarnessSetupRevision) error
	PinHarnessSetup(context.Context, domain.CapsuleID, string, string) error
	Reader
	InsertProject(context.Context, domain.Project) error
	UpdateProject(context.Context, domain.Project, domain.ResourceVersion) error
	UpdateProjectState(context.Context, domain.Project, domain.ResourceVersion) error
	PurgeProjectContents(context.Context, domain.Project, time.Time) (domain.Project, []string, error)
	InsertSecret(context.Context, domain.Secret) error
	UpdateSecret(context.Context, domain.Secret, domain.ResourceVersion) error
	DeleteSecret(context.Context, string, domain.ResourceVersion) error
	InsertCapsule(context.Context, domain.Capsule) error
	UpdateCapsule(context.Context, domain.Capsule, domain.ResourceVersion) error
	TouchCapsuleActivity(context.Context, domain.CapsuleID, time.Time) error
	InsertTimeline(context.Context, domain.Timeline) error
	InsertMoment(context.Context, domain.Moment) error
	PutSetupMomentCache(context.Context, domain.SetupMomentCache) error
	InsertRun(context.Context, domain.Run) error
	UpdateRun(context.Context, domain.Run, domain.ResourceVersion) error
	AppendEvent(context.Context, domain.Event) error
	PutIdempotency(context.Context, IdempotencyRecord) error
	InsertThread(context.Context, domain.Thread) error
	UpdateThread(context.Context, domain.Thread, domain.ResourceVersion) error
	AppendThreadMessage(context.Context, domain.ThreadMessage) error
	InsertThreadDelivery(context.Context, domain.ThreadDelivery) error
	AcknowledgeThreadDelivery(context.Context, domain.ThreadMessageID, time.Time) error
	RewrapThreadKey(
		context.Context,
		domain.ThreadID,
		domain.ResourceVersion,
		[]byte,
		string,
		uint32,
		time.Time,
	) error
	CryptoShredThread(context.Context, domain.ThreadID, domain.ResourceVersion, time.Time) error
	InsertProjectThreadIntent(context.Context, domain.ProjectThreadIntent) error
	UpdateProjectThreadIntent(
		context.Context,
		domain.ProjectThreadIntent,
		domain.ResourceVersion,
	) error
	InsertDelivery(context.Context, domain.Delivery) error
	UpdateDelivery(context.Context, domain.Delivery, domain.ResourceVersion) error
	ApplyDeliveryApproval(context.Context, domain.Delivery, domain.ResourceVersion) error
	InsertCoordinationGrant(context.Context, domain.CoordinationGrant) error
	UpdateCoordinationGrant(context.Context, domain.CoordinationGrant, domain.ResourceVersion) error
	InsertThreadLink(context.Context, domain.ThreadLink) error
	UpdateThreadLink(context.Context, domain.ThreadLink, domain.ResourceVersion) error
	PromoteCapsule(context.Context, domain.Capsule, domain.ResourceVersion) error
	InsertCoordinationRequest(context.Context, domain.CoordinationRequest) error
	UpdateCoordinationRequest(context.Context, domain.CoordinationRequest, domain.ResourceVersion) error
}

type Store interface {
	View(context.Context, func(Reader) error) error
	Transact(context.Context, func(Transaction) error) error
	Close() error
}

type IdempotencyRecord struct {
	Scope     string
	Key       string
	Outcome   []byte
	CreatedAt time.Time
}

type Clock interface {
	Now() time.Time
}

type IDSource interface {
	NewID() string
}

type ReconcileQueue interface {
	Enqueue(context.Context, domain.CapsuleID) error
}

// Observer receives only bounded operational dimensions. Implementations must
// not add object identifiers, repository data, paths, content, or error text.
type Observer interface {
	ObserveReconcile(time.Duration, string)
	ProviderOperation(string, string, string, time.Duration)
	Retry(string)
	Cleanup(string, string)
	RunTransition(string, string)
	EventBackpressure(string)
	EventGap()
	Snapshot(time.Duration, int64, string)
	ArtifactFailure(string)
}

type ProviderCapabilities struct {
	Version    string
	Attach     bool
	Run        bool
	Git        bool
	Pause      bool
	Snapshot   bool
	Clone      bool
	Preview    bool
	Structured bool
	Browse     bool
	Delivery   bool
	// Coordination reports a Capsule-local mailbox for lead Threads.
	Coordination bool
}

type RuntimeRunRequest struct {
	Connection *harnesssetup.Connection   `json:"connection,omitempty"`
	Setup      *harnesssetup.RuntimeSetup `json:"setup,omitempty"`
	RunID      domain.RunID
	ResourceID string
	Harness    string
	Prompt     string
	Columns    uint16
	Rows       uint16
	Secrets    map[string]string
}

type RuntimeRun struct {
	State      domain.RunState
	ExitStatus int
	HasExit    bool
	Failure    string
	Cursor     uint64
	PTY        bool
	Structured bool
	// Models says the structured adapter negotiated models frames.
	Models bool
	// Effort says it also negotiated reasoning effort.
	Effort bool
}

type RuntimeStructuredStartRequest struct {
	Connection *harnesssetup.Connection   `json:"connection,omitempty"`
	Setup      *harnesssetup.RuntimeSetup `json:"setup,omitempty"`
	RunID      domain.RunID
	ResourceID string
	Harness    string
	Frame      adapterproto.Frame
	Secrets    map[string]string
}

type RuntimeStructuredSendRequest struct {
	ResourceID string
	RunID      domain.RunID
	Frame      adapterproto.Frame
}

type RuntimeStructuredEvent struct {
	Sequence uint64
	Frame    adapterproto.Frame
}

type RuntimeStructuredEvents struct {
	Items      []RuntimeStructuredEvent
	NextCursor uint64
	Gap        bool
}

type RuntimeHarnessProfile struct {
	Name        string
	Structured  bool
	AdapterKind string
	Protocol    string
	PTY         bool
}

type RuntimeEvent struct {
	Sequence uint64
	Type     string
	Metadata []byte
}

type RuntimeEvents struct {
	Items      []RuntimeEvent
	NextCursor uint64
	Gap        bool
}

type GitResult struct {
	Content   string
	Truncated bool
}

// GitPatchRequest asks capsuled for a bounded format-patch plus working-tree
// diff, or for content-free git metadata when IncludePatch is false.
type GitPatchRequest struct {
	BaseCommit   string
	MaxBytes     int
	IncludePatch bool
}

type GitPatchResult struct {
	Patch        string
	Truncated    bool
	Head         string
	BaseCommit   string
	FilesChanged int
	Insertions   int
	Deletions    int
}

type WorkspaceFileEntry struct {
	Name       string
	Type       string
	Size       int64
	Executable bool
}

type WorkspaceFilePage struct {
	Path      string
	Items     []WorkspaceFileEntry
	NextAfter string
}

type WorkspaceFile struct {
	Path       string
	Content    []byte
	Size       int64
	Executable bool
}

// WorkspaceBrowser is optional and exposes only the bounded, supervisor-owned
// browser. Implementations must not access provider filesystems directly.
type WorkspaceBrowser interface {
	ListWorkspaceFiles(context.Context, string, string, string, int) (WorkspaceFilePage, error)
	ReadWorkspaceFile(context.Context, string, string) (WorkspaceFile, error)
}

type DeliveryInspection struct {
	CapsuleResourceVersion domain.ResourceVersion
	HEAD                   string
	Branch                 string
	Dirty                  bool
	OriginURL              string
	DefaultBranch          string
	Tree                   string
}

type DeliveryCommitRequest struct {
	ResourceID   string
	Message      string
	AuthorName   string
	AuthorEmail  string
	ExpectedHEAD string
	ExpectedTree string
}

type DeliveryCommitResult struct {
	Commit string
	Tree   string
}

type DeliveryPushRequest struct {
	ResourceID     string
	SourceCommit   string
	DestinationRef string
	ExpectedOldRef *string
	GitCredential  GitHTTPSCredential
}

type DeliveryPushResult struct {
	Commit         string
	DestinationRef string
}

// DeliveryRuntime is optional. Credentials are supplied for one exact push
// operation and must never be retained by an implementation.
type DeliveryRuntime interface {
	InspectDelivery(context.Context, string) (DeliveryInspection, error)
	CommitDelivery(context.Context, DeliveryCommitRequest) (DeliveryCommitResult, error)
	PushDelivery(context.Context, DeliveryPushRequest) (DeliveryPushResult, error)
}

type RuntimeAttachment interface {
	Read(context.Context) (binary bool, value []byte, err error)
	Write(ctx context.Context, binary bool, value []byte) error
	Close() error
}

// CapsuleRuntime is deliberately separate from CapsuleProvider lifecycle.
type CapsuleRuntime interface {
	StartRun(context.Context, RuntimeRunRequest) (RuntimeRun, error)
	GetRun(context.Context, string, domain.RunID) (RuntimeRun, error)
	CancelRun(context.Context, string, domain.RunID) (RuntimeRun, error)
	RunEvents(context.Context, string, domain.RunID, uint64) (RuntimeEvents, error)
	AttachRun(context.Context, string, domain.RunID, uint64) (RuntimeAttachment, error)
	GitStatus(context.Context, string) (GitResult, error)
	GitDiff(context.Context, string) (GitResult, error)
	GitPatch(context.Context, string, GitPatchRequest) (GitPatchResult, error)
}

// HarnessProfileRuntime exposes validated Capsule-local profile metadata
// independently from either native or structured session execution.
type HarnessProfileRuntime interface {
	HarnessProfiles(context.Context, string) ([]RuntimeHarnessProfile, error)
}

// StructuredRuntime is optional and deliberately separate from PTY attachment.
// Frames retain their typed metadata for the later application/API boundary;
// callers must not copy frame content into lifecycle events or logs.
type StructuredRuntime interface {
	HarnessProfileRuntime
	StartStructured(context.Context, RuntimeStructuredStartRequest) (RuntimeRun, error)
	GetStructured(context.Context, string, domain.RunID) (RuntimeRun, error)
	SendStructured(context.Context, RuntimeStructuredSendRequest) error
	StructuredEvents(context.Context, string, domain.RunID, uint64) (RuntimeStructuredEvents, error)
	CancelStructured(context.Context, string, domain.RunID) (RuntimeRun, error)
}

// CoordinationRuntime is optional. It relays the Capsule-local coordination
// mailbox; meridiand always pulls, and the Capsule never reaches the public
// API or holds a credential.
type CoordinationRuntime interface {
	ConfigureCoordination(context.Context, string, coordproto.Config) error
	PollCoordination(context.Context, string, uint64, uint64, time.Duration) (coordproto.Poll, error)
	ReplyCoordination(context.Context, string, []coordproto.Reply) error
	NotifyCoordination(context.Context, string, coordproto.Notification) error
}

type PreviewPort struct {
	Port uint16
}

type PreviewResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// PreviewRuntime is an optional, deliberately narrow Capsule-local ingress
// boundary. Implementations must resolve only the supplied owned resource and
// must connect only to the requested port inside that resource.
type PreviewRuntime interface {
	DiscoverPreviewPorts(context.Context, string) ([]PreviewPort, error)
	ForwardPreviewHTTP(context.Context, string, uint16, *http.Request) (PreviewResponse, error)
	AttachPreview(context.Context, string, uint16, string, http.Header) (RuntimeAttachment, string, error)
}

type SnapshotMetadata struct {
	ImageDigest     string
	GitBranch       string
	GitHEAD         string
	GitDirtySummary string
}

type WorkspaceCapture struct {
	Archive  io.ReadCloser
	Metadata SnapshotMetadata
}

// WorkspaceSnapshotter is deliberately separate from lifecycle and Run APIs.
type WorkspaceSnapshotter interface {
	CaptureWorkspace(context.Context, string) (WorkspaceCapture, error)
	RestoreWorkspace(context.Context, string, string, int64, io.Reader) error
}

type PublishedArtifact struct {
	Digest string
	Size   int64
}

type ArtifactStore interface {
	Publish(context.Context, io.Reader) (PublishedArtifact, error)
	Open(context.Context, string) (io.ReadCloser, int64, error)
}

type ProviderState string

const (
	ProviderPreparing ProviderState = "preparing"
	ProviderReady     ProviderState = "ready"
	ProviderPaused    ProviderState = "paused"
	ProviderDeleted   ProviderState = "deleted"
)

type ProviderResource struct {
	ID          string
	State       ProviderState
	ImageDigest string
}

type CreateCapsuleRequest struct {
	ForcePreparation bool
	CapsuleID        domain.CapsuleID
	RepositoryURL    string
	Setup            []string
	ImageReference   string
	Restore          bool
	GitCredential    *GitHTTPSCredential
}

type GitHTTPSCredential struct {
	Username string
	Password string
}

type CapsuleProvider interface {
	Capabilities(context.Context) (ProviderCapabilities, error)
	Create(context.Context, CreateCapsuleRequest) (ProviderResource, error)
	Get(context.Context, string) (ProviderResource, error)
	Pause(context.Context, string) (ProviderResource, error)
	Resume(context.Context, string) (ProviderResource, error)
	Delete(context.Context, string) error
}

// WorkspacePreparer separates source checkout from dependency setup. Calls execute
// only inside the provider-owned Capsule and never receive harness credentials.
type WorkspacePreparer interface {
	PreparationIdentity(context.Context, string) (PreparationIdentity, error)
	FinishPreparation(context.Context, string, []string, string, string) error
}
type PreparationIdentity struct {
	SourceRevision string
	Platform       string
}
