package checks

import "testing"

func TestParseProcStat(t *testing.T) {
	// comm contains spaces and parentheses; fields after the LAST ')' start at
	// field 3 (state). starttime is field 22 -> index 19 after the ')'.
	stat := []byte("1234 (my (weird) proc) S 1 1 1 0 -1 4194560 100 0 0 0 5 3 0 0 20 0 1 0 987654 0 0")
	got, ok := parseProcStat(stat)
	if !ok {
		t.Fatal("parseProcStat should succeed on a well-formed stat line")
	}
	if got != 987654 {
		t.Fatalf("starttime = %d, want 987654", got)
	}
}

func TestParseProcStatMalformed(t *testing.T) {
	if _, ok := parseProcStat([]byte("garbage without a paren")); ok {
		t.Fatal("no ')' -> not parseable")
	}
	if _, ok := parseProcStat([]byte("1 (x) S 1 2 3")); ok {
		t.Fatal("too few fields -> not parseable")
	}
}
