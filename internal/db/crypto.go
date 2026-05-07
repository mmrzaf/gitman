package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

const (
	saltSize         = 16
	pbkdf2Iterations = 100_000
	keySize          = 32
)

// EncryptSecret encrypts with AES-256-GCM, using PBKDF2 for key derivation.
// Output format: base64(Salt + Nonce + Ciphertext)
func EncryptSecret(passphrase, plaintext string) (string, error) {
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
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	result := make([]byte, len(salt)+len(nonce)+len(ciphertext))
	copy(result, salt)
	copy(result[len(salt):], nonce)
	copy(result[len(salt)+len(nonce):], ciphertext)
	return base64.StdEncoding.EncodeToString(result), nil
}

// DecryptSecret decrypts a value produced by EncryptSecret.
func DecryptSecret(passphrase, encoded string) (string, error) {
	if passphrase == "" {
		return "", fmt.Errorf("GITMAN_SECRET_KEY is not configured")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if len(data) < saltSize+12 {
		return "", fmt.Errorf("ciphertext too short")
	}
	salt := data[:saltSize]
	nonceAndCT := data[saltSize:]
	nonce := nonceAndCT[:12]
	ciphertext := nonceAndCT[12:]

	key := pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iterations, keySize, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}
