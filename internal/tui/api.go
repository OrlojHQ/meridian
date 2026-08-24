package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/OrlojHQ/meridian/pkg/client"
)

// API is the narrow transport boundary used by the dashboard model.
type API interface {
	Snapshot(context.Context) (Snapshot, error)
	Execute(context.Context, ActionRequest) (ActionResult, error)
}

type Snapshot struct {
	Projects []client.Project
	Capsules []CapsuleDetail
	LoadedAt time.Time
}

type CapsuleDetail struct {
	Project  client.Project
	Capsule  client.Capsule
	Profiles []client.HarnessProfile
	Threads  []ThreadDetail
	Runs     []client.Run
	Events   []client.RunEvent
	Moments  []client.Moment
	Timeline *client.TimelineView
}

// ThreadDetail keeps decrypted transcript content at the generated API
// boundary. It never exposes persistence or provider internals to the model.
type ThreadDetail struct {
	Thread          client.Thread
	Blocks          []client.ThreadBlock
	Cursor          int64
	More            bool
	Truncated       bool
	Gap             *client.ThreadAdapterEvent
	TranscriptCode  string
	TranscriptError string
}

func (d CapsuleDetail) LatestRun() *client.Run {
	if len(d.Runs) == 0 {
		return nil
	}
	return &d.Runs[0]
}

func (d CapsuleDetail) ActiveRun() *client.Run {
	for index := range d.Runs {
		switch d.Runs[index].State {
		case client.RunStateQueued, client.RunStateStarting, client.RunStateRunning, client.RunStateCancelling:
			return &d.Runs[index]
		}
	}
	return nil
}

func (d CapsuleDetail) LatestMoment() *client.Moment {
	if len(d.Moments) == 0 {
		return nil
	}
	return &d.Moments[0]
}

type Action string

const (
	ActionCreate Action = "create"
	ActionPause  Action = "pause"
	ActionResume Action = "resume"
	ActionDiff   Action = "diff"
	ActionMoment Action = "moment"
	ActionShard  Action = "shard"
	ActionRewind Action = "rewind"
	ActionSeal   Action = "seal"
	ActionDelete Action = "delete"

	ActionThreadCreate  Action = "thread-create"
	ActionThreadStart   Action = "thread-start"
	ActionThreadResume  Action = "thread-resume"
	ActionThreadSend    Action = "thread-send"
	ActionThreadRespond Action = "thread-respond"
	ActionThreadCancel  Action = "thread-cancel"
	ActionThreadArchive Action = "thread-archive"
	ActionThreadDelete  Action = "thread-delete"
)

type ActionRequest struct {
	Action          Action
	ProjectID       string
	CapsuleID       string
	Name            string
	MomentID        string
	ResourceVersion int64
	ThreadID        string
	Harness         string
	Content         string
	ResponseTo      string
	Choice          string
	Input           string
	Start           bool
}

type ActionResult struct {
	Message  string
	Content  string
	ThreadID string
}

type generatedAPI struct {
	client *client.Client
}

func NewAPI(server string) (API, error) {
	value, err := client.NewClient(server)
	if err != nil {
		return nil, err
	}
	return &generatedAPI{client: value}, nil
}

func (a *generatedAPI) Snapshot(ctx context.Context) (Snapshot, error) {
	projects, err := a.projects(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{Projects: projects, LoadedAt: time.Now()}
	for _, project := range projects {
		capsules, err := a.capsules(ctx, project)
		if err != nil {
			return Snapshot{}, err
		}
		for index := range capsules {
			detail, err := a.detail(ctx, project, capsules[index])
			if err != nil {
				return Snapshot{}, err
			}
			result.Capsules = append(result.Capsules, detail)
		}
	}
	return result, nil
}

func (a *generatedAPI) projects(ctx context.Context) ([]client.Project, error) {
	var output []client.Project
	var cursor string
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		params := client.ListProjectsParams{Limit: client.NewOptInt(100)}
		if cursor != "" {
			params.Cursor = client.NewOptString(cursor)
		}
		response, err := a.client.ListProjects(ctx, params)
		if err != nil {
			return nil, transportError(err)
		}
		page, ok := response.(*client.ProjectPage)
		if !ok {
			return nil, responseError(response)
		}
		output = append(output, page.Items...)
		next, more := page.NextCursor.Get()
		if !more || next == "" {
			return output, nil
		}
		cursor = next
	}
	return nil, errors.New("project pagination limit exceeded")
}

