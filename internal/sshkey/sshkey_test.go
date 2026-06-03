package sshkey

import (
	"bytes"
	"testing"

	"golang.org/x/crypto/ssh"
)

// The session create request carries PublicKeyOpenSSH; slice 4 dials with
// Signer. The two must describe the same key, or OCI authorizes a key the
// plugin cannot then present.
func TestGenerate_PublicKeyMatchesSigner(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(kp.PublicKeyOpenSSH))
	if err != nil {
		t.Fatalf("PublicKeyOpenSSH is not valid OpenSSH authorized-key text: %v", err)
	}

	if !bytes.Equal(parsed.Marshal(), kp.Signer.PublicKey().Marshal()) {
		t.Error("Signer's public key does not match PublicKeyOpenSSH")
	}
}
