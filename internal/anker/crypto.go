// Package anker talks to the Anker cloud API.
package anker

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// ServerPublicKeyHex is the Anker server's uncompressed P-256 public key,
// shared by the EU and COM servers.
const ServerPublicKeyHex = "04c5c00c4f8d1197cc7c3167c52bf7acb054d722f0ef08dcd7e0883236e0d72a3868d9750cb47fa4619248f3d83f0f662671dadc6e2d31c2f41db0161651c7c076"

// KeyExchange holds a client key pair and the ECDH secret shared with the server.
type KeyExchange struct {
	pubHex string
	shared []byte
}

// NewKeyExchange generates a client P-256 key pair and derives the shared
// secret with the hex-encoded uncompressed server public key.
func NewKeyExchange(serverPubHex string) (*KeyExchange, error) {
	raw, err := hex.DecodeString(serverPubHex)
	if err != nil {
		return nil, fmt.Errorf("decode server public key: %w", err)
	}
	curve := ecdh.P256()
	serverPub, err := curve.NewPublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse server public key: %w", err)
	}
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate client key: %w", err)
	}
	shared, err := priv.ECDH(serverPub)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}
	return &KeyExchange{pubHex: hex.EncodeToString(priv.PublicKey().Bytes()), shared: shared}, nil
}

// PublicKeyHex returns the client public key as uncompressed-point hex.
func (k *KeyExchange) PublicKeyHex() string { return k.pubHex }

// EncryptPassword returns the password as base64 AES-256-CBC ciphertext.
// The server expects the shared secret as key and its first block as IV.
func (k *KeyExchange) EncryptPassword(pw string) (string, error) {
	block, err := aes.NewCipher(k.shared)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	data := []byte(pw)
	pad := aes.BlockSize - len(data)%aes.BlockSize
	for i := 0; i < pad; i++ {
		data = append(data, byte(pad))
	}
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, k.shared[:aes.BlockSize]).CryptBlocks(out, data)
	return base64.StdEncoding.EncodeToString(out), nil
}