func (a *generatedAPI) capsules(ctx context.Context, project client.Project) ([]client.Capsule, error) {
	var output []client.Capsule
	var cursor string
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		params := client.ListCapsulesParams{ProjectId: project.ID, Limit: client.NewOptInt(100)}
		if cursor != "" {
			params.Cursor = client.NewOptString(cursor)
		}
		response, err := a.client.ListCapsules(ctx, params)
		if err != nil {
			return nil, transportError(err)
		}
		page, ok := response.(*client.CapsulePage)
		if !ok {
			return nil, responseError(response)
		}
		output = append(output, page.Items...)
		next, more := page.NextCursor.Get()
		if !more || next == "" {
			return output, nil
		}
		cursor = next
	}
	return nil, errors.New("Capsule pagination limit exceeded")
}

func (a *generatedAPI) detail(
	ctx context.Context,
	project client.Project,
	capsule client.Capsule,
) (CapsuleDetail, error) {
	detail := CapsuleDetail{Project: project, Capsule: capsule}
	runs, err := a.runs(ctx, capsule.ID)
	if err != nil {
		return detail, err
	}
	detail.Runs = runs
	if latest := detail.LatestRun(); latest != nil {
		events, err := a.events(ctx, latest.ID)
		if err != nil {
			return detail, err
		}
		if len(events) > 8 {
			detail.Events = events[len(events)-8:]
		} else {
			detail.Events = events
		}
	}
	moments, err := a.moments(ctx, capsule.ID)
	if err != nil {
		return detail, err
	}
	detail.Moments = moments
	timelineResponse, err := a.client.GetTimeline(ctx, client.GetTimelineParams{
		TimelineId: capsule.TimelineId,
	})
	if err != nil {
		return detail, transportError(err)
	}
	timeline, ok := timelineResponse.(*client.TimelineView)
	if !ok {
		return detail, responseError(timelineResponse)
	}
	detail.Timeline = timeline
	profileResponse, err := a.client.ListHarnessProfiles(
		ctx, client.ListHarnessProfilesParams{CapsuleId: capsule.ID},
	)
	if err != nil {
		return detail, transportError(err)
	}
	profiles, ok := profileResponse.(*client.HarnessProfilePage)
	if !ok {
		if _, unsupported := profileResponse.(*client.ListHarnessProfilesUnprocessableEntity); !unsupported {
			return detail, responseError(profileResponse)
		}
	} else {
		detail.Profiles = profiles.Items
	}
	threads, err := a.threads(ctx, capsule.ID)
	if err != nil {
		return detail, err
	}
	detail.Threads = threads
	return detail, nil
}

func (a *generatedAPI) threads(ctx context.Context, capsuleID string) ([]ThreadDetail, error) {
	var output []ThreadDetail
	var cursor string
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		params := client.ListThreadsParams{CapsuleId: capsuleID, Limit: client.NewOptInt(100)}
		if cursor != "" {
			params.Cursor = client.NewOptString(cursor)
		}
		response, err := a.client.ListThreads(ctx, params)
		if err != nil {
			return nil, transportError(err)
		}
		page, ok := response.(*client.ThreadPage)
		if !ok {
			return nil, responseError(response)
		}
		for index := range page.Items {
			detail := ThreadDetail{Thread: page.Items[index]}
			if detail.Thread.State != client.ThreadStateDeleted {
				a.loadThreadBlocks(ctx, &detail)
			}
			output = append(output, detail)
		}
		next, more := page.NextCursor.Get()
		if !more || next == "" {
			sort.Slice(output, func(left, right int) bool {
				if output[left].Thread.UpdatedAt.Equal(output[right].Thread.UpdatedAt) {
					return output[left].Thread.ID > output[right].Thread.ID
				}
				return output[left].Thread.UpdatedAt.After(output[right].Thread.UpdatedAt)
			})
			return output, nil
		}
		cursor = next
	}
	return nil, errors.New("Thread pagination limit exceeded")
}

