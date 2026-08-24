package transcripts

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestEnvelopeRoundTripTamperWrongKeyAndAADBinding(t *testing.T) {
	key, err := NewInstallationKey("", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	dek, err := key.NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	defer zero(dek)
	metadata := testMetadata()
	plaintext := []byte("sensitive transcript value")
	envelope, err := Seal(dek, metadata, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(dek, metadata, envelope)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("open failed: %v", err)
	}
	zero(opened)

	tampered := append([]byte(nil), envelope...)
	tampered[len(tampered)-1] ^= 1
	if _, err := Open(dek, metadata, tampered); !errors.Is(err, domain.ErrTranscriptCorrupt) {
		t.Fatalf("tamper error = %v", err)
	}
	swappedThread := metadata
	swappedThread.ThreadID = "other-thread"
	swappedMessage := metadata
	swappedMessage.MessageID = "other-message"
	swappedMessageSequence := metadata
	swappedMessageSequence.MessageSequence++
	swappedBlockSequence := metadata
	swappedBlockSequence.BlockSequence++
	swappedRole := metadata
	swappedRole.Role = domain.ThreadRoleAssistant
	swappedMessageKind := metadata
	swappedMessageKind.MessageKind = domain.ThreadMessageStatus
	swappedBlockKind := metadata
	swappedBlockKind.BlockKind = domain.ThreadBlockJSON
	swappedCreatedAt := metadata
	swappedCreatedAt.CreatedAt = "2026-08-24T12:00:01Z"
	swaps := map[string]FrameMetadata{
		"thread":           swappedThread,
		"message":          swappedMessage,
		"message sequence": swappedMessageSequence,
		"block sequence":   swappedBlockSequence,
		"role":             swappedRole,
		"message kind":     swappedMessageKind,
		"block kind":       swappedBlockKind,
		"created at":       swappedCreatedAt,
	}
	for name, swapped := range swaps {
		if _, err := Open(dek, swapped, envelope); !errors.Is(err, domain.ErrTranscriptCorrupt) {
			t.Fatalf("%s AAD swap error = %v", name, err)
		}
	}
	wrongDEK := bytes.Repeat([]byte{0x5a}, 32)
	if _, err := Open(wrongDEK, metadata, envelope); !errors.Is(err, domain.ErrTranscriptCorrupt) {
		t.Fatalf("wrong DEK error = %v", err)
	}
}

func TestDEKWrappingRotationAndCryptoShredPrimitive(t *testing.T) {
	oldKey, err := NewInstallationKey("installation", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer oldKey.Zero()
	newKey, err := NewInstallationKey("installation", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer newKey.Zero()
	dek, err := oldKey.NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	defer zero(dek)
	wrapped, err := oldKey.WrapDEK("thread", dek)
	if err != nil {
		t.Fatal(err)
	}
	rewrapped, err := RewrapDEK("thread", wrapped, oldKey, newKey)
	if err != nil {
		t.Fatal(err)
	}
	unwrapped, err := newKey.UnwrapDEK("thread", newKey.ID, newKey.Version, rewrapped)
	if err != nil || !bytes.Equal(unwrapped, dek) {
		t.Fatalf("rotated key failed: %v", err)
	}
	zero(unwrapped)
	if _, err := oldKey.UnwrapDEK("thread", newKey.ID, newKey.Version, rewrapped); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("old key mismatch = %v", err)
	}
	rewrapped[len(rewrapped)-1] ^= 1
	if _, err := newKey.UnwrapDEK("thread", newKey.ID, newKey.Version, rewrapped); !errors.Is(err, domain.ErrTranscriptCorrupt) {
		t.Fatalf("wrapped DEK tamper = %v", err)
	}
}

func TestNonceUniquenessAndLimits(t *testing.T) {
	dek := bytes.Repeat([]byte{1}, 32)
	metadata := testMetadata()
	seen := make(map[string]struct{}, 1000)
	for index := 0; index < 1000; index++ {
		envelope, err := Seal(dek, metadata, []byte("same"))
		if err != nil {
			t.Fatal(err)
		}
		nonce := string(envelope[4:16])
		if _, exists := seen[nonce]; exists {
			t.Fatal("duplicate random nonce")
		}
		seen[nonce] = struct{}{}
	}
	if _, err := Seal(dek, metadata, make([]byte, MaxPlaintextBytes+1)); !errors.Is(err, domain.ErrResourceExhausted) {
		t.Fatalf("plaintext limit error = %v", err)
	}
	if err := ValidateEnvelope(make([]byte, MaxFrameBytes+1)); !errors.Is(err, domain.ErrTranscriptCorrupt) {
		t.Fatalf("frame limit error = %v", err)
	}
}

func TestKeyFileAtomicCreationModesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "keys", "installation.key")
	const workers = 16
	keys := make(chan *InstallationKey, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			key, err := OpenOrCreateKey(path)
			if err != nil {
				errs <- err
				return
			}
			keys <- key
		}()
	}
	group.Wait()
	close(keys)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var first *InstallationKey
	for key := range keys {
		if first == nil {
			first = key
			continue
		}
		if !key.Matches(first.ID, first.Version) {
			t.Error("concurrent creators observed different keys")
		}
		key.Zero()
	}
	if first == nil {
		t.Fatal("no key created")
	}
	first.Zero()
	fileInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o", fileInfo.Mode().Perm())
	}
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("key directory mode = %o", dirInfo.Mode().Perm())
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(path); err == nil {
		t.Fatal("insecure key mode accepted")
	}

	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOrCreateKey(filepath.Join(link, "key")); err == nil {
		t.Fatal("symlink key directory accepted")
	}
	target := filepath.Join(real, "target.key")
	targetKey, err := OpenOrCreateKey(target)
	if err != nil {
		t.Fatal(err)
	}
	targetKey.Zero()
	keyLink := filepath.Join(real, "linked.key")
	if err := os.Symlink(target, keyLink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(keyLink); err == nil {
		t.Fatal("symlink key file accepted")
	}
	if _, err := OpenOrCreateKey(keyLink); err == nil {
		t.Fatal("existing symlink key file accepted")
	}
}

