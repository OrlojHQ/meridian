package app_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

type memoryBrowser struct {
	files map[string][]byte
}

func (m *memoryBrowser) ListWorkspaceFiles(
	context.Context, string, string, string, int,
) (ports.WorkspaceFilePage, error) {
	return ports.WorkspaceFilePage{}, domain.ErrUnsupported
}

func (m *memoryBrowser) ReadWorkspaceFile(context.Context, string, string) (ports.WorkspaceFile, error) {
	return ports.WorkspaceFile{}, domain.ErrUnsupported
}

func (m *memoryBrowser) WriteWorkspaceFile(_ context.Context, resourceID, path string, content []byte) error {
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[resourceID+"\n"+path] = append([]byte(nil), content...)
	return nil
}

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestThreadAttachmentsCopyFilesAndKeepBytesOutOfTheTranscript(t *testing.T) {
	ctx := operatorContext()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	browser := &memoryBrowser{}
	service.ConfigureBrowse(browser)
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "hello",
		Start: true, IdempotencyKey: "attach-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := service.GetThread(ctx, created.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	notes := []byte("remember the fixture\n")
	sent, err := service.SendThreadMessageWithAttachments(
		ctx, thread.ID, thread.ResourceVersion, "see the notes", "attach-file", "",
		[]app.ThreadAttachmentInput{{Name: "notes.txt", MediaType: "text/plain", Data: append([]byte(nil), notes...)}},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	frame := runtime.frames[string(sent.MessageID)]
	runtime.mu.Unlock()
	if !strings.Contains(frame.Content, "see the notes") || !strings.Contains(frame.Content, "Files in the workspace:\n- /workspace/.meridian/attachments/") {
		t.Fatalf("harness prompt = %q", frame.Content)
	}
	if bytes.Contains(frame.Metadata, notes) || bytes.Contains([]byte(frame.Content), notes) {
		t.Fatal("file bytes were copied into the adapter frame")
	}
	var saved []byte
	for path, content := range browser.files {
		if !strings.Contains(path, ".meridian/attachments/") || !strings.HasSuffix(path, "-notes.txt") {
			t.Fatalf("wrote %s", path)
		}
		if !adapterproto.ValidAttachmentPath(path[strings.Index(path, ".meridian/"):]) {
			t.Fatalf("path %s is not an attachment path", path)
		}
		saved = content
	}
	if !bytes.Equal(saved, notes) {
		t.Fatalf("workspace bytes = %q", saved)
	}
	page, err := service.ThreadBlocks(ctx, thread.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, block := range page.Items {
		if block.MessageID != sent.MessageID {
			continue
		}
		found = true
		if block.Content != "see the notes" || len(block.Attachments) != 1 ||
			block.Attachments[0].Kind != "file" || block.Attachments[0].Name != "notes.txt" {
			t.Fatalf("block = %#v", block)
		}
	}
	if !found {
		t.Fatal("sent message was not in the transcript")
	}
	audit, err := service.ListAuditRecords(ctx, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var wrote bool
	for _, record := range audit.Items {
		if record.Action == domain.AuditWorkspaceWrite && record.Result == domain.AuditAllowed &&
			record.ObjectID == string(capsuleID) {
			wrote = true
		}
	}
	if !wrote {
		t.Fatalf("audit = %#v", audit.Items)
	}
}

func TestThreadAttachmentsRejectOversizedUnknownAndUnsupportedImages(t *testing.T) {
	ctx := operatorContext()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	browser := &memoryBrowser{}
	service.ConfigureBrowse(browser)
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "hello",
		Start: true, IdempotencyKey: "attach-limits-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := service.GetThread(ctx, created.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	png := onePixelPNG(t)
	_, err = service.SendThreadMessageWithAttachments(
		ctx, thread.ID, thread.ResourceVersion, "picture", "attach-image", "",
		[]app.ThreadAttachmentInput{{Name: "shot.png", MediaType: "image/png", Data: png}},
	)
	if err == nil || !strings.Contains(err.Error(), "can't take images") {
		t.Fatalf("image without support = %v", err)
	}
	if len(browser.files) != 0 {
		t.Fatal("rejected image was written")
	}

	thread, err = service.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SendThreadMessageWithAttachments(
		ctx, thread.ID, thread.ResourceVersion, "run", "attach-type", "",
		[]app.ThreadAttachmentInput{{Name: "run.exe", MediaType: "application/octet-stream", Data: []byte("MZ")}},
	)
	if err == nil || !strings.Contains(err.Error(), "file type") {
		t.Fatalf("executable = %v", err)
	}

	thread, err = service.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SendThreadMessageWithAttachments(
		ctx, thread.ID, thread.ResourceVersion, "big", "attach-size", "",
		[]app.ThreadAttachmentInput{{Name: "notes.txt", MediaType: "text/plain", Data: bytes.Repeat([]byte("x"), adapterproto.MaxAttachmentBytes+1)}},
	)
	if err == nil || !strings.Contains(err.Error(), "larger than 4 MB") {
		t.Fatalf("oversize = %v", err)
	}
	if len(browser.files) != 0 {
		t.Fatal("rejected attachment was written")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	for _, frame := range runtime.frames {
		if strings.Contains(frame.Content, "picture") || strings.Contains(frame.Content, "run.exe") {
			t.Fatalf("rejected message was delivered: %#v", frame)
		}
	}
}

func TestThreadAttachmentsSendImagesWhenTheHarnessAdvertisesThem(t *testing.T) {
	ctx := operatorContext()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	browser := &memoryBrowser{}
	service.ConfigureBrowse(browser)
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "hello",
		Start: true, IdempotencyKey: "attach-vision-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := service.GetThread(ctx, created.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.events = ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{{Sequence: 1, Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindModels,
			Models: &adapterproto.ModelSet{
				Current: "vision", Images: true,
				Options: []adapterproto.ModelOption{{ID: "vision", Label: "Vision"}},
			},
		}}},
		NextCursor: 1,
	}
	if err := service.SyncThread(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	thread, err = service.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	png := onePixelPNG(t)
	sent, err := service.SendThreadMessageWithAttachments(
		ctx, thread.ID, thread.ResourceVersion, "look", "attach-vision", "",
		[]app.ThreadAttachmentInput{{Name: "shot.png", MediaType: "image/png", Data: png}},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	frame := runtime.frames[string(sent.MessageID)]
	runtime.mu.Unlock()
	if frame.Content != "look" {
		t.Fatalf("image prompt = %q", frame.Content)
	}
	if !bytes.Contains(frame.Metadata, []byte(`"kind":"image"`)) ||
		!bytes.Contains(frame.Metadata, []byte(`"mediaType":"image/png"`)) ||
		bytes.Contains(frame.Metadata, png) {
		t.Fatalf("image metadata = %s", frame.Metadata)
	}
	page, err := service.ThreadBlocks(ctx, thread.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range page.Items {
		if block.MessageID != sent.MessageID || len(block.Attachments) != 1 {
			continue
		}
		if block.Attachments[0].Kind != "image" || block.Content != "look" {
			t.Fatalf("image block = %#v", block)
		}
		return
	}
	t.Fatal("image attachment was not stored")
}
