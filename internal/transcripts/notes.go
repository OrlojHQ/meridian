package transcripts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/OrlojHQ/meridian/internal/domain"
)

// Notes envelopes (ADR 0060). Each Project has one Project-notes data key
// and each Coordination Grant one working-notes data key. Both are wrapped
// with the installation transcript KEK like a Thread's key, but the wrap
// binds the Project, the grant and the owner, so a wrapped key moved to
// another Project, tree or owner fails to open. Revision content binds its
// note, sequence, kind and writing Capsule; proposal content binds its
// proposal, base revision and proposing Thread.

var (
	noteMagic    = [4]byte{'M', 'N', 'T', '1'}
	noteDEKMagic = [4]byte{'M', 'N', 'K', '1'}
)

// MaxNoteEnvelopeBytes bounds one sealed note.
const MaxNoteEnvelopeBytes = domain.MaxNoteBytes + 4 + 12 + 16

// NoteKeyScope is what a notes data key is wrapped for. GrantID is empty
// for a Project-notes key.
type NoteKeyScope struct {
	ProjectID domain.ProjectID
	GrantID   domain.CoordinationGrantID
	Owner     domain.UserID
}

func (s NoteKeyScope) valid() bool { return s.ProjectID != "" }

// NoteRevisionAAD is what a revision's envelope authenticates.
type NoteRevisionAAD struct {
	ProjectID domain.ProjectID
	GrantID   domain.CoordinationGrantID
	NoteID    domain.NoteID
	Seq       int64
	Kind      domain.NoteRevisionKind
	// ProposalID names the approved proposal; CapsuleID the Capsule that
	// wrote an agent revision. Either may be empty.
	ProposalID domain.NoteProposalID
	CapsuleID  domain.CapsuleID
}

func (a NoteRevisionAAD) bytes() []byte {
	buffer := []byte("meridian/note-revision/v1")
	buffer = appendString(buffer, string(a.ProjectID))
	buffer = appendString(buffer, string(a.GrantID))
	buffer = appendString(buffer, string(a.NoteID))
	buffer = binary.BigEndian.AppendUint64(buffer, uint64(a.Seq))
	buffer = appendString(buffer, string(a.Kind))
	buffer = appendString(buffer, string(a.ProposalID))
	return appendString(buffer, string(a.CapsuleID))
}

func (a NoteRevisionAAD) valid() bool {
	return a.ProjectID != "" && a.NoteID != "" && a.Seq > 0 && a.Kind.Valid()
}

// NoteProposalAAD is what a proposal's envelope authenticates.
type NoteProposalAAD struct {
	ProjectID  domain.ProjectID
	ProposalID domain.NoteProposalID
	BaseSeq    int64
	ThreadID   domain.ThreadID
}

func (a NoteProposalAAD) bytes() []byte {
	buffer := []byte("meridian/note-proposal/v1")
	buffer = appendString(buffer, string(a.ProjectID))
	buffer = appendString(buffer, string(a.ProposalID))
	buffer = binary.BigEndian.AppendUint64(buffer, uint64(a.BaseSeq))
	return appendString(buffer, string(a.ThreadID))
}

func (a NoteProposalAAD) valid() bool {
	return a.ProjectID != "" && a.ProposalID != "" && a.BaseSeq >= 0
}

func noteWrapAAD(scope NoteKeyScope, keyID string, version uint32) []byte {
	buffer := []byte("meridian/notes-dek/v1")
	buffer = appendString(buffer, string(scope.ProjectID))
	buffer = appendString(buffer, string(scope.GrantID))
	buffer = appendString(buffer, string(scope.Owner))
	buffer = appendString(buffer, keyID)
	return binary.BigEndian.AppendUint32(buffer, version)
}

// WrapNotesDEK wraps a notes data key for scope under the current KEK.
func (k *InstallationKey) WrapNotesDEK(scope NoteKeyScope, dek []byte) ([]byte, error) {
	if !k.valid() || !scope.valid() || len(dek) != 32 {
		return nil, fmt.Errorf("%w: invalid notes key envelope", domain.ErrInvalid)
	}
	aead, err := k.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate notes key nonce: %w", err)
	}
	out := make([]byte, 4+len(nonce), 4+len(nonce)+len(dek)+aead.Overhead())
	copy(out, noteDEKMagic[:])
	copy(out[4:], nonce)
	out = aead.Seal(out, nonce, dek, noteWrapAAD(scope, k.ID, k.Version))
	zero(nonce)
	return out, nil
}

