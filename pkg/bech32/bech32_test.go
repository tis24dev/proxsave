package bech32

import (
	"bytes"
	"strings"
	"testing"
)

// Test vectors from BIP173 to make sure known-good strings decode correctly
// and re-encode to the original value.
func TestDecode_ValidVectors(t *testing.T) {
	validCases := []string{
		"A12UEL5L",
		"a12uel5l",
		"abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw",
		"split1checkupstagehandshakeupstreamerranterredcaperred2y9e3w",
	}

	for _, tc := range validCases {
		name := tc
		if len(name) > 10 {
			name = name[:10]
		}

		t.Run(name, func(t *testing.T) {
			hrp, data, err := Decode(tc)
			if err != nil {
				t.Fatalf("Decode(%q) failed: %v", tc, err)
			}

			separator := strings.LastIndex(tc, "1")
			if separator == -1 {
				t.Fatalf("missing separator in %q", tc)
			}
			expectedHRP := tc[:separator]
			if hrp != expectedHRP {
				t.Fatalf("HRP mismatch: got %q, want %q", hrp, expectedHRP)
			}

			encoded, err := Encode(hrp, data)
			if err != nil {
				t.Fatalf("Encode round-trip failed: %v", err)
			}
			if encoded != tc {
				t.Fatalf("Encode/Decode mismatch: got %q, want %q", encoded, tc)
			}
		})
	}
}

// Encode -> Decode -> original data.
func TestEncodeDecode_RoundTrip(t *testing.T) {
	testCases := []struct {
		hrp  string
		data []byte
	}{
		{"test", []byte{0, 1, 2, 3, 4, 5}},
		{"bc", []byte{0x00, 0x14, 0x75, 0x1e}},
		{"age", []byte("hello world")},
		{"PREFIX", []byte{255, 128, 64, 32, 16, 8, 4, 2, 1, 0}},
	}

	for _, tc := range testCases {
		t.Run(tc.hrp, func(t *testing.T) {
			encoded, err := Encode(tc.hrp, tc.data)
			if err != nil {
				t.Fatalf("Encode failed: %v", err)
			}

			hrp, decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode failed: %v", err)
			}

			if !strings.EqualFold(hrp, tc.hrp) {
				t.Errorf("HRP mismatch: got %q, want %q", hrp, tc.hrp)
			}
			if !bytes.Equal(decoded, tc.data) {
				t.Errorf("Data mismatch: got %v, want %v", decoded, tc.data)
			}
		})
	}
}

// Uppercase HRPs should produce uppercase output and vice versa.
func TestEncode_CasePreservation(t *testing.T) {
	data := []byte{1, 2, 3}

	lower, err := Encode("test", data)
	if err != nil {
		t.Fatalf("Encode lowercase failed: %v", err)
	}
	if strings.ToLower(lower) != lower {
		t.Errorf("lowercase HRP should produce lowercase output: %q", lower)
	}

	upper, err := Encode("TEST", data)
	if err != nil {
		t.Fatalf("Encode uppercase failed: %v", err)
	}
	if strings.ToUpper(upper) != upper {
		t.Errorf("uppercase HRP should produce uppercase output: %q", upper)
	}
}

func TestEncode_Errors(t *testing.T) {
	testCases := []struct {
		name string
		hrp  string
		data []byte
	}{
		{"empty HRP", "", []byte{1, 2, 3}},
		{"mixed case HRP", "TeSt", []byte{1, 2, 3}},
		{"invalid char in HRP (space)", "te st", []byte{1, 2, 3}},
		{"invalid char in HRP (control)", "test\x00", []byte{1, 2, 3}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Encode(tc.hrp, tc.data); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestDecode_Errors(t *testing.T) {
	testCases := []struct {
		name  string
		input string
	}{
		{"mixed case", "TeSt1qpzry"},
		{"no separator", "testqpzry"},
		{"separator at start", "1qpzry9x8gf2tvdw0s3jn54khce6mua7l"},
		{"invalid char in data", "test1qpzry9x8gf2tvdw0s3jn54khce6mua7b"},
		{"invalid checksum", "test1qpzryaaaaaa"},
		{"too short after separator", "test1qqqqq"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Decode(tc.input); err == nil {
				t.Errorf("Decode(%q) should fail", tc.input)
			}
		})
	}
}

func TestEncodeDecode_EmptyData(t *testing.T) {
	encoded, err := Encode("test", []byte{})
	if err != nil {
		t.Fatalf("Encode empty data failed: %v", err)
	}

	hrp, data, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if hrp != "test" {
		t.Errorf("HRP mismatch: got %q", hrp)
	}
	if len(data) != 0 {
		t.Errorf("expected empty data, got %v", data)
	}
}
