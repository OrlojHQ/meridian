package transcripts

import (
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

type PlaintextBlock struct {
	ID      domain.ThreadBlockID
	Kind    domain.ThreadBlockKind
	Content []byte
}

type MessageInput struct {
	ID        domain.ThreadMessageID
	ThreadID  domain.ThreadID
	Sequence  int64
	Role      domain.ThreadMessageRole
	Kind      domain.ThreadMessageKind
	CreatedAt time.Time
	Blocks    []PlaintextBlock
}

// EncryptMessage validates all quotas before allocating ciphertext and returns
// a persistence-safe message containing no plaintext.
func EncryptMessage(dek []byte, input MessageInput) (domain.ThreadMessage, error) {
	if input.ID == "" || input.ThreadID == "" || input.Sequence <= 0 ||
		!input.Role.Valid() || !input.Kind.Valid() || input.CreatedAt.IsZero() ||
		len(input.Blocks) == 0 {
		return domain.ThreadMessage{}, fmt.Errorf("%w: invalid ThreadMessage", domain.ErrInvalid)
	}
	if len(input.Blocks) > MaxBlocksPerMessage {
		return domain.ThreadMessage{}, fmt.Errorf(
			"%w: ThreadMessage block count exceeds limit", domain.ErrResourceExhausted,
		)
	}
	total := 0
	for _, block := range input.Blocks {
		if block.ID == "" || !block.Kind.Valid() {
			return domain.ThreadMessage{}, fmt.Errorf("%w: invalid ThreadBlock", domain.ErrInvalid)
		}
		if len(block.Content) > MaxPlaintextBytes ||
			total > MaxMessagePlaintextBytes-len(block.Content) {
			return domain.ThreadMessage{}, fmt.Errorf(
				"%w: ThreadMessage plaintext exceeds limit", domain.ErrResourceExhausted,
			)
		}
		total += len(block.Content)
	}
	createdAt := input.CreatedAt.UTC()
	message := domain.ThreadMessage{
		ID: input.ID, ThreadID: input.ThreadID, Sequence: input.Sequence,
		Role: input.Role, Kind: input.Kind, CreatedAt: createdAt,
		Blocks: make([]domain.ThreadBlock, 0, len(input.Blocks)),
	}
	for index, plaintextBlock := range input.Blocks {
		blockSequence := int64(index + 1)
		metadata := FrameMetadata{
			ThreadID: input.ThreadID, MessageID: input.ID,
			MessageSequence: input.Sequence, BlockSequence: blockSequence,
			Role: input.Role, MessageKind: input.Kind, BlockKind: plaintextBlock.Kind,
			CreatedAt: createdAt.Format(time.RFC3339Nano),
		}
		envelope, err := Seal(dek, metadata, plaintextBlock.Content)
		if err != nil {
			for blockIndex := range message.Blocks {
				zero(message.Blocks[blockIndex].Ciphertext)
			}
			return domain.ThreadMessage{}, err
		}
		message.Blocks = append(message.Blocks, domain.ThreadBlock{
			ID: plaintextBlock.ID, ThreadID: input.ThreadID, MessageID: input.ID,
			MessageSequence: input.Sequence, Sequence: blockSequence, Kind: plaintextBlock.Kind,
			EnvelopeVersion: EnvelopeVersion, Ciphertext: envelope, CreatedAt: createdAt,
		})
	}
	return message, nil
}

// DecryptMessage returns bounded plaintext blocks after authenticating every
// immutable metadata field. The caller owns and should zero returned buffers.
func DecryptMessage(dek []byte, message domain.ThreadMessage) ([][]byte, error) {
	if err := message.Validate(); err != nil {
		return nil, err
	}
	if len(message.Blocks) > MaxBlocksPerMessage {
		return nil, domain.ErrTranscriptCorrupt
	}
	total := 0
	plaintext := make([][]byte, 0, len(message.Blocks))
	for _, block := range message.Blocks {
		metadata := FrameMetadata{
			ThreadID: message.ThreadID, MessageID: message.ID,
			MessageSequence: message.Sequence, BlockSequence: block.Sequence,
			Role: message.Role, MessageKind: message.Kind, BlockKind: block.Kind,
			CreatedAt: block.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		content, err := Open(dek, metadata, block.Ciphertext)
		if err != nil || total > MaxMessagePlaintextBytes-len(content) {
			zero(content)
			ZeroPlaintextBlocks(plaintext)
			if err != nil {
				return nil, err
			}
			return nil, domain.ErrTranscriptCorrupt
		}
		total += len(content)
		plaintext = append(plaintext, content)
	}
	return plaintext, nil
}

func ZeroPlaintextBlocks(blocks [][]byte) {
	for _, block := range blocks {
		zero(block)
	}
}
