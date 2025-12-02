package wizard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePublicKey(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "valid", input: " age1abc  ", want: "age1abc"},
		{name: "empty", input: "", wantErr: true},
		{name: "missing prefix", input: "abc", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := validatePublicKey(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidatePassphrase(t *testing.T) {
	cases := []struct {
		name    string
		pass    string
		confirm string
		wantErr bool
	}{
		{name: "valid", pass: "correct horse", confirm: "correct horse"},
		{name: "empty", pass: "", confirm: "", wantErr: true},
		{name: "short", pass: "short", confirm: "short", wantErr: true},
		{name: "mismatch", pass: "longenough", confirm: "diff", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePassphrase(tc.pass, tc.confirm)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidatePrivateKey(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "valid", input: " AGE-SECRET-KEY-1abc  ", want: "AGE-SECRET-KEY-1abc"},
		{name: "empty", input: "", wantErr: true},
		{name: "wrong prefix", input: "SECRET", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := validatePrivateKey(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSaveAgeRecipientSuccess(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "keys", "recipient.age")

	origMkdir := ageMkdirAll
	origWrite := ageWriteFile
	origChmod := ageChmod
	t.Cleanup(func() {
		ageMkdirAll = origMkdir
		ageWriteFile = origWrite
		ageChmod = origChmod
	})

	ageMkdirAll = func(path string, perm os.FileMode) error {
		if perm != 0o700 {
			t.Fatalf("unexpected mkdir perm: %v", perm)
		}
		return nil
	}

	var written []byte
	ageWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if path != target {
			t.Fatalf("unexpected path %s", path)
		}
		written = append([]byte(nil), data...)
		if perm != 0o600 {
			t.Fatalf("unexpected write perm: %v", perm)
		}
		return nil
	}

	var chmodPath string
	ageChmod = func(path string, perm os.FileMode) error {
		chmodPath = path
		if perm != 0o600 {
			t.Fatalf("unexpected chmod perm: %v", perm)
		}
		return nil
	}

	if err := SaveAgeRecipient(target, "age1final"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(written) != "age1final\n" {
		t.Fatalf("wrong payload: %q", string(written))
	}
	if chmodPath != target {
		t.Fatalf("chmod not called on target")
	}
}

func TestSaveAgeRecipientErrors(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "keys", "recipient.age")

	origMkdir := ageMkdirAll
	origWrite := ageWriteFile
	origChmod := ageChmod
	t.Cleanup(func() {
		ageMkdirAll = origMkdir
		ageWriteFile = origWrite
		ageChmod = origChmod
	})

	ageMkdirAll = func(string, os.FileMode) error {
		return errors.New("boom")
	}
	if err := SaveAgeRecipient(target, "age1final"); err == nil || !strings.Contains(err.Error(), "create recipient directory") {
		t.Fatalf("unexpected error: %v", err)
	}

	ageMkdirAll = origMkdir

	ageWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("write fail")
	}
	if err := SaveAgeRecipient(target, "age1final"); err == nil || !strings.Contains(err.Error(), "write recipient file") {
		t.Fatalf("unexpected error: %v", err)
	}

	ageWriteFile = origWrite

	ageChmod = func(string, os.FileMode) error {
		return errors.New("chmod fail")
	}
	if err := SaveAgeRecipient(target, "age1final"); err == nil || !strings.Contains(err.Error(), "chmod recipient file") {
		t.Fatalf("unexpected error: %v", err)
	}
}
