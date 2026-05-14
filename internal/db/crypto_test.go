package db

import (
	"encoding/base64"
	"testing"
)

func TestEncryptDecryptSecret(t *testing.T) {
	passphrase := "my-secret-key-1234"
	plaintext := "my-sensitive-value"

	enc, err := EncryptSecret(passphrase, plaintext)
	if err != nil {
		t.Fatalf("EncryptSecret failed: %v", err)
	}

	dec, err := DecryptSecret(passphrase, enc)
	if err != nil {
		t.Fatalf("DecryptSecret failed: %v", err)
	}
	if dec != plaintext {
		t.Errorf("decrypted value = %q, want %q", dec, plaintext)
	}
}

func TestEncryptSecretEmptyPassphrase(t *testing.T) {
	_, err := EncryptSecret("", "value")
	if err == nil {
		t.Error("expected error for empty passphrase")
	}
}

func TestDecryptSecretWrongPassphrase(t *testing.T) {
	enc, err := EncryptSecret("correct", "value")
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecryptSecret("wrong", enc)
	if err == nil {
		t.Error("expected error for wrong passphrase")
	}
}

func TestDecryptSecretInvalidBase64(t *testing.T) {
	_, err := DecryptSecret("key", "not-base64!@#")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestDecryptSecretCorrupted(t *testing.T) {
	enc, _ := EncryptSecret("key", "value")
	data, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("DecodeString failed: %v", err)
	}
	data[len(data)-1] ^= 0xFF
	enc = base64.StdEncoding.EncodeToString(data)
	_, err = DecryptSecret("key", enc)
	if err == nil {
		t.Error("expected error for corrupted ciphertext")
	}
}
