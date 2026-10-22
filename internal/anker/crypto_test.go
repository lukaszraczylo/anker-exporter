package anker

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func pkcs7Unpad(t *testing.T, b []byte) []byte {
	t.Helper()
	if len(b) == 0 || len(b)%aes.BlockSize != 0 {
		t.Fatalf("bad padded length %d", len(b))
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		t.Fatalf("bad pad byte %d", n)
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			t.Fatalf("inconsistent padding")
		}
	}
	return b[:len(b)-n]
}

func TestCryptoEncryptPassword_RoundTrip(t *testing.T) {
	server, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kx, err := NewKeyExchange(hex.EncodeToString(server.PublicKey().Bytes()))
	if err != nil {
		t.Fatalf("NewKeyExchange: %v", err)
	}

	clientPubBytes, err := hex.DecodeString(kx.PublicKeyHex())
	if err != nil {
		t.Fatalf("PublicKeyHex not hex: %v", err)
	}
	if len(clientPubBytes) != 65 || clientPubBytes[0] != 0x04 {
		t.Fatalf("want uncompressed 65-byte point, got len %d", len(clientPubBytes))
	}
	clientPub, err := ecdh.P256().NewPublicKey(clientPubBytes)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := server.ECDH(clientPub)
	if err != nil {
		t.Fatal(err)
	}

	for _, pw := range []string{"hunter2", "", "exactly16bytes!!", "pässwörd with unicode and a long tail beyond a block"} {
		enc, err := kx.EncryptPassword(pw)
		if err != nil {
			t.Fatalf("EncryptPassword: %v", err)
		}
		ct, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			t.Fatalf("not standard base64: %v", err)
		}
		block, err := aes.NewCipher(secret)
		if err != nil {
			t.Fatal(err)
		}
		pt := make([]byte, len(ct))
		cipher.NewCBCDecrypter(block, secret[:16]).CryptBlocks(pt, ct)
		if got := string(pkcs7Unpad(t, pt)); got != pw {
			t.Errorf("round trip: got %q want %q", got, pw)
		}
	}
}

func TestCryptoNewKeyExchange_RejectsBadKey(t *testing.T) {
	for name, in := range map[string]string{
		"not hex":   "zz-not-hex",
		"empty":     "",
		"short":     "04abcd",
		"off curve": "04" + hex.EncodeToString(make([]byte, 64)),
	} {
		if _, err := NewKeyExchange(in); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCryptoServerPublicKeyHex_Valid(t *testing.T) {
	if _, err := NewKeyExchange(ServerPublicKeyHex); err != nil {
		t.Fatalf("embedded server key invalid: %v", err)
	}
}
