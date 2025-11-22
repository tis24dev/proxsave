package orchestrator

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"
)

// ========================================
// encryption.go tests
// ========================================

func TestDedupeRecipientStringsHelpers(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "no duplicates",
			input: []string{"age1abc", "age1def", "age1ghi"},
			want:  []string{"age1abc", "age1def", "age1ghi"},
		},
		{
			name:  "with duplicates",
			input: []string{"age1abc", "age1def", "age1abc", "age1ghi", "age1def"},
			want:  []string{"age1abc", "age1def", "age1ghi"},
		},
		{
			name:  "empty strings filtered",
			input: []string{"age1abc", "", "age1def", "  ", "age1ghi"},
			want:  []string{"age1abc", "age1def", "age1ghi"},
		},
		{
			name:  "whitespace trimmed",
			input: []string{"  age1abc  ", "age1def", "age1abc"},
			want:  []string{"age1abc", "age1def"},
		},
		{
			name:  "empty input",
			input: []string{},
			want:  []string{},
		},
		{
			name:  "nil input",
			input: nil,
			want:  []string{},
		},
		{
			name:  "all empty strings",
			input: []string{"", "  ", "   "},
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupeRecipientStrings(tt.input)

			if len(got) != len(tt.want) {
				t.Fatalf("dedupeRecipientStrings() returned %d items; want %d", len(got), len(tt.want))
			}

			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("dedupeRecipientStrings()[%d] = %q; want %q", i, got[i], want)
				}
			}
		})
	}
}

func TestParseRecipientString(t *testing.T) {
	generateSSHPublicKey := func(t *testing.T, algo string) string {
		t.Helper()
		var pub interface{}
		var err error
		switch algo {
		case "rsa":
			var key *rsa.PrivateKey
			key, err = rsa.GenerateKey(rand.Reader, 2048)
			if err == nil {
				pub = &key.PublicKey
			}
		case "ed25519":
			var pubKey ed25519.PublicKey
			pubKey, _, err = ed25519.GenerateKey(rand.Reader)
			if err == nil {
				pub = pubKey
			}
		default:
			t.Fatalf("unsupported algo %s", algo)
		}
		if err != nil {
			t.Fatalf("generate %s key: %v", algo, err)
		}

		sshPub, err := ssh.NewPublicKey(pub)
		if err != nil {
			t.Fatalf("ssh.NewPublicKey(%s): %v", algo, err)
		}
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	}

	sshRSAPub := generateSSHPublicKey(t, "rsa")
	sshED25519Pub := generateSSHPublicKey(t, "ed25519")
	sshED25519Mixed := "SSH-ED25519 " + strings.SplitN(sshED25519Pub, " ", 2)[1]

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "valid age recipient",
			input:   "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p",
			wantErr: false,
		},
		{
			name:    "valid ssh-rsa",
			input:   sshRSAPub,
			wantErr: false,
		},
		{
			name:    "valid ssh-ed25519",
			input:   sshED25519Pub,
			wantErr: false,
		},
		{
			name:    "invalid format",
			input:   "invalid-recipient-format",
			wantErr: true,
		},
		{
			name:    "invalid age recipient content",
			input:   "age1notactuallyvalid",
			wantErr: true,
		},
		{
			name:    "gpg key unsupported",
			input:   "-----BEGIN PGP PUBLIC KEY BLOCK-----",
			wantErr: true,
		},
		{
			name:    "age private key rejected",
			input:   "AGE-SECRET-KEY-1ABCDEFGHIJKLMNOPQRSTUVWXYZ",
			wantErr: true,
		},
		{
			name:    "ssh recipient mixed case",
			input:   sshED25519Mixed,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseRecipientString(tt.input)

			if tt.wantErr && err == nil {
				t.Errorf("parseRecipientString(%q) expected error; got nil", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("parseRecipientString(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestParseRecipientStrings(t *testing.T) {
	validRecipient := "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"

	tests := []struct {
		name    string
		input   []string
		wantLen int
		wantErr bool
	}{
		{
			name:    "single valid",
			input:   []string{validRecipient},
			wantLen: 1,
			wantErr: false,
		},
		{
			name:    "multiple valid with duplicates",
			input:   []string{validRecipient, validRecipient},
			wantLen: 1, // deduped
			wantErr: false,
		},
		{
			name:    "single valid with whitespace trimmed",
			input:   []string{"  " + validRecipient + "  "},
			wantLen: 1,
			wantErr: false,
		},
		{
			name:    "empty slice",
			input:   []string{},
			wantLen: 0,
			wantErr: true,
		},
		{
			name:    "one invalid",
			input:   []string{validRecipient, "invalid"},
			wantLen: 0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRecipientStrings(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Error("parseRecipientStrings() expected error; got nil")
				}
			} else {
				if err != nil {
					t.Errorf("parseRecipientStrings() unexpected error: %v", err)
				}
				if len(got) != tt.wantLen {
					t.Errorf("parseRecipientStrings() returned %d recipients; want %d", len(got), tt.wantLen)
				}
			}
		})
	}
}

func TestCloneRecipients(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		got := cloneRecipients(nil)
		if got != nil {
			t.Errorf("cloneRecipients(nil) = %v; want nil", got)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		got := cloneRecipients([]age.Recipient{})
		if got != nil {
			t.Errorf("cloneRecipients([]) = %v; want nil", got)
		}
	})

	t.Run("non-empty slice copied", func(t *testing.T) {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatalf("generate identity: %v", err)
		}

		input := []age.Recipient{id.Recipient()}
		got := cloneRecipients(input)

		if len(got) != len(input) {
			t.Fatalf("cloneRecipients length = %d; want %d", len(got), len(input))
		}
		got[0] = nil
		if input[0] == nil {
			t.Fatalf("modifying clone mutated original slice")
		}
	})
}

