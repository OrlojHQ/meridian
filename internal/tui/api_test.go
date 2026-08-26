package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OrlojHQ/meridian/pkg/client"
)

type testSecuritySource struct{}

func (testSecuritySource) BearerAuth(
	context.Context,
	client.OperationName,
) (client.BearerAuth, error) {
	return client.BearerAuth{Token: "test-token"}, nil
}

func TestSnapshotDoesNotQueryDeletedCapsuleRuntime(t *testing.T) {
	profileRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/projects":
			_, _ = writer.Write([]byte(`{"items":[{"id":"project-1","name":"project","createdAt":"2026-08-25T00:00:00Z","updatedAt":"2026-08-25T00:00:00Z","resourceVersion":1}]}`))
		case "/projects/project-1/capsules":
			_, _ = writer.Write([]byte(`{"items":[{"id":"capsule-1","projectId":"project-1","timelineId":"timeline-1","name":"deleted","state":"Deleted","desiredState":"Deleted","restoreComplete":false,"createdAt":"2026-08-25T00:00:00Z","updatedAt":"2026-08-25T00:00:00Z","resourceVersion":2}]}`))
		case "/capsules/capsule-1/runs":
			_, _ = writer.Write([]byte(`{"items":[{"id":"run-1","capsuleId":"capsule-1","harness":"test","state":"Running","createdAt":"2026-08-25T00:00:00Z","startedAt":"2026-08-25T00:00:01Z","updatedAt":"2026-08-25T00:00:01Z","resourceVersion":2}]}`))
		case "/runs/run-1/events":
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"error":{"code":"internal_error","message":"internal server error"}}`))
		case "/capsules/capsule-1/moments", "/capsules/capsule-1/threads":
			_, _ = writer.Write([]byte(`{"items":[]}`))
		case "/timelines/timeline-1":
			_, _ = writer.Write([]byte(`{"timeline":{"id":"timeline-1","projectId":"project-1","capsuleId":"capsule-1","reason":"root","createdAt":"2026-08-25T00:00:00Z"},"ancestry":[{"id":"timeline-1","projectId":"project-1","capsuleId":"capsule-1","reason":"root","createdAt":"2026-08-25T00:00:00Z"}]}`))
		case "/capsules/capsule-1/harness-profiles":
			profileRequested = true
			http.Error(writer, "runtime resource is gone", http.StatusInternalServerError)
		default:
			http.Error(writer, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	api, err := NewAPI(server.URL, testSecuritySource{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := api.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if profileRequested {
		t.Fatal("deleted Capsule runtime profiles were requested")
	}
	if len(snapshot.Capsules) != 1 || snapshot.Capsules[0].Capsule.State != client.CapsuleStateDeleted {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Capsules[0].EventError == "" {
		t.Fatal("Run event failure was not retained as a Capsule warning")
	}
}
