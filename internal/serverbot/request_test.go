package serverbot

import (
	"strings"
	"testing"
)

func TestResponseJSON(t *testing.T) {
	r := &Response{Body: []byte(`{"a":"b","n":3}`)}
	var v struct {
		A string
		N int
	}
	if err := r.JSON(&v); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if v.A != "b" || v.N != 3 {
		t.Errorf("decoded %+v", v)
	}
}

func TestResponseSnippet(t *testing.T) {
	// ESC (0x1b) and NUL (0x00) are control chars and must be stripped; the printable
	// remainder ("[2J") stays as inert text.
	got := (&Response{Body: []byte("ab\x1b[2Jcd\x00ef")}).Snippet(100)
	if strings.ContainsAny(got, "\x1b\x00") {
		t.Errorf("control chars not stripped: %q", got)
	}
	if got != "ab[2Jcdef" {
		t.Errorf("snippet = %q, want ab[2Jcdef", got)
	}
	// Unicode format/bidi/zero-width tricks (Trojan Source) must also be stripped,
	// not just C0/C1 control chars.
	bidi := "denied " + string(rune(0x202e)) + "safe" + string(rune(0x200b)) + string(rune(0x2066))
	if s := (&Response{Body: []byte(bidi)}).Snippet(100); s != "denied safe" {
		t.Errorf("format/bidi runes not stripped: %q", s)
	}
	// Truncate to n runes.
	if s := (&Response{Body: []byte("abcdef")}).Snippet(3); s != "abc" {
		t.Errorf("truncate = %q, want abc", s)
	}
	if s := (&Response{Body: []byte("x")}).Snippet(0); s != "" {
		t.Errorf("Snippet(0) = %q, want empty", s)
	}
	// Honest doc: Snippet does NOT redact a secret (the caller wraps RedactSecrets).
	if s := (&Response{Body: []byte("token=SEKRET")}).Snippet(100); !strings.Contains(s, "SEKRET") {
		t.Error("Snippet must not redact; the caller is responsible")
	}
}
