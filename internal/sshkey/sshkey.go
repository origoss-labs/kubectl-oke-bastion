// Package sshkey mints the throwaway SSH keypair that authorizes one bastion
// session. The public half travels in the session create request; the Signer
// is what slice 4 presents when it dials the port-forward. The key is never
// persisted — it lives only as long as the session it serves (the "ephemeral
// key" in CONTEXT.md).
package sshkey

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// rsaBits is 4096 for maximum OCI Bastion compatibility; key generation happens
// once per invocation, so the cost is irrelevant.
const rsaBits = 4096

// KeyPair is one ephemeral SSH key in the two forms the lifecycle needs:
// PublicKeyOpenSSH for the create request, Signer for dialing.
type KeyPair struct {
	// Signer signs the SSH handshake when dialing the bastion.
	Signer ssh.Signer
	// PublicKeyOpenSSH is the public key in authorized-keys text, as OCI's
	// PublicKeyContent expects.
	PublicKeyOpenSSH string
}

// Generate mints a fresh RSA-4096 keypair.
func Generate() (KeyPair, error) {
	priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generating RSA key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return KeyPair{}, fmt.Errorf("building SSH signer: %w", err)
	}
	return KeyPair{
		Signer:           signer,
		PublicKeyOpenSSH: string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
	}, nil
}
