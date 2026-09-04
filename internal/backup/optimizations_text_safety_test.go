package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The prefilter's CR strip was a blind bytes.ReplaceAll(data, "\r", nil) over every
// small .txt/.log/.md/.conf/.cfg/.ini in the whole staging tree, CUSTOM_BACKUP_PATHS
// payload included, while CONFIGURATION.md sells it as safe, semantic-preserving
// removal of \r "from CRLF text files". Measured live before this fix: a UTF-16LE
// "hi\r\nyo" lost the 0x0D byte of its 0x0D 0x00 pair, the payload went odd-length
// and every following character shifted into garbage; a lone-\r progress log had its
// lines silently merged. The backup copy is what gets corrupted - the restore then
// delivers it. The contract these tests pin: only CRLF pairs in plain single-byte
// text are touched; anything else stays byte-identical.

func normalizeInDir(t *testing.T, name string, content []byte) (changed bool, after []byte) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o640); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	changed, err = normalizeTextFile(root, name)
	if err != nil {
		t.Fatalf("normalizeTextFile: %v", err)
	}
	after, err = os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return changed, after
}

func TestNormalizeLeavesUTF16Alone(t *testing.T) {
	// "hi\r\nyo" in UTF-16LE with BOM - the shape Windows tools export.
	le := []byte{0xFF, 0xFE, 'h', 0, 'i', 0, '\r', 0, '\n', 0, 'y', 0, 'o', 0}
	if changed, after := normalizeInDir(t, "note.txt", le); changed || string(after) != string(le) {
		t.Fatalf("UTF-16LE text was rewritten (changed=%v):\nbefore % x\nafter  % x", changed, le, after)
	}
	be := []byte{0xFE, 0xFF, 0, 'h', 0, 'i', 0, '\r', 0, '\n', 0, 'y', 0, 'o'}
	if changed, after := normalizeInDir(t, "note2.txt", be); changed || string(after) != string(be) {
		t.Fatalf("UTF-16BE text was rewritten (changed=%v)", changed)
	}
}

func TestNormalizeLeavesLoneCRAlone(t *testing.T) {
	log := []byte("done 10%\rdone 99%\rdone 100%\n")
	if changed, after := normalizeInDir(t, "progress.log", log); changed || string(after) != string(log) {
		t.Fatalf("lone \\r lines were merged (changed=%v): %q", changed, after)
	}
}

func TestNormalizeLeavesBinaryBearingLogsAlone(t *testing.T) {
	bin := []byte("prefix\x00\r\nraw\rbytes")
	if changed, after := normalizeInDir(t, "mixed.log", bin); changed || string(after) != string(bin) {
		t.Fatalf("a NUL-bearing log was rewritten (changed=%v): %q", changed, after)
	}
}

func TestNormalizeLeavesNULFreeBinaryAlone(t *testing.T) {
	bin := []byte{0x01, 0x02, 'A', '\r', '\n', 0x7F}
	if changed, after := normalizeInDir(t, "opaque.log", bin); changed || !bytes.Equal(after, bin) {
		t.Fatalf("a NUL-free binary log was rewritten (changed=%v):\nbefore % x\nafter  % x", changed, bin, after)
	}
}

func TestNormalizeStillCollapsesCRLFAndOnlyCRLF(t *testing.T) {
	in := []byte("a\r\nb\rc\r\nd\n")
	changed, after := normalizeInDir(t, "doc.md", in)
	if !changed {
		t.Fatal("a CRLF file must still be normalized")
	}
	if want := "a\nb\rc\nd\n"; string(after) != want {
		t.Fatalf("only the \\r of CRLF pairs may go:\nwant %q\ngot  %q", want, after)
	}
}
