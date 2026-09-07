package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// SealSetup uses a separate authenticated context from named credentials.
func (k *InstallationKey) SealSetup(id string, plain []byte) ([]byte, []byte, error) {
	if k == nil || id == "" || len(plain) > 2<<20 {
		return nil, nil, errors.New("invalid setup envelope")
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
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plain, []byte("meridian.harness-setup.v1:"+id)), nil
}
func (k *InstallationKey) OpenSetup(id, keyID string, nonce, ciphertext []byte) ([]byte, error) {
	if k == nil || k.ID != keyID {
		return nil, errors.New("setup encryption key unavailable")
	}
	block, err := aes.NewCipher(k.key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, errors.New("invalid setup envelope")
	}
	return aead.Open(nil, nonce, ciphertext, []byte("meridian.harness-setup.v1:"+id))
}
