// Package transcripts provides bounded envelope encryption for explicit
// structured agent transcripts. It never persists or formats plaintext.
package transcripts

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
)

const (
	EnvelopeVersion          uint16 = 1
	KeyFormatVersion                = 1
	MaxPlaintextBytes               = 1 << 20
	MaxFrameBytes                   = MaxPlaintextBytes + 64
	MaxBlocksPerMessage             = 128
	MaxMessagePlaintextBytes        = 4 << 20
	MaxMessagesPerThread     int64  = 100_000
	MaxThreadEncryptedBytes  int64  = 256 << 20
	maxKeyFileBytes                 = 4 << 10
)

var (
	frameMagic = [4]byte{'M', 'T', 'F', '1'}
	dekMagic   = [4]byte{'M', 'D', 'K', '1'}
)

type InstallationKey struct {
	ID       string
	Version  uint32
	key      [32]byte
	previous *keyMaterial
}

type keyFile struct {
	Format   string      `json:"format"`
	ID       string      `json:"id"`
	Version  uint32      `json:"version"`
	Key      string      `json:"key"`
	Previous *encodedKey `json:"previous,omitempty"`
}

type encodedKey struct {
	ID      string `json:"id"`
	Version uint32 `json:"version"`
	Key     string `json:"key"`
}

type keyMaterial struct {
	ID      string
	Version uint32
	Key     [32]byte
}

type FrameMetadata struct {
	ThreadID        domain.ThreadID
	MessageID       domain.ThreadMessageID
	MessageSequence int64
	BlockSequence   int64
	Role            domain.ThreadMessageRole
	MessageKind     domain.ThreadMessageKind
	BlockKind       domain.ThreadBlockKind
	CreatedAt       string
}

func DefaultKeyPath(dataDir string) string {
	return filepath.Join(dataDir, "transcript-keys", "installation.key")
}

// OpenOrCreateKey loads a restrictive regular key file, or atomically creates
// one without replacing a concurrent creator.
func OpenOrCreateKey(path string) (*InstallationKey, error) {
	if err := secureParent(filepath.Dir(path), true); err != nil {
		return nil, err
	}
	key, err := LoadKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err = NewInstallationKey("", 1)
	if err != nil {
		return nil, err
	}
	if err := publishNewKey(path, key); err != nil {
		key.Zero()
		if errors.Is(err, os.ErrExist) {
			return LoadKey(path)
		}
		return nil, err
	}
	return key, nil
}

// NewInstallationKey creates key material for explicit rotation workflows.
func NewInstallationKey(id string, version uint32) (*InstallationKey, error) {
	if version == 0 {
		return nil, fmt.Errorf("%w: invalid transcript key metadata", domain.ErrInvalid)
	}
	if id == "" {
		randomID := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, randomID); err != nil {
			return nil, fmt.Errorf("generate transcript key identifier: %w", err)
		}
		id = hex.EncodeToString(randomID)
		zero(randomID)
	}
	if !validKeyID(id) {
		return nil, fmt.Errorf("%w: invalid transcript key metadata", domain.ErrInvalid)
	}
	key := &InstallationKey{ID: id, Version: version}
	if _, err := io.ReadFull(rand.Reader, key.key[:]); err != nil {
		key.Zero()
		return nil, fmt.Errorf("generate transcript installation key: %w", err)
	}
	return key, nil
}

// WriteNewKey publishes a newly generated key without replacing an existing
// installation key. It is intended for separately protected rotation targets.
func WriteNewKey(path string, key *InstallationKey) error {
	if key == nil || !key.valid() {
		return fmt.Errorf("%w: invalid transcript key", domain.ErrInvalid)
	}
	if err := secureParent(filepath.Dir(path), true); err != nil {
		return err
	}
	return publishNewKey(path, key)
}

