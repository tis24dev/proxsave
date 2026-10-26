package cli

import (
	"reflect"
	"testing"
)

func TestParseKeepsWordsLeftAfterFlags(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want []string
	}{
		{name: "word and the flag after it", argv: []string{"--backup", "extra", "--dry-run"}, want: []string{"extra", "--dry-run"}},
		{name: "upgrade yes is not consumed", argv: []string{"--upgrade", "yes"}, want: []string{"yes"}},
		{name: "upgrade y is consumed", argv: []string{"--upgrade", "y"}},
		{name: "flags only", argv: []string{"--backup", "--dry-run"}},
		{name: "bare", argv: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWithArgs(t, tt.argv).UnexpectedArgs
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Parse(%q).UnexpectedArgs = %q, want %q", tt.argv, got, tt.want)
			}
		})
	}
}
