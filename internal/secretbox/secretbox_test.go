package secretbox

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealAndOpenUseAuthenticatedEncryption(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x42}, 32)
	box, err := New(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	nonce, ciphertext, fingerprint, err := box.Seal("proxy.primary-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "primary-secret-value") || fingerprint == "" {
		t.Fatalf("ciphertext=%q fingerprint=%q", ciphertext, fingerprint)
	}
	plaintext, err := box.Open(nonce, ciphertext)
	if err != nil || plaintext != "proxy.primary-secret-value" {
		t.Fatalf("Open()=%q, %v", plaintext, err)
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	box, err := New(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	nonce, ciphertext, _, err := box.Seal("proxy.secret")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)-1] ^= 0xff
	if _, err := box.Open(nonce, ciphertext); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestNewRejectsMissingOrWrongSizedMasterKey(t *testing.T) {
	for _, key := range [][]byte{nil, []byte("short")} {
		if _, err := New(key); err == nil {
			t.Fatalf("New(%d bytes) should fail", len(key))
		}
	}
}

func TestArtifactSigningKeyIsStableAndDomainSeparated(t *testing.T) {
	box, err := New(bytes.Repeat([]byte{0x23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	first, err := box.DeriveArtifactSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := box.DeriveArtifactSigningKey()
	callback, _ := box.DeriveCallbackSigningKey("owner")
	if !bytes.Equal(first, second) || bytes.Equal(first, callback) || len(first) != 32 {
		t.Fatalf("artifact signing key derivation is not stable or separated")
	}
}