// ActivateTransition atomically replaces an existing restrictive key file with
// a transitional keyring. New wraps use newKey while old and new envelopes can
// both be opened until ActivateKey retires the old material.
func ActivateTransition(path string, newKey, oldKey *InstallationKey) error {
	if newKey == nil || oldKey == nil || !newKey.valid() || !oldKey.valid() ||
		(newKey.ID == oldKey.ID && newKey.Version == oldKey.Version) {
		return fmt.Errorf("%w: invalid transcript key transition", domain.ErrInvalid)
	}
	encoded := keyFile{
		Format: "meridian.transcript-key.transition.v1",
		ID:     newKey.ID, Version: newKey.Version,
		Key: base64.StdEncoding.EncodeToString(newKey.key[:]),
		Previous: &encodedKey{
			ID: oldKey.ID, Version: oldKey.Version,
			Key: base64.StdEncoding.EncodeToString(oldKey.key[:]),
		},
	}
	return replaceKeyFile(path, encoded)
}

// ActivateKey atomically publishes one active key and removes transitional old
// key material from the live key file.
func ActivateKey(path string, key *InstallationKey) error {
	if key == nil || !key.valid() {
		return fmt.Errorf("%w: invalid transcript key", domain.ErrInvalid)
	}
	return replaceKeyFile(path, encodedKeyFile(key))
}

func LoadKey(path string) (*InstallationKey, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: transcript key path is required", domain.ErrInvalid)
	}
	if err := secureParent(filepath.Dir(path), false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("transcript key path must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, errors.New("transcript key file permissions must be 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("transcript key file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxKeyFileBytes+1))
	if err != nil {
		return nil, err
	}
	defer zero(raw)
	if len(raw) > maxKeyFileBytes {
		return nil, errors.New("transcript key file exceeds limit")
	}
	var encoded keyFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return nil, errors.New("transcript key file is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("transcript key file is invalid")
	}
	if (encoded.Format != "meridian.transcript-key.v1" &&
		encoded.Format != "meridian.transcript-key.transition.v1") ||
		encoded.Version == 0 || !validKeyID(encoded.ID) ||
		(encoded.Format == "meridian.transcript-key.v1" && encoded.Previous != nil) {
		return nil, errors.New("transcript key file is invalid")
	}
	material, err := base64.StdEncoding.DecodeString(encoded.Key)
	if err != nil || len(material) != 32 {
		zero(material)
		return nil, errors.New("transcript key file is invalid")
	}
	defer zero(material)
	key := &InstallationKey{ID: encoded.ID, Version: encoded.Version}
	copy(key.key[:], material)
	if encoded.Format == "meridian.transcript-key.transition.v1" {
		if encoded.Previous == nil || !validKeyID(encoded.Previous.ID) ||
			encoded.Previous.Version == 0 ||
			(encoded.Previous.ID == encoded.ID && encoded.Previous.Version == encoded.Version) {
			key.Zero()
			return nil, errors.New("transcript key file is invalid")
		}
		previous, decodeErr := base64.StdEncoding.DecodeString(encoded.Previous.Key)
		if decodeErr != nil || len(previous) != 32 {
			zero(previous)
			key.Zero()
			return nil, errors.New("transcript key file is invalid")
		}
		key.previous = &keyMaterial{ID: encoded.Previous.ID, Version: encoded.Previous.Version}
		copy(key.previous.Key[:], previous)
		zero(previous)
	}
	return key, nil
}

func (k *InstallationKey) Zero() {
	if k != nil {
		zero(k.key[:])
		if k.previous != nil {
			zero(k.previous.Key[:])
			k.previous = nil
		}
	}
}

func (k *InstallationKey) Matches(id string, version uint32) bool {
	if k == nil {
		return false
	}
	if version == k.Version && len(id) == len(k.ID) &&
		subtle.ConstantTimeCompare([]byte(id), []byte(k.ID)) == 1 {
		return true
	}
	return k.previous != nil && version == k.previous.Version &&
		len(id) == len(k.previous.ID) &&
		subtle.ConstantTimeCompare([]byte(id), []byte(k.previous.ID)) == 1
}

func (k *InstallationKey) Metadata() (string, uint32) {
	if k == nil {
		return "", 0
	}
	return k.ID, k.Version
}

// Transitional reports whether the loaded restrictive key file retains a
// previous KEK for crash recovery during offline rotation.
func (k *InstallationKey) Transitional() bool {
	return k != nil && k.previous != nil
}

func (k *InstallationKey) NewDEK() ([]byte, error) {
	if !k.valid() {
		return nil, fmt.Errorf("%w: invalid transcript key", domain.ErrInvalid)
	}
	dek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		zero(dek)
		return nil, fmt.Errorf("generate thread data key: %w", err)
	}
	return dek, nil
}

func (k *InstallationKey) WrapDEK(threadID domain.ThreadID, dek []byte) ([]byte, error) {
	if !k.valid() || threadID == "" || len(dek) != 32 {
		return nil, fmt.Errorf("%w: invalid transcript key envelope", domain.ErrInvalid)
	}
	aead, err := k.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate thread key nonce: %w", err)
	}
	out := make([]byte, 4+len(nonce), 4+len(nonce)+len(dek)+aead.Overhead())
	copy(out, dekMagic[:])
	copy(out[4:], nonce)
	out = aead.Seal(out, nonce, dek, wrappedAAD(threadID, k.ID, k.Version))
	zero(nonce)
	return out, nil
}

