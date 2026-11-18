package adapterproto

import "testing"

func TestSniffImageAndAttachmentPath(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nrest")
	if media, ok := SniffImage(png); !ok || media != "image/png" {
		t.Fatalf("png = %s %v", media, ok)
	}
	if _, ok := SniffImage([]byte("not an image")); ok {
		t.Fatal("text sniffed as an image")
	}
	if !ValidAttachmentPath(".meridian/attachments/0123456789abcdef/1-notes.txt") {
		t.Fatal("expected attachment path to be valid")
	}
	for _, path := range []string{
		"notes.txt",
		".meridian/attachments/../notes.txt",
		".meridian/attachments/0123456789abcdef/../1-notes.txt",
		".meridian/attachments/ZZZZZZZZZZZZZZZZ/1-notes.txt",
	} {
		if ValidAttachmentPath(path) {
			t.Fatalf("accepted %s", path)
		}
	}
}
