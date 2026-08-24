// Package secrets provides the installation credential KEK and authenticated
// encryption for purpose-scoped named secrets.
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
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
	EnvelopeVersion uint16 = 1
	KeyVersion      uint32 = 1
	maxKeyFileBytes        = 4096
	MaxValueBytes          = 256 << 10
)

type InstallationKey struct {
	ID      string
	Version uint32
	key     [32]byte
}

type keyFile struct {
	Format  string `json:"format"`
	ID      string `json:"id"`
	Version uint32 `json:"version"`
	Key     string `json:"key"`
}

type Payload struct {
	Value    string `json:"value,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func DefaultKeyPath(dataDir string) string {
	return filepath.Join(dataDir, "secret-keys", "installation.key")
}

// OpenOrCreateKey loads an existing key. Creation may be disabled when the
// database already contains envelopes, preventing silent key replacement.
func OpenOrCreateKey(path string, allowCreate bool) (*InstallationKey, error) {
	key, err := LoadKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !allowCreate {
		return nil, fmt.Errorf("%w: credential key is missing for stored secrets", domain.ErrKeyMismatch)
	}
	if err := secureParent(filepath.Dir(path), true); err != nil {
		return nil, err
	}
	key, err = newKey()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(keyFile{
		Format: "meridian.secret-key.v1", ID: key.ID, Version: key.Version,
		Key: base64.StdEncoding.EncodeToString(key.key[:]),
	})
	if err != nil {
		key.Zero()
		return nil, err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".installation-key-*")
	if err != nil {
		key.Zero()
		return nil, err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err == nil {
		_, err = temp.Write(raw)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Link(name, path)
	}
	for i := range raw {
		raw[i] = 0
	}
	if errors.Is(err, os.ErrExist) {
		key.Zero()
		return LoadKey(path)
	}
	if err != nil {
		key.Zero()
		return nil, err
	}
	if directory, openErr := os.Open(filepath.Dir(path)); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return key, nil
}

func LoadKey(path string) (*InstallationKey, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: credential key path is required", domain.ErrInvalid)
	}
	if err := secureParent(filepath.Dir(path), false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return nil, errors.New("credential key must be a regular 0600 file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("credential key changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxKeyFileBytes+1))
	if err != nil {
		return nil, err
	}
	defer zero(raw)
	if len(raw) > maxKeyFileBytes {
		return nil, errors.New("credential key file exceeds limit")
	}
	var encoded keyFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&encoded) != nil || encoded.Format != "meridian.secret-key.v1" ||
		encoded.Version == 0 || !validID(encoded.ID) {
		return nil, errors.New("credential key file is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("credential key file is invalid")
	}
	material, err := base64.StdEncoding.DecodeString(encoded.Key)
	if err != nil || len(material) != 32 {
		zero(material)
		return nil, errors.New("credential key file is invalid")
	}
	key := &InstallationKey{ID: encoded.ID, Version: encoded.Version}
	copy(key.key[:], material)
	zero(material)
	return key, nil
}

func newKey() (*InstallationKey, error) {
	id := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, id); err != nil {
		return nil, err
	}
	key := &InstallationKey{ID: hex.EncodeToString(id), Version: KeyVersion}
	zero(id)
	if _, err := io.ReadFull(rand.Reader, key.key[:]); err != nil {
		key.Zero()
		return nil, err
	}
	return key, nil
}

func (k *InstallationKey) Seal(metadata domain.Secret, payload Payload) ([]byte, []byte, error) {
	if k == nil || metadata.ID == "" || metadata.Name == "" || !metadata.Purpose.Valid() {
		return nil, nil, domain.ErrInvalid
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	defer zero(plain)
	if len(plain) > MaxValueBytes {
		return nil, nil, fmt.Errorf("%w: secret value exceeds limit", domain.ErrInvalid)
	}
	block, err := aes.NewCipher(k.key[:])
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plain, aad(metadata, EnvelopeVersion)), nil
}

func (k *InstallationKey) Open(secret domain.Secret) (Payload, error) {
	if k == nil || secret.KEKID != k.ID || secret.KEKVersion != k.Version ||
		secret.EnvelopeVersion != EnvelopeVersion {
		return Payload{}, domain.ErrKeyMismatch
	}
	block, err := aes.NewCipher(k.key[:])
	if err != nil {
		return Payload{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Payload{}, err
	}
	plain, err := aead.Open(nil, secret.Nonce, secret.Ciphertext, aad(secret, secret.EnvelopeVersion))
	if err != nil {
		return Payload{}, domain.ErrKeyMismatch
	}
	defer zero(plain)
	var payload Payload
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil {
		return Payload{}, domain.ErrCorrupt
	}
	return payload, nil
}

func (k *InstallationKey) Zero() {
	if k != nil {
		zero(k.key[:])
	}
}

func aad(secret domain.Secret, version uint16) []byte {
	return []byte(fmt.Sprintf("meridian.secret.v1\x00%s\x00%s\x00%s\x00%d\x00%d",
		secret.ID, secret.Name, secret.Purpose, version, secret.ResourceVersion))
}

func secureParent(path string, create bool) error {
	if create {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential key directory must be a real directory")
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func validID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && value == strings.ToLower(value)
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
