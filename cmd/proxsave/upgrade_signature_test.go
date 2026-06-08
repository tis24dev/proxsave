package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func testPubPEM(t *testing.T, priv *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func testSignASN1(t *testing.T, priv *ecdsa.PrivateKey, data []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(data)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

func TestVerifyReleaseSignature(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubPEM := testPubPEM(t, priv)

	dir := t.TempDir()
	checksums := []byte("abc123  proxsave_9.9.9_linux_amd64.tar.gz\n")
	checksumPath := filepath.Join(dir, "SHA256SUMS")
	if err := os.WriteFile(checksumPath, checksums, 0o644); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "SHA256SUMS.sig")
	if err := os.WriteFile(sigPath, testSignASN1(t, priv, checksums), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() { _ = root.Close() }()

	t.Run("valid signature passes", func(t *testing.T) {
		if err := verifyReleaseSignature(root, "SHA256SUMS", "SHA256SUMS.sig", pubPEM); err != nil {
			t.Fatalf("expected a valid signature, got: %v", err)
		}
	})

	t.Run("tampered checksums are rejected", func(t *testing.T) {
		bad := filepath.Join(dir, "SHA256SUMS.bad")
		if err := os.WriteFile(bad, []byte("tampered  x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := verifyReleaseSignature(root, "SHA256SUMS.bad", "SHA256SUMS.sig", pubPEM); err == nil {
			t.Fatal("expected failure for tampered checksums")
		}
	})

	t.Run("wrong key is rejected", func(t *testing.T) {
		other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyReleaseSignature(root, "SHA256SUMS", "SHA256SUMS.sig", testPubPEM(t, other)); err == nil {
			t.Fatal("expected failure when verifying with a different key")
		}
	})
}

// TestPinnedReleaseKeyParses guards that the embedded release-signing public key
// is a valid ECDSA key (a typo in the constant would otherwise only surface at
// upgrade time).
func TestPinnedReleaseKeyParses(t *testing.T) {
	if _, err := parseECDSAPublicKey(releaseSigningPubKeyPEM); err != nil {
		t.Fatalf("pinned release signing public key must parse: %v", err)
	}
}
