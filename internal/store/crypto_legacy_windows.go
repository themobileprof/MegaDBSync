//go:build windows

package store

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
)

// legacyDecryptPassword decrypts passwords stored before DPAPI migration (AES + .key file).
func legacyDecryptPassword(dataDir, encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	key, err := loadLegacyKey(dataDir)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid ciphertext")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func loadLegacyKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, ".key")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("invalid legacy key")
	}
	return b, nil
}
