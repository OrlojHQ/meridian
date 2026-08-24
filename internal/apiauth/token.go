// Package apiauth owns the single-principal installation API token.
package apiauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	tokenBytes        = 32
	maxTokenFileBytes = 256
)

// Token retains only the digest needed to authenticate presented token text.
type Token struct {
	digest [sha256.Size]byte
}

func DefaultTokenPath(dataDir string) string {
	return filepath.Join(dataDir, "api-auth", "installation.token")
}

// OpenOrCreateToken loads a restrictive regular token file, or atomically
// creates a new 256-bit token without replacing a concurrent creator.
func OpenOrCreateToken(path string) (*Token, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("API token path is required")
	}
	parent := filepath.Dir(path)
	if err := secureDirectory(parent, true); err != nil {
		return nil, err
	}
	token, err := LoadToken(path)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	random := make([]byte, tokenBytes)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return nil, fmt.Errorf("generate API token: %w", err)
	}
	text := base64.RawURLEncoding.EncodeToString(random)
	zero(random)
	content := []byte(text + "\n")
	defer zero(content)
	if err := publishToken(path, content); err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadToken(path)
		}
		return nil, err
	}
	return ParseToken(text)
}

func LoadToken(path string) (*Token, error) {
	value, err := ReadTokenFile(path)
	if err != nil {
		return nil, err
	}
	return ParseToken(value)
}

// ReadTokenFile reads validated token text for an API client.
func ReadTokenFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("API token path is required")
	}
	if err := secureDirectory(filepath.Dir(path), false); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("API token path must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return "", errors.New("API token file permissions must be 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("API token file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxTokenFileBytes+1))
	if err != nil {
		return "", err
	}
	defer zero(raw)
	if len(raw) > maxTokenFileBytes {
		return "", errors.New("API token file exceeds limit")
	}
	value := strings.TrimSpace(string(raw))
	if _, err := ParseToken(value); err != nil {
		return "", err
	}
	return value, nil
}

func ParseToken(value string) (*Token, error) {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return nil, errors.New("API token is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != tokenBytes {
		zero(decoded)
		return nil, errors.New("API token is invalid")
	}
	zero(decoded)
	return &Token{digest: sha256.Sum256([]byte(value))}, nil
}

// Matches compares a presented token in constant time after fixed-size hashing.
func (t *Token) Matches(value string) bool {
	presented := sha256.Sum256([]byte(value))
	if t == nil {
		var empty [sha256.Size]byte
		return subtle.ConstantTimeCompare(presented[:], empty[:]) == 1 && false
	}
	return subtle.ConstantTimeCompare(presented[:], t.digest[:]) == 1
}

func publishToken(path string, content []byte) error {
	parent := filepath.Dir(path)
	temp, err := os.CreateTemp(parent, ".installation-token-")
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

func secureDirectory(path string, create bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if create {
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return fmt.Errorf("create API token directory: %w", err)
		}
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("API token directory path is unsafe")
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("API token directory permissions must be 0700")
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