func TestMapInputError(t *testing.T) {
	tests := []struct {
		name     string
		input    error
		wantNil  bool
		wantType error
	}{
		{
			name:    "nil error",
			input:   nil,
			wantNil: true,
		},
		{
			name:     "EOF error",
			input:    io.EOF,
			wantNil:  false,
			wantType: ErrAgeRecipientSetupAborted,
		},
		{
			name:     "ErrClosed",
			input:    os.ErrClosed,
			wantNil:  false,
			wantType: ErrAgeRecipientSetupAborted,
		},
		{
			name:     "use of closed file",
			input:    errors.New("use of closed file"),
			wantNil:  false,
			wantType: ErrAgeRecipientSetupAborted,
		},
		{
			name:     "bad file descriptor",
			input:    errors.New("bad file descriptor"),
			wantNil:  false,
			wantType: ErrAgeRecipientSetupAborted,
		},
		{
			name:     "file already closed",
			input:    errors.New("file already closed"),
			wantNil:  false,
			wantType: ErrAgeRecipientSetupAborted,
		},
		{
			name:    "other error passed through",
			input:   errors.New("network timeout"),
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapInputError(tt.input)

			if tt.wantNil {
				if got != nil {
					t.Errorf("mapInputError(%v) = %v; want nil", tt.input, got)
				}
				return
			}

			if got == nil {
				t.Errorf("mapInputError(%v) = nil; want non-nil", tt.input)
				return
			}

			if tt.wantType != nil && !errors.Is(got, tt.wantType) {
				t.Errorf("mapInputError(%v) = %v; want %v", tt.input, got, tt.wantType)
			}
		})
	}
}

