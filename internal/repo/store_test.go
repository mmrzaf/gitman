package repo

import "testing"

var testContext = secretContext("repo-1", "DEPLOY_TOKEN")

func TestEncryptDecryptSecretRoundTrip(t *testing.T) {
	ciphertext, nonce, err := encryptSecret("a very secret passphrase, at least 32 bytes long", "hunter2", testContext)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptSecret("a very secret passphrase, at least 32 bytes long", ciphertext, nonce, testContext)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Fatalf("got %q", got)
	}
}

func TestEncryptSecretIsRandomized(t *testing.T) {
	c1, n1, _ := encryptSecret("key-one-key-one-key-one-key-one", "value", testContext)
	c2, n2, _ := encryptSecret("key-one-key-one-key-one-key-one", "value", testContext)
	if string(c1) == string(c2) || string(n1) == string(n2) {
		t.Fatal("two encryptions of the same value produced the same bytes")
	}
}

func TestDecryptSecretRejectsWrongKeyAndTampering(t *testing.T) {
	ciphertext, nonce, err := encryptSecret("key-one-key-one-key-one-key-one", "value", testContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptSecret("key-two-key-two-key-two-key-two", ciphertext, nonce, testContext); err == nil {
		t.Error("expected the wrong key to fail")
	}
	tampered := append([]byte{}, ciphertext...)
	tampered[0] ^= 0xff
	if _, err := decryptSecret("key-one-key-one-key-one-key-one", tampered, nonce, testContext); err == nil {
		t.Error("expected tampered ciphertext to fail")
	}
}

// TestDecryptSecretRejectsAMovedValue covers someone with write access
// to the database copying one secret's sealed value to another
// repository or another name: it must not decrypt there.
func TestDecryptSecretRejectsAMovedValue(t *testing.T) {
	const key = "key-one-key-one-key-one-key-one"
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
