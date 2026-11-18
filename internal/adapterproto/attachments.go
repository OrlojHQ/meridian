package adapterproto

import (
	"path"
	"regexp"
	"strings"
)

const (
	// MaxAttachmentBytes is the decoded size of one composer attachment.
	MaxAttachmentBytes = 4 << 20
	// MaxAttachments is how many files one message may carry.
	MaxAttachments = 8
	// MaxAttachmentTotalBytes is the decoded size of every attachment on one message.
	MaxAttachmentTotalBytes = 8 << 20
	// AttachmentPrefix is the only workspace tree a composer attachment may occupy.
	AttachmentPrefix = ".meridian/attachments/"
	// AttachmentWorkspace is the Capsule workspace root that attachment paths
	// are relative to. A harness may run in a subdirectory of it, so the
	// prompt names attachments by absolute path.
	AttachmentWorkspace = "/workspace"
)

// attachmentPath matches a station-chosen relative path: a 16-hex directory
// and a numbered safe filename. The capsule refuses every other write.
var attachmentPath = regexp.MustCompile(`^\.meridian/attachments/[0-9a-f]{16}/[1-8]-[A-Za-z0-9][A-Za-z0-9._-]{0,118}$`)

// ValidAttachmentPath reports whether path is a single attachment file the
// workspace writer is allowed to create.
func ValidAttachmentPath(value string) bool {
	if !attachmentPath.MatchString(value) || strings.Contains(value, "..") {
		return false
	}
	return path.Clean(value) == value
}

// SniffImage reports the media type of a PNG, JPEG, GIF, or WebP payload.
// Anything else, including a type the caller claimed, is not an image.
func SniffImage(data []byte) (string, bool) {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", true
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", true
	default:
		return "", false
	}
}