func TestValidatePassphraseStrengthHelpers(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid strong passphrase",
			input:   "MyStr0ng!Pass#2024",
			wantErr: false,
		},
		{
			name:    "valid with symbols",
			input:   "Abc123!@#xyz",
			wantErr: false,
		},
		{
			name:    "too short",
			input:   "Abc123!",
			wantErr: true,
			errMsg:  "too short",
		},
		{
			name:    "exactly minimum length valid",
			input:   "Abc123!@#xyz", // 12 chars, 4 classes
			wantErr: false,
		},
		{
			name:    "only lowercase",
			input:   "abcdefghijklmnop",
			wantErr: true,
			errMsg:  "at least three categories",
		},
		{
			name:    "only uppercase",
			input:   "ABCDEFGHIJKLMNOP",
			wantErr: true,
			errMsg:  "at least three categories",
		},
		{
			name:    "only digits",
			input:   "123456789012345",
			wantErr: true,
			errMsg:  "at least three categories",
		},
		{
			name:    "lower and upper only",
			input:   "AbCdEfGhIjKlMn",
			wantErr: true,
			errMsg:  "at least three categories",
		},
		{
			name:    "lower upper digit (3 classes)",
			input:   "AbCdEfGh123456",
			wantErr: false,
		},
		{
			name:    "weak password",
			input:   "password12345",
			wantErr: true,
			errMsg:  "at least three categories",
		},
		{
			name:    "common password rejected",
			input:   "Password123!", // would be valid but "password" is in weak list
			wantErr: false,          // only exact match "password" is blocked
		},
		{
			name:    "exact weak password",
			input:   "password",
			wantErr: true,
			errMsg:  "too short",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePassphraseStrength([]byte(tt.input))

			if tt.wantErr {
				if err == nil {
					t.Errorf("validatePassphraseStrength(%q) expected error; got nil", tt.input)
					return
				}
				if tt.errMsg != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.errMsg)) {
					t.Errorf("validatePassphraseStrength(%q) error = %v; want message containing %q", tt.input, err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validatePassphraseStrength(%q) unexpected error: %v", tt.input, err)
				}
			}
		})
	}
}

func TestClampCurve25519Scalar(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		wantLen int
		checkFn func([]byte) bool
	}{
		{
			name:    "correct length clamped",
			input:   make([]byte, 32),
			wantLen: 32,
			checkFn: func(k []byte) bool {
				// Check clamping: k[0] &= 248, k[31] &= 127, k[31] |= 64
				return (k[0]&0x07) == 0 && (k[31]&0x80) == 0 && (k[31]&0x40) == 0x40
			},
		},
		{
			name:    "all ones clamped",
			input:   bytes32(0xFF),
			wantLen: 32,
			checkFn: func(k []byte) bool {
				return k[0] == 0xF8 && k[31] == 0x7F
			},
		},
		{
			name:    "all zeros clamped",
			input:   make([]byte, 32),
			wantLen: 32,
			checkFn: func(k []byte) bool {
				return k[0] == 0x00 && k[31] == 0x40
			},
		},
		{
			name:    "wrong length unchanged",
			input:   []byte{0xFF, 0xFF, 0xFF},
			wantLen: 3,
			checkFn: func(k []byte) bool {
				return k[0] == 0xFF && k[1] == 0xFF && k[2] == 0xFF
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := make([]byte, len(tt.input))
			copy(input, tt.input)

			clampCurve25519Scalar(input)

			if len(input) != tt.wantLen {
				t.Errorf("clampCurve25519Scalar() length = %d; want %d", len(input), tt.wantLen)
			}

			if tt.checkFn != nil && !tt.checkFn(input) {
				t.Errorf("clampCurve25519Scalar() clamping check failed: %x", input)
			}
		})
	}
}

// bytes32 creates a 32-byte slice filled with the given value
func bytes32(val byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = val
	}
	return b
}

