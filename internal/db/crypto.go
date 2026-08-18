package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	secretFormatV1   = "v1:"
	saltSize         = 16
	pbkdf2Iterations = 100_000
	keySize          = 32
)

// EncryptSecret writes the versioned Gitman secret format. Versioning keeps
// cryptographic format changes local to this codec instead of leaking storage
// assumptions through handlers and workers.
func EncryptSecret(passphrase, plaintext string) (string, error) {
	payload, err := encryptSecretPayload(passphrase, plaintext)
	if err != nil {
		return "", err
	}
	return secretFormatV1 + payload, nil
}

func encryptSecretPayload(passphrase, plaintext string) (string, error) {
	if passphrase == "" {
		return "", fmt.Errorf("GITMAN_SECRET_KEY is not configured")
	}
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iterations, keySize, sha256.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("initialize cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("initialize GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	result := make([]byte, len(salt)+len(nonce)+len(ciphertext))
	copy(result, salt)
	copy(result[len(salt):], nonce)
	copy(result[len(salt)+len(nonce):], ciphertext)
	return base64.StdEncoding.EncodeToString(result), nil
}

// DecryptSecret accepts the current v1 format and the unversioned legacy
// format. New writes are always versioned, so legacy
// values naturally disappear as secrets are updated.
func DecryptSecret(passphrase, encoded string) (string, error) {
	if passphrase == "" {
		return "", fmt.Errorf("GITMAN_SECRET_KEY is not configured")
	}
	payload := encoded
	if strings.HasPrefix(encoded, secretFormatV1) {
		payload = strings.TrimPrefix(encoded, secretFormatV1)
	} else if strings.Contains(encoded, ":") {
		return "", fmt.Errorf("unsupported secret ciphertext version")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	if len(data) < saltSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	salt := data[:saltSize]
	nonceAndCT := data[saltSize:]

	key := pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iterations, keySize, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("initialize cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("initialize GCM: %w", err)
	}
	if len(nonceAndCT) < gcm.NonceSize()+gcm.Overhead() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce := nonceAndCT[:gcm.NonceSize()]
	ciphertext := nonceAndCT[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}
