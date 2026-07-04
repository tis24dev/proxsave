package logging

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestMaskSecret(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short", secretMaskPrefix},    // <= 8 -> full mask
		{"12345678", secretMaskPrefix}, // exactly 8 -> full mask
		{"0123456789ABCDEF", secretMaskPrefix + "CDEF"}, // last 4 visible
	}
	for _, c := range cases {
		if got := MaskSecret(c.in); got != c.want {
			t.Errorf("MaskSecret(%q)=%q want %q", c.in, got, c.want)
		}
	}
	sec := "supersecretvalue123"
	if strings.Contains(MaskSecret(sec), sec) {
		t.Fatalf("MaskSecret leaked the raw secret")
	}
}

func TestRedactSecrets(t *testing.T) {
	sec := "1234567890ABCDEF"
	got := RedactSecrets("token="+sec+" tail", sec)
	if strings.Contains(got, sec) {
		t.Fatalf("raw secret not redacted: %q", got)
	}
	if !strings.Contains(got, MaskSecret(sec)) {
		t.Fatalf("expected mask in %q", got)
	}

	// URL-encoded form (how a token appears inside a *url.Error, e.g. Gotify).
	raw := "tok+en/val=longenough"
	enc := url.QueryEscape(raw)
	gotEnc := RedactSecrets(`Post "https://h/m?token=`+enc+`": refused`, raw)
	if strings.Contains(gotEnc, enc) {
		t.Fatalf("URL-encoded secret not redacted: %q", gotEnc)
	}
	if strings.Contains(gotEnc, raw) {
		t.Fatalf("raw secret not redacted: %q", gotEnc)
	}

	// Empty secret is a no-op (must not corrupt the string).
	if out := RedactSecrets("hello world", ""); out != "hello world" {
		t.Fatalf("empty secret should be a no-op, got %q", out)
	}
	// Too-short secret is skipped (avoid masking innocent substrings).
	if out := RedactSecrets("abcabc", "abc"); out != "abcabc" {
		t.Fatalf("short secret should be skipped, got %q", out)
	}
}

func TestLoggerScrubsRegisteredSecret(t *testing.T) {
	var buf bytes.Buffer
	l := New(types.LogLevelInfo, false)
	l.SetOutput(&buf)

	sec := "bottoken0123456789secret"
	l.RegisterSecret(sec)
	l.Warning("api request failed: https://api/bot%s/x", sec)

	out := buf.String()
	if strings.Contains(out, sec) {
		t.Fatalf("log leaked the registered secret: %q", out)
	}
	if !strings.Contains(out, MaskSecret(sec)) {
		t.Fatalf("expected masked secret in log: %q", out)
	}
}

func TestLoggerRegisterSecretIgnoresEmptyOrShort(t *testing.T) {
	var buf bytes.Buffer
	l := New(types.LogLevelInfo, false)
	l.SetOutput(&buf)

	l.RegisterSecret("")   // ignored
	l.RegisterSecret("ab") // too short, ignored
	l.Info("a normal line without secrets")

	if !strings.Contains(buf.String(), "a normal line without secrets") {
		t.Fatalf("normal line corrupted: %q", buf.String())
	}
}