func (k *InstallationKey) UnwrapDEK(
	threadID domain.ThreadID,
	keyID string,
	keyVersion uint32,
	wrapped []byte,
) ([]byte, error) {
	if !k.Matches(keyID, keyVersion) {
		return nil, domain.ErrKeyMismatch
	}
	if threadID == "" || len(wrapped) != 4+12+32+16 ||
		subtle.ConstantTimeCompare(wrapped[:4], dekMagic[:]) != 1 {
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
	plaintext, err := aead.Open(nil, wrapped[4:16], wrapped[16:], wrappedAAD(threadID, keyID, keyVersion))
	if err != nil || len(plaintext) != 32 {
		zero(plaintext)
		return nil, domain.ErrTranscriptCorrupt
	}
	return plaintext, nil
}

// RewrapDEK authenticates with the old KEK and wraps the same DEK under the
// new KEK. Callers persist the returned key metadata and envelope atomically.
func RewrapDEK(
	threadID domain.ThreadID,
	wrapped []byte,
	oldKey, newKey *InstallationKey,
) ([]byte, error) {
	if oldKey == nil || newKey == nil {
		return nil, fmt.Errorf("%w: missing transcript rotation key", domain.ErrInvalid)
	}
	dek, err := oldKey.UnwrapDEK(threadID, oldKey.ID, oldKey.Version, wrapped)
	if err != nil {
		return nil, err
	}
	defer zero(dek)
	return newKey.WrapDEK(threadID, dek)
}

func Seal(dek []byte, metadata FrameMetadata, plaintext []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintextBytes {
		return nil, fmt.Errorf("%w: transcript frame exceeds limit", domain.ErrResourceExhausted)
	}
	if len(dek) != 32 || !metadata.valid() {
		return nil, fmt.Errorf("%w: invalid transcript frame metadata", domain.ErrInvalid)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("initialize transcript cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize transcript envelope: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate transcript nonce: %w", err)
	}
	out := make([]byte, 4+len(nonce), 4+len(nonce)+len(plaintext)+aead.Overhead())
	copy(out, frameMagic[:])
	copy(out[4:], nonce)
	out = aead.Seal(out, nonce, plaintext, metadata.aad())
	zero(nonce)
	return out, nil
}

func Open(dek []byte, metadata FrameMetadata, envelope []byte) ([]byte, error) {
	if len(dek) != 32 || !metadata.valid() {
		return nil, fmt.Errorf("%w: invalid transcript frame metadata", domain.ErrInvalid)
	}
	if len(envelope) < 4+12+16 || len(envelope) > MaxFrameBytes ||
		subtle.ConstantTimeCompare(envelope[:4], frameMagic[:]) != 1 {
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
	plaintext, err := aead.Open(nil, envelope[4:16], envelope[16:], metadata.aad())
	if err != nil || len(plaintext) > MaxPlaintextBytes {
		zero(plaintext)
		return nil, domain.ErrTranscriptCorrupt
	}
	return plaintext, nil
}

func ValidateEnvelope(envelope []byte) error {
	if len(envelope) < 4+12+16 || len(envelope) > MaxFrameBytes ||
		subtle.ConstantTimeCompare(envelope[:4], frameMagic[:]) != 1 {
		return domain.ErrTranscriptCorrupt
	}
	return nil
}

func (k *InstallationKey) valid() bool {
	if k == nil || !validKeyID(k.ID) || k.Version == 0 {
		return false
	}
	var combined byte
	for _, value := range k.key {
		combined |= value
	}
	return combined != 0
}

func validKeyID(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func (k *InstallationKey) aead() (cipher.AEAD, error) {
	return aeadFor(k.key[:])
}

func aeadFor(material []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, fmt.Errorf("initialize transcript key cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

func (m FrameMetadata) valid() bool {
	return m.ThreadID != "" && m.MessageID != "" &&
		m.MessageSequence > 0 && m.BlockSequence > 0 &&
		m.Role.Valid() && m.MessageKind.Valid() && m.BlockKind.Valid() &&
		m.CreatedAt != "" && len(m.CreatedAt) <= 64
}

func (m FrameMetadata) aad() []byte {
	buffer := make([]byte, 0, 256)
	buffer = append(buffer, "meridian/transcript-frame/v1"...)
	buffer = appendString(buffer, string(m.ThreadID))
	buffer = appendString(buffer, string(m.MessageID))
	buffer = binary.BigEndian.AppendUint64(buffer, uint64(m.MessageSequence))
	buffer = binary.BigEndian.AppendUint64(buffer, uint64(m.BlockSequence))
	buffer = appendString(buffer, string(m.Role))
	buffer = appendString(buffer, string(m.MessageKind))
	buffer = appendString(buffer, string(m.BlockKind))
	return appendString(buffer, m.CreatedAt)
}

func wrappedAAD(threadID domain.ThreadID, keyID string, version uint32) []byte {
	buffer := []byte("meridian/thread-dek/v1")
	buffer = appendString(buffer, string(threadID))
	buffer = appendString(buffer, keyID)
	return binary.BigEndian.AppendUint32(buffer, version)
}

func appendString(destination []byte, value string) []byte {
	destination = binary.BigEndian.AppendUint32(destination, uint32(len(value)))
	return append(destination, value...)
}

func publishNewKey(path string, key *InstallationKey) error {
	encoded := encodedKeyFile(key)
	content, err := json.Marshal(encoded)
	if err != nil {
		return err
	}
	defer zero(content)
	content = append(content, '\n')
	parent := filepath.Dir(path)
	temp, err := os.CreateTemp(parent, ".installation-key-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, path); err != nil {
		return err
	}
	if err := syncDirectory(parent); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func encodedKeyFile(key *InstallationKey) keyFile {
	return keyFile{
		Format: "meridian.transcript-key.v1", ID: key.ID, Version: key.Version,
		Key: base64.StdEncoding.EncodeToString(key.key[:]),
	}
}

func replaceKeyFile(path string, encoded keyFile) error {
	if err := secureParent(filepath.Dir(path), false); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o600 {
		return errors.New("transcript key path must be a restrictive regular file")
	}
	content, err := json.Marshal(encoded)
	if err != nil {
		return err
	}
	defer zero(content)
	content = append(content, '\n')
	parent := filepath.Dir(path)
	temp, err := os.CreateTemp(parent, ".installation-key-activate-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func secureParent(path string, create bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if create {
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return fmt.Errorf("create transcript key directory: %w", err)
		}
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("transcript key directory path is unsafe")
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("transcript key directory permissions must exclude group and other access")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