func TestZeroedInstallationKeyCannotEncrypt(t *testing.T) {
	key, err := NewInstallationKey("zero-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	key.Zero()
	if _, err := key.NewDEK(); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("zeroed key generated DEK: %v", err)
	}
	if _, err := key.WrapDEK("thread", bytes.Repeat([]byte{1}, 32)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("zeroed key wrapped DEK: %v", err)
	}
}

func TestErrorsDoNotContainTranscriptContent(t *testing.T) {
	dek := bytes.Repeat([]byte{7}, 32)
	metadata := testMetadata()
	secret := "content-that-must-not-escape"
	envelope, err := Seal(dek, metadata, []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	envelope[len(envelope)-1] ^= 1
	_, err = Open(dek, metadata, envelope)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe error = %v", err)
	}
}

func TestEncryptDecryptMessageAndPreallocationLimits(t *testing.T) {
	dek := bytes.Repeat([]byte{9}, 32)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	input := MessageInput{
		ID: "message", ThreadID: "thread", Sequence: 1,
		Role: domain.ThreadRoleAssistant, Kind: domain.ThreadMessageResponse,
		CreatedAt: now,
		Blocks: []PlaintextBlock{
			{ID: "one", Kind: domain.ThreadBlockText, Content: []byte("first")},
			{ID: "two", Kind: domain.ThreadBlockJSON, Content: []byte(`{"second":true}`)},
		},
	}
	message, err := EncryptMessage(dek, input)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := DecryptMessage(dek, message)
	if err != nil {
		t.Fatal(err)
	}
	defer ZeroPlaintextBlocks(plaintext)
	if string(plaintext[0]) != "first" || string(plaintext[1]) != `{"second":true}` {
		t.Fatalf("plaintext blocks = %q", plaintext)
	}
	input.Blocks = []PlaintextBlock{{
		ID: "large", Kind: domain.ThreadBlockText, Content: make([]byte, MaxPlaintextBytes+1),
	}}
	if _, err := EncryptMessage(dek, input); !errors.Is(err, domain.ErrResourceExhausted) {
		t.Fatalf("message limit error = %v", err)
	}
}

func testMetadata() FrameMetadata {
	return FrameMetadata{
		ThreadID:        "thread",
		MessageID:       "message",
		MessageSequence: 1,
		BlockSequence:   1,
		Role:            domain.ThreadRoleUser,
		MessageKind:     domain.ThreadMessagePrompt,
		BlockKind:       domain.ThreadBlockText,
		CreatedAt:       "2026-08-24T12:00:00Z",
	}
}
