package crypto

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	key, err := DeriveKey([]byte("super-secret-jwt"))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nabc123\n-----END-----\n")
	enc, err := Encrypt(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if enc == string(plain) {
		t.Fatal("ciphertext equals plaintext")
	}
	got, err := Decrypt(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round-trip mismatch: %q != %q", got, plain)
	}
}

func TestDeriveKeyStable(t *testing.T) {
	a, _ := DeriveKey([]byte("secret"))
	b, _ := DeriveKey([]byte("secret"))
	if !bytes.Equal(a, b) {
		t.Error("DeriveKey not deterministic for same secret")
	}
	c, _ := DeriveKey([]byte("different"))
	if bytes.Equal(a, c) {
		t.Error("DeriveKey collided for different secrets")
	}
	if len(a) != 32 {
		t.Errorf("key length = %d, want 32", len(a))
	}
}

func TestWrongKeyFails(t *testing.T) {
	k1, _ := DeriveKey([]byte("secret-one"))
	k2, _ := DeriveKey([]byte("secret-two"))
	enc, err := Encrypt(k1, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(k2, enc); err == nil {
		t.Error("Decrypt with wrong key succeeded, want error")
	}
}

func TestTamperFails(t *testing.T) {
	key, _ := DeriveKey([]byte("secret"))
	enc, _ := Encrypt(key, []byte("payload"))
	// Flip a leading base64 char to corrupt the nonce. (Trailing chars can carry
	// don't-care padding bits that decode unchanged, so tamper near the front.)
	b := []byte(enc)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	if _, err := Decrypt(key, string(b)); err == nil {
		t.Error("Decrypt of tampered ciphertext succeeded, want error")
	}
}

func TestEmptySecretRejected(t *testing.T) {
	if _, err := DeriveKey(nil); err == nil {
		t.Error("DeriveKey(nil) succeeded, want error")
	}
}
