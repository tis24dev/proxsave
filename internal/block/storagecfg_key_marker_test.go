package block

import "testing"

// pve-storage writes encryption-key as the key's fingerprint, or as a bare 1 when the
// key JSON has none (keys from before proxmox-backup-client 1.0.3) and for every
// storage added before pve-storage 3cc2eb73 (2020-11-24) and never re-keyed. The 1 is
// a yes/no marker PVE itself never compares: compared with the fingerprint, it refused
// the storage's own key ("does not match storage").
func TestEvaluateKeyShowTakesPVEsBareMarker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output []byte
	}{
		{"key with a fingerprint", keyShowJSON("none", testEncKeyFP)},
		{"key without a fingerprint", []byte(`{"fingerprint":null,"kdf":"none","path":"\"/etc/pve/priv/storage/old.enc\""}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, marker := range []string{"1", " 1 "} {
				fact, err := EvaluateKeyShow(tc.output, marker)
				if err != nil {
					t.Fatalf("EvaluateKeyShow: %v", err)
				}
				if fact != nil {
					t.Fatalf("encryption-key %q: %q, want no fact", marker, fact.Line())
				}
			}
		})
	}
	// A passphrase is still a fact with the marker, and any other value is compared.
	if fact, _ := EvaluateKeyShow(keyShowJSON("scrypt", testEncKeyFP), "1"); fact == nil || fact.Line() != "Encryption key: needs a passphrase" {
		t.Fatalf("passphrase with the marker: %v", fact)
	}
	if fact, _ := EvaluateKeyShow(keyShowJSON("none", testEncKeyFP), "11"); fact == nil || fact.Line() != "Encryption key: does not match storage" {
		t.Fatalf("encryption-key 11: %v, want a mismatch", fact)
	}
}