// UnwrapNotesDEK opens a notes data key wrapped for scope under the named
// KEK, which must be the current one or, during a rotation, the previous.
func (k *InstallationKey) UnwrapNotesDEK(
	scope NoteKeyScope, keyID string, keyVersion uint32, wrapped []byte,
) ([]byte, error) {
	if !k.Matches(keyID, keyVersion) {
		return nil, domain.ErrKeyMismatch
	}
	if !scope.valid() || len(wrapped) != 4+12+32+16 ||
		subtle.ConstantTimeCompare(wrapped[:4], noteDEKMagic[:]) != 1 {
		return nil, domain.ErrTranscriptCorrupt
	}
	material := k.key[:]
	if !(keyVersion == k.Version && len(keyID) == len(k.ID) &&
		subtle.ConstantTimeCompare([]byte(keyID), []byte(k.ID)) == 1) {
		if k.previous == nil {
			return nil, domain.ErrKeyMismatch
		}
		material = k.previous.Key[:]
	}
	aead, err := aeadFor(material)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, wrapped[4:16], wrapped[16:], noteWrapAAD(scope, keyID, keyVersion))
	if err != nil || len(plaintext) != 32 {
		zero(plaintext)
		return nil, domain.ErrTranscriptCorrupt
	}
	return plaintext, nil
}

// RewrapNotesDEK authenticates a notes key under from with oldKey and wraps
// the same key for to under newKey. A key rotation passes the same scope
// twice; an owner transfer passes the same key twice.
func RewrapNotesDEK(
	from, to NoteKeyScope, keyID string, keyVersion uint32, wrapped []byte,
	oldKey, newKey *InstallationKey,
) ([]byte, error) {
	if oldKey == nil || newKey == nil {
		return nil, fmt.Errorf("%w: missing notes key", domain.ErrInvalid)
	}
	dek, err := oldKey.UnwrapNotesDEK(from, keyID, keyVersion, wrapped)
	if err != nil {
		return nil, err
	}
	defer zero(dek)
	return newKey.WrapNotesDEK(to, dek)
}

// SealNoteRevision encrypts one revision's content.
func SealNoteRevision(dek []byte, aad NoteRevisionAAD, content []byte) ([]byte, error) {
	if !aad.valid() {
		return nil, fmt.Errorf("%w: invalid note revision metadata", domain.ErrInvalid)
	}
	return sealNote(dek, aad.bytes(), content)
}

// OpenNoteRevision authenticates and decrypts one revision's content.
func OpenNoteRevision(dek []byte, aad NoteRevisionAAD, envelope []byte) ([]byte, error) {
	if !aad.valid() {
		return nil, domain.ErrTranscriptCorrupt
	}
	return openNote(dek, aad.bytes(), envelope)
}

// SealNoteProposal encrypts one proposal's content.
func SealNoteProposal(dek []byte, aad NoteProposalAAD, content []byte) ([]byte, error) {
	if !aad.valid() {
		return nil, fmt.Errorf("%w: invalid note proposal metadata", domain.ErrInvalid)
	}
	return sealNote(dek, aad.bytes(), content)
}

// OpenNoteProposal authenticates and decrypts one proposal's content.
func OpenNoteProposal(dek []byte, aad NoteProposalAAD, envelope []byte) ([]byte, error) {
	if !aad.valid() {
		return nil, domain.ErrTranscriptCorrupt
	}
	return openNote(dek, aad.bytes(), envelope)
}

func sealNote(dek, aad, content []byte) ([]byte, error) {
	if len(dek) != 32 {
		return nil, fmt.Errorf("%w: invalid notes key", domain.ErrInvalid)
	}
	if len(content) > domain.MaxNoteBytes {
		return nil, fmt.Errorf("%w: note exceeds limit", domain.ErrResourceExhausted)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("initialize note cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize note envelope: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate note nonce: %w", err)
	}
	out := make([]byte, 4+len(nonce), 4+len(nonce)+len(content)+aead.Overhead())
	copy(out, noteMagic[:])
	copy(out[4:], nonce)
	out = aead.Seal(out, nonce, content, aad)
	zero(nonce)
	return out, nil
}

func openNote(dek, aad, envelope []byte) ([]byte, error) {
	if len(dek) != 32 || len(envelope) < 4+12+16 || len(envelope) > MaxNoteEnvelopeBytes ||
		subtle.ConstantTimeCompare(envelope[:4], noteMagic[:]) != 1 {
		return nil, domain.ErrTranscriptCorrupt
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, domain.ErrTranscriptCorrupt
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, domain.ErrTranscriptCorrupt
	}
	plaintext, err := aead.Open(nil, envelope[4:16], envelope[16:], aad)
	if err != nil || len(plaintext) > domain.MaxNoteBytes {
		zero(plaintext)
		return nil, domain.ErrTranscriptCorrupt
	}
	return plaintext, nil
}

// ZeroBytes clears a plaintext buffer the caller no longer needs.
func ZeroBytes(value []byte) { zero(value) }