func (a *generatedAPI) loadThreadBlocks(ctx context.Context, detail *ThreadDetail) {
	const (
		pageSize      = 100
		maxTranscript = 400
		maxPages      = 100
	)
	var after int64
	for pageNumber := 0; pageNumber < maxPages; pageNumber++ {
		response, err := a.client.ListThreadBlocks(ctx, client.ListThreadBlocksParams{
			ThreadId: detail.Thread.ID,
			After:    client.NewOptInt64(after),
			Limit:    client.NewOptInt(pageSize),
		})
		if err != nil {
			detail.TranscriptCode = "transport"
			detail.TranscriptError = transportError(err).Error()
			return
		}
		switch value := response.(type) {
		case *client.ThreadBlockPage:
			detail.Blocks = append(detail.Blocks, value.Items...)
			if len(detail.Blocks) > maxTranscript {
				detail.Blocks = detail.Blocks[len(detail.Blocks)-maxTranscript:]
				detail.Truncated = true
			}
			detail.Cursor = value.NextCursor
			detail.More = value.More
			if gap, ok := value.Gap.Get(); ok {
				detail.Gap = &gap
			}
			if !value.More {
				return
			}
			if value.NextCursor <= after {
				detail.TranscriptCode = "cursor"
				detail.TranscriptError = "Thread block cursor did not advance"
				return
			}
			after = value.NextCursor
		case *client.ListThreadBlocksLocked:
			detail.TranscriptCode = "transcript_locked"
			detail.TranscriptError = "Transcript key is missing or does not match"
			return
		case *client.ListThreadBlocksConflict:
			detail.TranscriptCode = "transcript_corrupt"
			detail.TranscriptError = "Encrypted transcript is corrupt"
			return
		default:
			detail.TranscriptCode = "read_error"
			detail.TranscriptError = responseError(response).Error()
			return
		}
	}
	detail.More = true
	detail.Truncated = true
}

func (a *generatedAPI) runs(ctx context.Context, capsuleID string) ([]client.Run, error) {
	var output []client.Run
	var cursor string
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		params := client.ListRunsParams{CapsuleId: capsuleID, Limit: client.NewOptInt(100)}
		if cursor != "" {
			params.Cursor = client.NewOptString(cursor)
		}
		response, err := a.client.ListRuns(ctx, params)
		if err != nil {
			return nil, transportError(err)
		}
		page, ok := response.(*client.RunPage)
		if !ok {
			return nil, responseError(response)
		}
		output = append(output, page.Items...)
		next, more := page.NextCursor.Get()
		if !more || next == "" {
			sort.Slice(output, func(left, right int) bool {
				if output[left].CreatedAt.Equal(output[right].CreatedAt) {
					return output[left].ID > output[right].ID
				}
				return output[left].CreatedAt.After(output[right].CreatedAt)
			})
			return output, nil
		}
		cursor = next
	}
	return nil, errors.New("Run pagination limit exceeded")
}

func (a *generatedAPI) events(ctx context.Context, runID string) ([]client.RunEvent, error) {
	var output []client.RunEvent
	var after int64
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		response, err := a.client.ListRunEvents(ctx, client.ListRunEventsParams{
			RunId: runID,
			After: client.NewOptInt64(after),
			Limit: client.NewOptInt(1000),
		})
		if err != nil {
			return nil, transportError(err)
		}
		page, ok := response.(*client.RunEventPage)
		if !ok {
			return nil, responseError(response)
		}
		output = append(output, page.Items...)
		if !page.More {
			return output, nil
		}
		if page.NextCursor <= after {
			return nil, errors.New("Run event cursor did not advance")
		}
		after = page.NextCursor
	}
	return nil, errors.New("Run event pagination limit exceeded")
}

func (a *generatedAPI) moments(ctx context.Context, capsuleID string) ([]client.Moment, error) {
	var output []client.Moment
	var cursor string
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		params := client.ListMomentsParams{CapsuleId: capsuleID, Limit: client.NewOptInt(100)}
		if cursor != "" {
			params.Cursor = client.NewOptString(cursor)
		}
		response, err := a.client.ListMoments(ctx, params)
		if err != nil {
			return nil, transportError(err)
		}
		page, ok := response.(*client.MomentPage)
		if !ok {
			return nil, responseError(response)
		}
		output = append(output, page.Items...)
		next, more := page.NextCursor.Get()
		if !more || next == "" {
			sort.Slice(output, func(left, right int) bool {
				if output[left].CreatedAt.Equal(output[right].CreatedAt) {
					return output[left].ID > output[right].ID
				}
				return output[left].CreatedAt.After(output[right].CreatedAt)
			})
			return output, nil
		}
		cursor = next
	}
	return nil, errors.New("Moment pagination limit exceeded")
}

