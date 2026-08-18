package db

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEncryptDecryptSecret(t *testing.T) {
	passphrase := "my-secret-key-1234"
	plaintext := "my-sensitive-value"
	enc, err := EncryptSecret(passphrase, plaintext)
	if err != nil {
		t.Fatalf("EncryptSecret failed: %v", err)
	}
	if !strings.HasPrefix(enc, secretFormatV1) {
		t.Fatalf("ciphertext %q is not versioned", enc)
	}
	dec, err := DecryptSecret(passphrase, enc)
	if err != nil {
		t.Fatalf("DecryptSecret failed: %v", err)
	}
	if dec != plaintext {
		t.Errorf("decrypted value = %q, want %q", dec, plaintext)
	}
}

func TestDecryptSecretReadsLegacyCiphertext(t *testing.T) {
	legacy, err := encryptSecretPayload("key", "legacy-value")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecryptSecret("key", legacy)
	if err != nil {
		t.Fatalf("legacy decrypt failed: %v", err)
	}
	if dec != "legacy-value" {
		t.Fatalf("got %q", dec)
	}
}

func TestEncryptSecretEmptyPassphrase(t *testing.T) {
	if _, err := EncryptSecret("", "value"); err == nil {
		t.Error("expected error for empty passphrase")
	}
}

func TestDecryptSecretWrongPassphrase(t *testing.T) {
	enc, err := EncryptSecret("correct", "value")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecryptSecret("wrong", enc); err == nil {
		t.Error("expected error for wrong passphrase")
	}
}

func TestDecryptSecretInvalidBase64(t *testing.T) {
	if _, err := DecryptSecret("key", "not-base64!@#"); err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestDecryptSecretRejectsUnknownVersion(t *testing.T) {
	if _, err := DecryptSecret("key", "v2:anything"); err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestDecryptSecretCorrupted(t *testing.T) {
	enc, _ := EncryptSecret("key", "value")
	payload := strings.TrimPrefix(enc, secretFormatV1)
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("DecodeString failed: %v", err)
	}
	data[len(data)-1] ^= 0xFF
	enc = secretFormatV1 + base64.StdEncoding.EncodeToString(data)
	if _, err = DecryptSecret("key", enc); err == nil {
		t.Error("expected error for corrupted ciphertext")
	}
}
