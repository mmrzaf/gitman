package repo

import "testing"

var testContext = secretContext("repo-1", "DEPLOY_TOKEN")

func TestEncryptDecryptSecretRoundTrip(t *testing.T) {
	ciphertext, nonce, err := encryptSecret("STsEYlF+KWuLHwa+R+yP7w5HqEwoKF2zUqpbukDA9PE=", "hunter2", testContext)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptSecret("STsEYlF+KWuLHwa+R+yP7w5HqEwoKF2zUqpbukDA9PE=", ciphertext, nonce, testContext)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Fatalf("got %q", got)
	}
}

func TestEncryptSecretIsRandomized(t *testing.T) {
	c1, n1, _ := encryptSecret("aZ0HdzeixAfXnJmZtgMu6+oqDkEyQ1D/mnybE/SjW+0=", "value", testContext)
	c2, n2, _ := encryptSecret("aZ0HdzeixAfXnJmZtgMu6+oqDkEyQ1D/mnybE/SjW+0=", "value", testContext)
	if string(c1) == string(c2) || string(n1) == string(n2) {
		t.Fatal("two encryptions of the same value produced the same bytes")
	}
}

func TestDecryptSecretRejectsWrongKeyAndTampering(t *testing.T) {
	ciphertext, nonce, err := encryptSecret("aZ0HdzeixAfXnJmZtgMu6+oqDkEyQ1D/mnybE/SjW+0=", "value", testContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptSecret("Sj2wRbpzaKQCqRV1JvWXgsq3kYw1UlpPHNNxuHHm+lw=", ciphertext, nonce, testContext); err == nil {
		t.Error("expected the wrong key to fail")
	}
	tampered := append([]byte{}, ciphertext...)
	tampered[0] ^= 0xff
	if _, err := decryptSecret("aZ0HdzeixAfXnJmZtgMu6+oqDkEyQ1D/mnybE/SjW+0=", tampered, nonce, testContext); err == nil {
		t.Error("expected tampered ciphertext to fail")
	}
}

// TestDecryptSecretRejectsAMovedValue covers someone with write access
// to the database copying one secret's sealed value to another
// repository or another name: it must not decrypt there.
func TestDecryptSecretRejectsAMovedValue(t *testing.T) {
	const key = "aZ0HdzeixAfXnJmZtgMu6+oqDkEyQ1D/mnybE/SjW+0="
	ciphertext, nonce, err := encryptSecret(key, "value", secretContext("repo-1", "DEPLOY_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	for _, moved := range [][]byte{secretContext("repo-2", "DEPLOY_TOKEN"), secretContext("repo-1", "OTHER")} {
		if _, err := decryptSecret(key, ciphertext, nonce, moved); err == nil {
			t.Errorf("a value moved to %q decrypted", moved)
		}
	}
}

func TestEncryptDecryptSecretRequireKey(t *testing.T) {
	if _, _, err := encryptSecret("", "value", testContext); err == nil {
		t.Error("expected an empty key to be rejected")
	}
	if _, err := decryptSecret("", []byte("x"), []byte("y"), testContext); err == nil {
		t.Error("expected an empty key to be rejected")
	}
}

func TestDecryptRejectsMalformedStructure(t *testing.T) {
	const key = "aZ0HdzeixAfXnJmZtgMu6+oqDkEyQ1D/mnybE/SjW+0="
	ciphertext, nonce, err := encryptSecret(key, "value", testContext)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 1, 11, 13, 100} {
		if _, err := decryptSecret(key, ciphertext, make([]byte, size), testContext); err == nil {
			t.Errorf("accepted nonce length %d", size)
		}
	}
	for size := 0; size < 16; size++ {
		if _, err := decryptSecret(key, make([]byte, size), nonce, testContext); err == nil {
			t.Errorf("accepted ciphertext length %d", size)
		}
	}
}
