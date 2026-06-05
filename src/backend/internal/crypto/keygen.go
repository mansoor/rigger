package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"

	"golang.org/x/crypto/ssh"
)

// GenerateSSHKeypair creates a new ed25519 SSH keypair for the Rigger-managed host
// identity. It returns the public key as a single authorized_keys line and the
// private key as unencrypted OpenSSH PEM bytes (the caller encrypts it at rest).
func GenerateSSHKeypair(comment string) (authorizedLine string, privatePEM []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", nil, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	return line, pem.EncodeToMemory(block), nil
}