func (a *generatedAPI) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	key, err := replayKey()
	if err != nil {
		return ActionResult{}, err
	}
	switch request.Action {
	case ActionCreate:
		response, err := a.client.CreateCapsule(
			ctx,
			&client.CreateCapsuleRequest{Name: request.Name},
			client.CreateCapsuleParams{ProjectId: request.ProjectID, IdempotencyKey: key},
		)
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		if _, ok := response.(*client.CapsuleHeaders); !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{Message: "Capsule creation requested"}, nil
	case ActionPause, ActionResume, ActionDelete:
		input := &client.LifecycleMutationRequest{ExpectedResourceVersion: request.ResourceVersion}
		var response any
		if request.Action == ActionPause {
			response, err = a.client.PauseCapsule(
				ctx, input, client.PauseCapsuleParams{CapsuleId: request.CapsuleID, IdempotencyKey: key},
			)
		} else if request.Action == ActionResume {
			response, err = a.client.ResumeCapsule(
				ctx, input, client.ResumeCapsuleParams{CapsuleId: request.CapsuleID, IdempotencyKey: key},
			)
		} else {
			response, err = a.client.DeleteCapsule(
				ctx, input, client.DeleteCapsuleParams{CapsuleId: request.CapsuleID, IdempotencyKey: key},
			)
		}
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		if _, ok := response.(*client.CapsuleAcceptedHeaders); !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{Message: string(request.Action) + " requested"}, nil
	case ActionDiff:
		response, err := a.client.GetCapsuleGitDiff(
			ctx, client.GetCapsuleGitDiffParams{CapsuleId: request.CapsuleID},
		)
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		diff, ok := response.(*client.GitResult)
		if !ok {
			return ActionResult{}, responseError(response)
		}
		message := "Git diff"
		if diff.Truncated {
			message += " (truncated)"
		}
		return ActionResult{Message: message, Content: diff.Content}, nil
	case ActionMoment:
		response, err := a.client.CaptureMoment(
			ctx,
			&client.CaptureMomentRequest{
				Name: request.Name, ExpectedResourceVersion: request.ResourceVersion,
			},
			client.CaptureMomentParams{CapsuleId: request.CapsuleID, IdempotencyKey: key},
		)
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		if _, ok := response.(*client.Moment); !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{Message: "Moment captured"}, nil
	case ActionShard:
		response, err := a.client.CreateShard(
			ctx,
			&client.CreateDescendantRequest{Name: request.Name},
			client.CreateShardParams{MomentId: request.MomentID, IdempotencyKey: key},
		)
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		if _, ok := response.(*client.DescendantResult); !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{Message: "Shard creation requested"}, nil
	case ActionRewind:
		response, err := a.client.RewindCapsule(
			ctx,
			&client.RewindRequest{MomentId: request.MomentID, Name: request.Name},
			client.RewindCapsuleParams{CapsuleId: request.CapsuleID, IdempotencyKey: key},
		)
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		if _, ok := response.(*client.DescendantResult); !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{Message: "Rewind descendant requested"}, nil
	case ActionSeal:
		response, err := a.client.SealCapsule(
			ctx,
			&client.LifecycleMutationRequest{ExpectedResourceVersion: request.ResourceVersion},
			client.SealCapsuleParams{CapsuleId: request.CapsuleID, IdempotencyKey: key},
		)
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		if _, ok := response.(*client.SealResult); !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{Message: "Capsule sealed"}, nil
	case ActionThreadCreate:
		input := &client.CreateThreadRequest{
			Harness: request.Harness,
			Start:   client.NewOptBool(request.Start),
		}
		if request.Content != "" {
			input.FirstMessage = client.NewOptString(request.Content)
		}
		response, err := a.client.CreateThread(ctx, input, client.CreateThreadParams{
			CapsuleId: request.CapsuleID, IdempotencyKey: key,
		})
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		success, ok := response.(*client.ThreadMutationResultHeaders)
		if !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{
			Message: "Thread created", ThreadID: success.Response.Thread.ID,
		}, nil
	case ActionThreadStart, ActionThreadResume, ActionThreadCancel:
		input := &client.LifecycleMutationRequest{ExpectedResourceVersion: request.ResourceVersion}
		var response any
		if request.Action == ActionThreadStart {
			response, err = a.client.StartThread(ctx, input, client.StartThreadParams{
				ThreadId: request.ThreadID, IdempotencyKey: key,
			})
		} else if request.Action == ActionThreadResume {
			response, err = a.client.ResumeThread(ctx, input, client.ResumeThreadParams{
				ThreadId: request.ThreadID, IdempotencyKey: key,
			})
		} else {
			response, err = a.client.CancelThread(ctx, input, client.CancelThreadParams{
				ThreadId: request.ThreadID, IdempotencyKey: key,
			})
		}
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		success, ok := response.(*client.ThreadSessionMutationHeaders)
		if !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{
			Message:  string(request.Action) + " requested",
			ThreadID: success.Response.Thread.ID,
		}, nil
	case ActionThreadSend:
		response, err := a.client.SendThreadMessage(ctx, &client.SendThreadMessageRequest{
			ExpectedResourceVersion: request.ResourceVersion,
			Content:                 request.Content,
		}, client.SendThreadMessageParams{ThreadId: request.ThreadID, IdempotencyKey: key})
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		success, ok := response.(*client.ThreadSessionMutationHeaders)
		if !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{Message: "Message sent", ThreadID: success.Response.Thread.ID}, nil
	case ActionThreadRespond:
		input := &client.ThreadResponseRequest{
			ExpectedResourceVersion: request.ResourceVersion,
			ResponseTo:              request.ResponseTo,
		}
		if request.Choice != "" {
			input.Choice = client.NewOptString(request.Choice)
		} else {
			input.Input = client.NewOptString(request.Input)
		}
		response, err := a.client.RespondThread(ctx, input, client.RespondThreadParams{
			ThreadId: request.ThreadID, IdempotencyKey: key,
		})
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		success, ok := response.(*client.ThreadSessionMutationHeaders)
		if !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{Message: "Response sent", ThreadID: success.Response.Thread.ID}, nil
	case ActionThreadArchive, ActionThreadDelete:
		var response any
		if request.Action == ActionThreadArchive {
			response, err = a.client.ArchiveThread(
				ctx,
				&client.LifecycleMutationRequest{ExpectedResourceVersion: request.ResourceVersion},
				client.ArchiveThreadParams{ThreadId: request.ThreadID, IdempotencyKey: key},
			)
		} else {
			response, err = a.client.DeleteThread(
				ctx,
				&client.DeleteThreadRequest{
					ExpectedResourceVersion: request.ResourceVersion,
					Confirmation:            client.DeleteThreadRequestConfirmationCryptoShred,
				},
				client.DeleteThreadParams{ThreadId: request.ThreadID, IdempotencyKey: key},
			)
		}
		if err != nil {
			return ActionResult{}, transportError(err)
		}
		success, ok := response.(*client.ThreadMutationHeaders)
		if !ok {
			return ActionResult{}, responseError(response)
		}
		return ActionResult{
			Message: string(request.Action) + " completed", ThreadID: success.Response.ID,
		}, nil
	default:
		return ActionResult{}, fmt.Errorf("unsupported dashboard action %q", request.Action)
	}
}

func replayKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func transportError(err error) error {
	var response *client.DefaultErrorStatusCode
	if errors.As(err, &response) {
		return fmt.Errorf("%s: %s", response.Response.Error.Code, response.Response.Error.Message)
	}
	return fmt.Errorf("API request failed: %w", err)
}

func responseError(response any) error {
	encoded, err := json.Marshal(response)
	if err == nil {
		var envelope client.ErrorEnvelope
		if json.Unmarshal(encoded, &envelope) == nil && envelope.Error.Code != "" {
			return fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message)
		}
	}
	return fmt.Errorf("API returned unexpected %T", response)
}
