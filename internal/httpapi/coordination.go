package httpapi

import (
	"net/http"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

type coordinationPolicyJSON struct {
	MaxDepth           int      `json:"maxDepth,omitempty"`
	MaxChildren        int      `json:"maxChildren,omitempty"`
	MaxDescendants     int      `json:"maxDescendants,omitempty"`
	MaxLiveCapsules    int      `json:"maxLiveCapsules,omitempty"`
	MaxMessagesPerLink int      `json:"maxMessagesPerLink,omitempty"`
	AllowedHarnesses   []string `json:"allowedHarnesses,omitempty"`
	DeadlineSeconds    int64    `json:"deadlineSeconds,omitempty"`
	// ArchiveAfterSeconds is a pointer because 0 disables auto-archive.
	ArchiveAfterSeconds *int64 `json:"archiveAfterSeconds,omitempty"`
	OnDeadline          string `json:"onDeadline,omitempty"`
	SpawnApproval       string `json:"spawnApproval,omitempty"`
}

func (p *coordinationPolicyJSON) policy() *domain.CoordinationPolicy {
	if p == nil {
		return nil
	}
	var archiveAfter *time.Duration
	if p.ArchiveAfterSeconds != nil {
		// Out-of-range values become negative so Normalize rejects them
		// instead of overflowing.
		value := time.Duration(-1)
		if seconds := *p.ArchiveAfterSeconds; seconds >= 0 &&
			seconds <= int64(domain.CoordinationMaxArchiveAfter/time.Second) {
			value = time.Duration(seconds) * time.Second
		}
		archiveAfter = &value
	}
	return &domain.CoordinationPolicy{
		ArchiveAfter: archiveAfter,
		MaxDepth:     p.MaxDepth, MaxChildren: p.MaxChildren, MaxDescendants: p.MaxDescendants,
		MaxLiveCapsules: p.MaxLiveCapsules, MaxMessagesPerLink: p.MaxMessagesPerLink,
		AllowedHarnesses: p.AllowedHarnesses, Deadline: time.Duration(p.DeadlineSeconds) * time.Second,
		OnDeadline: domain.OnDeadline(p.OnDeadline), SpawnApproval: domain.SpawnApproval(p.SpawnApproval),
	}
}

type coordinationCapabilityJSON struct {
	Supported      bool `json:"supported"`
	MaxDepth       int  `json:"maxDepth"`
	MaxDescendants int  `json:"maxDescendants"`
}

type coordinationGrantJSON struct {
	ID                  string   `json:"id"`
	ProjectID           string   `json:"projectId"`
	RootThreadID        string   `json:"rootThreadId"`
	RootCapsuleID       string   `json:"rootCapsuleId"`
	State               string   `json:"state"`
	SpawnApproval       string   `json:"spawnApproval"`
	MaxDepth            int      `json:"maxDepth"`
	MaxChildren         int      `json:"maxChildren"`
	MaxDescendants      int      `json:"maxDescendants"`
	MaxLiveCapsules     int      `json:"maxLiveCapsules"`
	MaxMessagesPerLink  int      `json:"maxMessagesPerLink"`
	AllowedHarnesses    []string `json:"allowedHarnesses"`
	DeadlineAt          string   `json:"deadlineAt"`
	OnDeadline          string   `json:"onDeadline"`
	ArchiveAfterSeconds int64    `json:"archiveAfterSeconds"`
	DescendantsCreated  int      `json:"descendantsCreated"`
	RevokedAt           string   `json:"revokedAt,omitempty"`
	RevokeReason        string   `json:"revokeReason,omitempty"`
	CreatedAt           string   `json:"createdAt"`
	UpdatedAt           string   `json:"updatedAt"`
	ResourceVersion     int64    `json:"resourceVersion"`
}

type threadLinkJSON struct {
	ID                 string `json:"id"`
	GrantID            string `json:"grantId"`
	ParentThreadID     string `json:"parentThreadId"`
	ParentCapsuleID    string `json:"parentCapsuleId"`
	ChildThreadID      string `json:"childThreadId"`
	ChildCapsuleID     string `json:"childCapsuleId"`
	ChildIntentID      string `json:"childIntentId"`
	Depth              int    `json:"depth"`
	Name               string `json:"name"`
	Harness            string `json:"harness"`
	State              string `json:"state"`
	CloseReason        string `json:"closeReason,omitempty"`
	Model              string `json:"model,omitempty"`
	Effort             string `json:"effort,omitempty"`
	Phase              string `json:"phase"`
	ReportSeq          int    `json:"reportSeq"`
	ReportDeliveredSeq int    `json:"reportDeliveredSeq"`
	LastReportStatus   string `json:"lastReportStatus,omitempty"`
	LastReportReason   string `json:"lastReportReason,omitempty"`
	LastReportAt       string `json:"lastReportAt,omitempty"`
	MessagesForwarded  int    `json:"messagesForwarded"`
	OperatorMessages   int    `json:"operatorMessages"`
	ArchivedAt         string `json:"archivedAt,omitempty"`
	PromotedAt         string `json:"promotedAt,omitempty"`
	CapsuleRemoved     bool   `json:"capsuleRemoved,omitempty"`
	BaseCommit         string `json:"baseCommit,omitempty"`
	Head               string `json:"head,omitempty"`
	FilesChanged       int    `json:"filesChanged,omitempty"`
	Insertions         int    `json:"insertions,omitempty"`
	Deletions          int    `json:"deletions,omitempty"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
	ResourceVersion    int64  `json:"resourceVersion"`
}

type threadLinkPageJSON struct {
	Items []threadLinkJSON `json:"items"`
}

type promoteCapsuleRequest struct {
	ExpectedResourceVersion int64 `json:"expectedResourceVersion"`
	Detach                  bool  `json:"detach,omitempty"`
}

type revokeCoordinationGrantRequest struct {
	ExpectedResourceVersion int64 `json:"expectedResourceVersion"`
	CancelDescendants       *bool `json:"cancelDescendants,omitempty"`
}

type updateCoordinationGrantRequest struct {
	ExpectedResourceVersion int64     `json:"expectedResourceVersion"`
	SpawnApproval           *string   `json:"spawnApproval"`
	MaxChildren             *int      `json:"maxChildren"`
	MaxDescendants          *int      `json:"maxDescendants"`
	MaxLiveCapsules         *int      `json:"maxLiveCapsules"`
	MaxMessagesPerLink      *int      `json:"maxMessagesPerLink"`
	AllowedHarnesses        *[]string `json:"allowedHarnesses"`
	DeadlineSeconds         *int64    `json:"deadlineSeconds"`
	OnDeadline              *string   `json:"onDeadline"`
	ArchiveAfterSeconds     *int64    `json:"archiveAfterSeconds"`
}

func (p updateCoordinationGrantRequest) patch() domain.CoordinationGrantPatch {
	patch := domain.CoordinationGrantPatch{
		MaxChildren: p.MaxChildren, MaxDescendants: p.MaxDescendants,
		MaxLiveCapsules: p.MaxLiveCapsules, MaxMessagesPerLink: p.MaxMessagesPerLink,
		AllowedHarnesses: p.AllowedHarnesses,
	}
	if p.SpawnApproval != nil {
		value := domain.SpawnApproval(*p.SpawnApproval)
		patch.SpawnApproval = &value
	}
	if p.OnDeadline != nil {
		value := domain.OnDeadline(*p.OnDeadline)
		patch.OnDeadline = &value
	}
	if p.DeadlineSeconds != nil {
		value := time.Duration(-1)
		if seconds := *p.DeadlineSeconds; seconds >= 0 &&
			seconds <= int64(domain.CoordinationMaxDeadline/time.Second) {
			value = time.Duration(seconds) * time.Second
		}
		patch.Deadline = &value
	}
	if p.ArchiveAfterSeconds != nil {
		value := time.Duration(-1)
		if seconds := *p.ArchiveAfterSeconds; seconds >= 0 &&
			seconds <= int64(domain.CoordinationMaxArchiveAfter/time.Second) {
			value = time.Duration(seconds) * time.Second
		}
		patch.ArchiveAfter = &value
	}
	return patch
}

type decideCoordinationRequest struct {
	ExpectedResourceVersion int64 `json:"expectedResourceVersion"`
}

type coordinationRequestJSON struct {
	ID              string `json:"id"`
	GrantID         string `json:"grantId"`
	CapsuleID       string `json:"capsuleId"`
	Key             string `json:"key"`
	Kind            string `json:"kind"`
	LinkID          string `json:"linkId"`
	State           string `json:"state"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	ResourceVersion int64  `json:"resourceVersion"`
}

type coordinationRequestPageJSON struct {
	Items []coordinationRequestJSON `json:"items"`
}

func requestResponse(request domain.CoordinationRequest) coordinationRequestJSON {
	return coordinationRequestJSON{
		ID: string(request.ID), GrantID: string(request.GrantID), CapsuleID: string(request.CapsuleID),
		Key: request.Key, Kind: string(request.Kind), LinkID: string(request.LinkID),
		State: string(request.State), CreatedAt: request.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: request.UpdatedAt.UTC().Format(time.RFC3339Nano), ResourceVersion: int64(request.ResourceVersion),
	}
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func grantResponse(grant domain.CoordinationGrant) coordinationGrantJSON {
	harnesses := grant.AllowedHarnesses
	if harnesses == nil {
		harnesses = []string{}
	}
	return coordinationGrantJSON{
		ID: string(grant.ID), ProjectID: string(grant.ProjectID), RootThreadID: string(grant.RootThreadID),
		RootCapsuleID: string(grant.RootCapsuleID), State: string(grant.State),
		SpawnApproval: string(grant.SpawnApproval), MaxDepth: grant.MaxDepth,
		MaxChildren: grant.MaxChildren, MaxDescendants: grant.MaxDescendants,
		MaxLiveCapsules: grant.MaxLiveCapsules, MaxMessagesPerLink: grant.MaxMessagesPerLink,
		AllowedHarnesses: harnesses, DeadlineAt: grant.DeadlineAt.UTC().Format(time.RFC3339Nano),
		OnDeadline: string(grant.OnDeadline), ArchiveAfterSeconds: int64(grant.ArchiveAfter / time.Second),
		DescendantsCreated: grant.DescendantsCreated,
		RevokedAt:          formatOptionalTime(grant.RevokedAt), RevokeReason: string(grant.RevokeReason),
		CreatedAt:       grant.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       grant.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ResourceVersion: int64(grant.ResourceVersion),
	}
}

func (s *Server) linkResponse(request *http.Request, link domain.ThreadLink) threadLinkJSON {
	models, _ := s.service.ThreadLinkModels(request.Context(), link)
	return threadLinkJSON{
		Model: models.Model, Effort: models.Effort,
		ID: string(link.ID), GrantID: string(link.GrantID), ParentThreadID: string(link.ParentThreadID),
		ParentCapsuleID: string(link.ParentCapsuleID), ChildThreadID: string(link.ChildThreadID),
		ChildCapsuleID: string(link.ChildCapsuleID), ChildIntentID: string(link.ChildIntentID),
		Depth: link.Depth, Name: link.Name, Harness: link.Harness, State: string(link.State),
		CloseReason: string(link.CloseReason), Phase: s.service.ThreadLinkPhase(request.Context(), link),
		ReportSeq: link.ReportSeq, ReportDeliveredSeq: link.ReportDeliveredSeq,
		LastReportStatus: string(link.LastReportStatus), LastReportReason: string(link.LastReportReason),
		LastReportAt: formatOptionalTime(link.LastReportAt), MessagesForwarded: link.MessagesForwarded,
		OperatorMessages: link.OperatorMessages,
		ArchivedAt:       formatOptionalTime(link.ArchivedAt),
		PromotedAt:       formatOptionalTime(link.PromotedAt),
		CapsuleRemoved:   s.service.ThreadLinkCapsuleRemoved(request.Context(), link),
		BaseCommit:       link.BaseCommit, Head: link.Head, FilesChanged: link.FilesChanged,
		Insertions: link.Insertions, Deletions: link.Deletions,
		CreatedAt:       link.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:       link.UpdatedAt.UTC().Format(time.RFC3339Nano),
		ResourceVersion: int64(link.ResourceVersion),
	}
}

func (s *Server) coordinationCapability() *coordinationCapabilityJSON {
	if s.service == nil || !s.capabilities.Coordination || !s.service.CoordinationSupported() {
		return &coordinationCapabilityJSON{}
	}
	return &coordinationCapabilityJSON{
		Supported: true, MaxDepth: domain.CoordinationMaxDepth,
		MaxDescendants: domain.CoordinationMaxDescendants,
	}
}

func (s *Server) getCoordinationGrant(writer http.ResponseWriter, request *http.Request) {
	grant, err := s.service.GetCoordinationGrant(
		request.Context(), domain.CoordinationGrantID(request.PathValue("grantId")))
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(grant.ResourceVersion))
	writeJSON(writer, http.StatusOK, grantResponse(grant))
}

func (s *Server) updateCoordinationGrant(writer http.ResponseWriter, request *http.Request) {
	var input updateCoordinationGrantRequest
	if !decode(writer, request, &input) {
		return
	}
	grant, err := s.service.UpdateCoordinationGrant(
		request.Context(), domain.CoordinationGrantID(request.PathValue("grantId")),
		domain.ResourceVersion(input.ExpectedResourceVersion), input.patch(),
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(grant.ResourceVersion))
	writeJSON(writer, http.StatusOK, grantResponse(grant))
}

func (s *Server) listCoordinationRequests(writer http.ResponseWriter, request *http.Request) {
	requests, err := s.service.ListCoordinationRequests(
		request.Context(), domain.CoordinationRequestState(request.URL.Query().Get("state")))
	if err != nil {
		writeError(writer, err)
		return
	}
	response := coordinationRequestPageJSON{Items: make([]coordinationRequestJSON, 0, len(requests))}
	for _, item := range requests {
		response.Items = append(response.Items, requestResponse(item))
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) approveCoordinationRequest(writer http.ResponseWriter, request *http.Request) {
	s.decideCoordinationRequest(writer, request, true)
}

func (s *Server) denyCoordinationRequest(writer http.ResponseWriter, request *http.Request) {
	s.decideCoordinationRequest(writer, request, false)
}

func (s *Server) decideCoordinationRequest(writer http.ResponseWriter, request *http.Request, approve bool) {
	var input decideCoordinationRequest
	if !decode(writer, request, &input) {
		return
	}
	id := domain.CoordinationRequestID(request.PathValue("requestId"))
	key := request.Header.Get("Idempotency-Key")
	expected := domain.ResourceVersion(input.ExpectedResourceVersion)
	var result domain.CoordinationRequest
	var err error
	if approve {
		result, err = s.service.ApproveCoordinationRequest(request.Context(), id, expected, key)
	} else {
		result, err = s.service.DenyCoordinationRequest(request.Context(), id, expected, key)
	}
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(result.ResourceVersion))
	writeJSON(writer, http.StatusOK, requestResponse(result))
}

func (s *Server) revokeCoordinationGrant(writer http.ResponseWriter, request *http.Request) {
	var input revokeCoordinationGrantRequest
	if !decode(writer, request, &input) {
		return
	}
	cancelDescendants := input.CancelDescendants == nil || *input.CancelDescendants
	grant, err := s.service.RevokeCoordinationGrant(
		request.Context(), domain.CoordinationGrantID(request.PathValue("grantId")),
		domain.ResourceVersion(input.ExpectedResourceVersion), cancelDescendants,
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(grant.ResourceVersion))
	writeJSON(writer, http.StatusOK, grantResponse(grant))
}

// removeSubagent stops one subagent and deletes its Capsule without touching
// its lead's Coordination Grant.
func (s *Server) removeSubagent(writer http.ResponseWriter, request *http.Request) {
	var input lifecycleRequest
	if !decode(writer, request, &input) {
		return
	}
	link, err := s.service.RemoveSubagent(
		request.Context(), domain.ThreadLinkID(request.PathValue("linkId")),
		domain.ResourceVersion(input.ExpectedResourceVersion), request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	s.previews.revokeCapsule(link.ChildCapsuleID)
	writer.Header().Set("ETag", etag(link.ResourceVersion))
	writeJSON(writer, http.StatusAccepted, s.linkResponse(request, link))
}

func (s *Server) promoteCapsule(writer http.ResponseWriter, request *http.Request) {
	var input promoteCapsuleRequest
	if !decode(writer, request, &input) {
		return
	}
	capsule, err := s.service.PromoteCapsule(
		request.Context(), domain.CapsuleID(request.PathValue("capsuleId")),
		domain.ResourceVersion(input.ExpectedResourceVersion), input.Detach,
		request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(writer, err)
		return
	}
	s.writeCapsule(writer, request, http.StatusOK, capsule)
}

func (s *Server) listThreadLinks(writer http.ResponseWriter, request *http.Request) {
	links, err := s.service.ListThreadLinks(request.Context(), domain.ThreadID(request.PathValue("threadId")))
	if err != nil {
		writeError(writer, err)
		return
	}
	response := threadLinkPageJSON{Items: make([]threadLinkJSON, 0, len(links))}
	for _, link := range links {
		response.Items = append(response.Items, s.linkResponse(request, link))
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) getThreadLink(writer http.ResponseWriter, request *http.Request) {
	link, err := s.service.GetThreadLink(request.Context(), domain.ThreadLinkID(request.PathValue("linkId")))
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.Header().Set("ETag", etag(link.ResourceVersion))
	writeJSON(writer, http.StatusOK, s.linkResponse(request, link))
}