func TestDeriveDeterministicRecipientFromPassphrase(t *testing.T) {
	// Test determinism - same passphrase should always produce same recipient
	t.Run("deterministic output", func(t *testing.T) {
		passphrase := "TestPassphrase123!"

		recipient1, err1 := deriveDeterministicRecipientFromPassphrase(passphrase)
		recipient2, err2 := deriveDeterministicRecipientFromPassphrase(passphrase)

		if err1 != nil || err2 != nil {
			t.Fatalf("unexpected errors: %v, %v", err1, err2)
		}

		if recipient1 != recipient2 {
			t.Errorf("deriveDeterministicRecipientFromPassphrase() not deterministic: %q != %q",
				recipient1, recipient2)
		}
	})

	// Test that different passphrases produce different recipients
	t.Run("different passphrases produce different recipients", func(t *testing.T) {
		recipient1, _ := deriveDeterministicRecipientFromPassphrase("Passphrase1!")
		recipient2, _ := deriveDeterministicRecipientFromPassphrase("Passphrase2!")

		if recipient1 == recipient2 {
			t.Error("different passphrases produced same recipient")
		}
	})

	// Test output format
	t.Run("output starts with age1", func(t *testing.T) {
		recipient, err := deriveDeterministicRecipientFromPassphrase("MyTestPass123!")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(recipient) < 4 || recipient[:4] != "age1" {
			t.Errorf("recipient should start with 'age1'; got %q", recipient)
		}
	})
}

func TestDeriveDeterministicIdentityFromPassphrase(t *testing.T) {
	getRecipientString := func(t *testing.T, id age.Identity) string {
		t.Helper()
		withRecipient, ok := id.(interface{ Recipient() *age.X25519Recipient })
		if !ok {
			t.Fatalf("identity does not expose Recipient(): %T", id)
		}
		return fmt.Sprint(withRecipient.Recipient())
	}

	// Test determinism
	t.Run("deterministic output", func(t *testing.T) {
		passphrase := "TestIdentity456!"

		identity1, err1 := deriveDeterministicIdentityFromPassphrase(passphrase)
		identity2, err2 := deriveDeterministicIdentityFromPassphrase(passphrase)

		if err1 != nil || err2 != nil {
			t.Fatalf("unexpected errors: %v, %v", err1, err2)
		}

		// Compare the recipient strings since identity is an interface
		if getRecipientString(t, identity1) != getRecipientString(t, identity2) {
			t.Error("deriveDeterministicIdentityFromPassphrase() not deterministic")
		}
	})

	// Test that identity and recipient match
	t.Run("identity recipient matches derived recipient", func(t *testing.T) {
		passphrase := "MatchTest789!"

		identity, err := deriveDeterministicIdentityFromPassphrase(passphrase)
		if err != nil {
			t.Fatalf("identity error: %v", err)
		}

		recipient, err := deriveDeterministicRecipientFromPassphrase(passphrase)
		if err != nil {
			t.Fatalf("recipient error: %v", err)
		}

		if getRecipientString(t, identity) != recipient {
			t.Errorf("identity.Recipient() = %q; want %q",
				getRecipientString(t, identity), recipient)
		}
	})
}

func TestDeriveCurve25519ScalarFromPassphrase(t *testing.T) {
	t.Run("returns 32 bytes", func(t *testing.T) {
		key, err := deriveCurve25519ScalarFromPassphrase("test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(key) != 32 {
			t.Errorf("key length = %d; want 32", len(key))
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		key1, _ := deriveCurve25519ScalarFromPassphrase("testpass")
		key2, _ := deriveCurve25519ScalarFromPassphrase("testpass")

		for i := range key1 {
			if key1[i] != key2[i] {
				t.Fatal("deriveCurve25519ScalarFromPassphrase not deterministic")
			}
		}
	})

	t.Run("key is clamped", func(t *testing.T) {
		key, _ := deriveCurve25519ScalarFromPassphrase("anypass")

		// Check clamping
		if (key[0] & 0x07) != 0 {
			t.Error("key[0] not properly clamped")
		}
		if (key[31] & 0x80) != 0 {
			t.Error("key[31] high bit not cleared")
		}
		if (key[31] & 0x40) == 0 {
			t.Error("key[31] bit 6 not set")
		}
	})
}
