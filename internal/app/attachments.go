package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// inputError is a rejected request whose text is safe to show. It unwraps to
// ErrInvalid so the API maps it to 400, and Error is the sentence itself.
type inputError struct{ message string }

func (e inputError) Error() string { return e.message }

func (e inputError) Unwrap() error { return domain.ErrInvalid }

// InvalidInput rejects a request with a sentence the UI can show.
func InvalidInput(message string) error { return inputError{message: message} }

// ThreadAttachment is the description kept in the encrypted transcript.
// It names the workspace file and carries no bytes.
type ThreadAttachment struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
}

// ThreadAttachmentInput is one file the operator attached to a message.
// Data is the decoded bytes and is not persisted.
type ThreadAttachmentInput struct {
	Name      string
	MediaType string
	Data      []byte
}

const (
	attachmentKindImage = "image"
	attachmentKindFile  = "file"
)

// fileExtensions are the non-image types a message may copy into the workspace.
var fileExtensions = map[string]string{
	".txt": "text/plain", ".md": "text/markdown", ".markdown": "text/markdown",
	".json": "application/json", ".jsonl": "application/x-ndjson",
	".csv": "text/csv", ".tsv": "text/tab-separated-values",
	".pdf": "application/pdf", ".log": "text/plain",
	".yaml": "application/yaml", ".yml": "application/yaml",
	".xml": "application/xml", ".html": "text/html", ".css": "text/css",
	".patch": "text/x-diff", ".diff": "text/x-diff",
	".zip": "application/zip", ".gz": "application/gzip",
	".tgz": "application/gzip", ".tar": "application/x-tar",
	".toml": "application/toml", ".ini": "text/plain",
	".cfg": "text/plain", ".conf": "text/plain",
	".py": "text/x-python", ".go": "text/x-go",
	".ts": "text/plain", ".tsx": "text/plain", ".js": "text/plain", ".jsx": "text/plain",
	".rs": "text/plain", ".rb": "text/plain", ".java": "text/plain",
	".c": "text/plain", ".h": "text/plain", ".cc": "text/plain",
	".cpp": "text/plain", ".hpp": "text/plain",
	".sql": "text/plain", ".svg": "image/svg+xml", ".sh": "text/plain",
}

// preparedAttachment is a validated file ready to write.
type preparedAttachment struct {
	ThreadAttachment
	data []byte
}

// prepareAttachments checks size, type, and names, and chooses workspace paths.
// Image bytes are accepted only when the harness has advertised image support.
// The returned prompt is what the harness reads; it is not what the transcript shows.
func prepareAttachments(
	threadID domain.ThreadID, key, text string, inputs []ThreadAttachmentInput, images bool,
) (string, []preparedAttachment, json.RawMessage, error) {
	if len(inputs) == 0 {
		return text, nil, nil, nil
	}
	if len(inputs) > adapterproto.MaxAttachments {
		return "", nil, nil, InvalidInput("You can attach up to 8 files.")
	}
	total := 0
	prepared := make([]preparedAttachment, 0, len(inputs))
	seen := map[string]int{}
	directory := attachmentDirectory(threadID, key)
	for _, input := range inputs {
		if len(input.Data) == 0 {
			return "", nil, nil, InvalidInput("This file is empty.")
		}
		if len(input.Data) > adapterproto.MaxAttachmentBytes {
			return "", nil, nil, InvalidInput("This file is larger than 4 MB.")
		}
		total += len(input.Data)
		if total > adapterproto.MaxAttachmentTotalBytes {
			return "", nil, nil, InvalidInput("These attachments are larger than 8 MB together.")
		}
		name := attachmentName(input.Name)
		if name == "" {
			return "", nil, nil, InvalidInput("This file needs a simpler name.")
		}
		seen[name]++
		if seen[name] > 1 {
			base, ext := splitExt(name)
			name = base + "-" + attachmentIndex(seen[name]) + ext
		}
		media, kind, err := classifyAttachment(name, input.Data, images)
		if err != nil {
			return "", nil, nil, err
		}
		itemPath := adapterproto.AttachmentPrefix + directory + "/" + attachmentIndex(len(prepared)+1) + "-" + name
		if !adapterproto.ValidAttachmentPath(itemPath) {
			return "", nil, nil, InvalidInput("This file needs a simpler name.")
		}
		prepared = append(prepared, preparedAttachment{
			ThreadAttachment: ThreadAttachment{
				Name: name, MediaType: media, Size: int64(len(input.Data)),
				Path: itemPath, Kind: kind,
			},
			data: input.Data,
		})
	}
	prompt, err := harnessPrompt(text, prepared)
	if err != nil {
		return "", nil, nil, err
	}
	metadata, err := attachmentMetadata(prepared)
	if err != nil {
		return "", nil, nil, err
	}
	return prompt, prepared, metadata, nil
}

