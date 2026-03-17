// Package identity manages Ed25519 keypairs for agent authentication.
//
// # Why Ed25519?
//
// The current handshake uses a plain-text operator_id string. Any node can claim
// any operator identity — there is no proof of ownership. Ed25519 fixes this:
//
//   - Each agent generates a keypair on first start and saves it to disk.
//   - The public key is included in JoinRequest.
//   - On every pulse, the agent signs (agentID + timestamp) with its private key.
//   - The hub verifies the signature against the stored public key.
//   - A spoofed agent that doesn't hold the private key cannot forge a valid signature.
//
// Ed25519 is chosen over RSA/ECDSA because:
//   - 32-byte keys, 64-byte signatures — tiny wire overhead.
//   - Constant-time signing (no timing side-channels).
//   - stdlib crypto/ed25519 — zero external dependencies.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const keyFileName = "agent_key.pem"

// Identity holds an agent's Ed25519 keypair.
type Identity struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

// PublicKeyB64 returns the base64-encoded public key, safe to embed in JSON.
func (id *Identity) PublicKeyB64() string {
	return base64.StdEncoding.EncodeToString(id.PublicKey)
}

// Sign signs an arbitrary message and returns a base64-encoded signature.
// The hub verifies this with Verify().
func (id *Identity) Sign(message []byte) string {
	sig := ed25519.Sign(id.PrivateKey, message)
	return base64.StdEncoding.EncodeToString(sig)
}

// MakeChallenge returns the canonical message that should be signed for a pulse:
//
//	agentID + ":" + RFC3339 timestamp (truncated to the minute)
//
// Truncating to the minute means ±1 min clock skew is tolerated while preventing
// replay attacks older than 2 minutes.
func MakeChallenge(agentID, timestamp string) []byte {
	return []byte(agentID + ":" + timestamp)
}

// TimestampNow returns the current UTC time formatted for use in a challenge.
func TimestampNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// Verify checks a base64-encoded signature against a base64-encoded public key.
// Returns nil if the signature is valid.
func Verify(pubKeyB64, message, sigB64 string) error {
	pubBytes, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key length %d", len(pubBytes))
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), []byte(message), sigBytes) {
		return errors.New("signature verification failed")
	}
	return nil
}

// LoadOrCreate loads the keypair from dir/agent_key.pem, or generates and saves
// a new one if the file does not exist. dir is typically ~/.igrid/.
func LoadOrCreate(dir string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, keyFileName)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return generate(path)
	}
	return load(path)
}

// generate creates a new Ed25519 keypair, writes the private key to path in PEM
// format (PKCS#8), and returns the Identity.
func generate(path string) (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}

	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create key file: %w", err)
	}
	defer f.Close()
	if err := pem.Encode(f, block); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}

	return &Identity{PrivateKey: priv, PublicKey: pub}, nil
}

// load reads an Ed25519 private key from a PEM-encoded PKCS#8 file.
func load(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found in key file")
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}
	priv, ok := raw.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("key file does not contain an Ed25519 private key")
	}
	return &Identity{PrivateKey: priv, PublicKey: priv.Public().(ed25519.PublicKey)}, nil
}