func classifyAttachment(name string, data []byte, images bool) (string, string, error) {
	if media, ok := adapterproto.SniffImage(data); ok {
		if !images {
			return "", "", InvalidInput("This model can't take images.")
		}
		if !imageName(name, media) {
			return "", "", InvalidInput("This image's name doesn't match its contents.")
		}
		return media, attachmentKindImage, nil
	}
	ext := strings.ToLower(path.Ext(name))
	media, ok := fileExtensions[ext]
	if !ok {
		return "", "", InvalidInput("This file type can't be attached.")
	}
	return media, attachmentKindFile, nil
}

func imageName(name, media string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return media == "image/png"
	case ".jpg", ".jpeg":
		return media == "image/jpeg"
	case ".gif":
		return media == "image/gif"
	case ".webp":
		return media == "image/webp"
	default:
		return false
	}
}

func attachmentName(value string) string {
	value = path.Base(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r < 0x20 || r == 0x7f || r > 0x7e:
			b.WriteByte('_')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-", r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "._-")
	if name == "" || !utf8.ValidString(name) || len(name) > 80 || path.Ext(name) == "" {
		return ""
	}
	if name[0] == '.' {
		return ""
	}
	return name
}

func splitExt(name string) (string, string) {
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext), ext
}

func attachmentIndex(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [4]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}

func attachmentDirectory(threadID domain.ThreadID, key string) string {
	sum := sha256.Sum256([]byte(string(threadID) + "\n" + key))
	return hex.EncodeToString(sum[:8])
}

func harnessPrompt(text string, prepared []preparedAttachment) (string, error) {
	var files []ThreadAttachment
	images := 0
	for _, item := range prepared {
		if item.Kind == attachmentKindImage {
			images++
			continue
		}
		files = append(files, item.ThreadAttachment)
	}
	prompt := text
	if len(files) > 0 {
		var b strings.Builder
		b.WriteString(text)
		if text != "" && !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
		if text != "" {
			b.WriteString("\n")
		}
		b.WriteString("Files in the workspace:\n")
		for _, file := range files {
			b.WriteString("- ")
			b.WriteString(file.Path)
			b.WriteByte('\n')
		}
		prompt = strings.TrimRight(b.String(), "\n")
	}
	if prompt == "" && images > 0 {
		prompt = "See the attached image."
	}
	if prompt == "" {
		return "", InvalidInput("A message needs text or an attachment.")
	}
	if len(prompt) > adapterproto.MaxContentBytes {
		return "", InvalidInput("This message is too long to include the attached files.")
	}
	return prompt, nil
}

func attachmentMetadata(prepared []preparedAttachment) (json.RawMessage, error) {
	var images []ThreadAttachment
	for _, item := range prepared {
		if item.Kind == attachmentKindImage {
			images = append(images, item.ThreadAttachment)
		}
	}
	if len(images) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(map[string]any{"attachments": images})
	if err != nil || len(encoded) > adapterproto.MaxMetadataBytes {
		return nil, InvalidInput("These attachments are too large to describe.")
	}
	return encoded, nil
}

// mergeMessageMetadata folds image descriptors into delivery metadata.
func mergeMessageMetadata(delivery json.RawMessage, images json.RawMessage) (json.RawMessage, error) {
	if len(images) == 0 {
		return delivery, nil
	}
	if len(delivery) == 0 {
		return images, nil
	}
	var base map[string]json.RawMessage
	var extra map[string]json.RawMessage
	if json.Unmarshal(delivery, &base) != nil || json.Unmarshal(images, &extra) != nil {
		return nil, InvalidInput("This message could not be sent.")
	}
	for key, value := range extra {
		base[key] = value
	}
	encoded, err := json.Marshal(base)
	if err != nil || len(encoded) > adapterproto.MaxMetadataBytes {
		return nil, InvalidInput("These attachments are too large to describe.")
	}
	return encoded, nil
}

func (s *Service) writePreparedAttachments(
	ctx context.Context, resourceID string, prepared []preparedAttachment,
) error {
	if len(prepared) == 0 {
		return nil
	}
	if s.browser == nil {
		return InvalidInput("This Capsule cannot take attachments.")
	}
	for _, item := range prepared {
		if err := s.browser.WriteWorkspaceFile(ctx, resourceID, item.Path, item.data); err != nil {
			return InvalidInput("The attachment could not be saved in the Capsule.")
		}
	}
	return nil
}

func wipeAttachments(inputs []ThreadAttachmentInput) {
	for _, input := range inputs {
		for i := range input.Data {
			input.Data[i] = 0
		}
	}
}

func storedAttachments(prepared []preparedAttachment) []ThreadAttachment {
	if len(prepared) == 0 {
		return nil
	}
	items := make([]ThreadAttachment, len(prepared))
	for i, item := range prepared {
		items[i] = item.ThreadAttachment
	}
	return items
}
