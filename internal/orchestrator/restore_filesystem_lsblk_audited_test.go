package orchestrator

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Regression for the 2026-06-09 audit finding lsblk-text-column-index-vs-fields:
// parseLsblkTextInventory used strings.Fields + absolute token index, so an empty
// earlier cell (or a space inside FSVER) shifted UUID/LABEL onto the wrong values,
// corrupting the /dev->identity map used to rewrite fstab. Written after the fix
// to fixed-width column-offset parsing, hence the _audited suffix.

// formatLsblkTable renders a fixed-width, column-aligned table the way `lsblk -f`
// does: each column padded (in display/rune cells) to the widest of its header
// label and values, columns separated by a single space, trailing space trimmed.
func formatLsblkTable(header []string, rows [][]string) string {
	width := make([]int, len(header))
	for i, h := range header {
		width[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i := 0; i < len(header) && i < len(r); i++ {
			if w := utf8.RuneCountInString(r[i]); w > width[i] {
				width[i] = w
			}
		}
	}
	pad := func(s string, w int) string {
		if n := w - utf8.RuneCountInString(s); n > 0 {
			return s + strings.Repeat(" ", n)
		}
		return s
	}
	render := func(cells []string) string {
		parts := make([]string, len(header))
		for i := range header {
			v := ""
			if i < len(cells) {
				v = cells[i]
			}
			parts[i] = pad(v, width[i])
		}
		return strings.TrimRight(strings.Join(parts, " "), " ")
	}
	var b strings.Builder
	b.WriteString(render(header))
	b.WriteByte('\n')
	for _, r := range rows {
		b.WriteString(render(r))
		b.WriteByte('\n')
	}
	return b.String()
}

func TestParseLsblkTextInventory_EmptyCellsAndSpacesDoNotShiftColumns(t *testing.T) {
	header := []string{"NAME", "FSTYPE", "FSVER", "LABEL", "UUID", "FSAVAIL", "FSUSE%", "MOUNTPOINTS"}
	rows := [][]string{
		// Whole disk: no filesystem at all -> dropped (no uuid/label).
		{"sda", "", "", "", "", "", "", ""},
		// EFI vfat: empty LABEL, UUID present. The classic mis-parse: token index
		// would put the UUID into LABEL and FSAVAIL into UUID.
		{"├─sda2", "vfat", "FAT32", "", "E843-7857", "500M", "5%", "/boot/efi"},
		// LVM member: empty LABEL AND an FSVER value that contains a space.
		{"└─sda3", "LVM2_member", "LVM2 001", "", "rfsqkR-aaaa-bbbb-cccc", "", "", ""},
		// Fully populated row: the only shape the old token parser got right.
		{"pve-root", "ext4", "1.0", "root-fs", "11111111-2222-3333-4444-555555555555", "2.5G", "87%", "/"},
	}

	got := parseLsblkTextInventory(formatLsblkTable(header, rows))

	want := map[string]struct{ uuid, label string }{
		"/dev/sda2":     {uuid: "E843-7857", label: ""},
		"/dev/sda3":     {uuid: "rfsqkR-aaaa-bbbb-cccc", label: ""},
		"/dev/pve-root": {uuid: "11111111-2222-3333-4444-555555555555", label: "root-fs"},
	}

	if len(got) != len(want) {
		t.Fatalf("entry count = %d, want %d; got=%+v", len(got), len(want), got)
	}
	for dev, w := range want {
		id, ok := got[dev]
		if !ok {
			t.Errorf("%s missing from inventory", dev)
			continue
		}
		if id.UUID != w.uuid {
			t.Errorf("%s UUID = %q, want %q", dev, id.UUID, w.uuid)
		}
		if id.Label != w.label {
			t.Errorf("%s Label = %q, want %q", dev, id.Label, w.label)
		}
	}

	// The whole-disk row must not appear (it has neither UUID nor LABEL).
	if _, ok := got["/dev/sda"]; ok {
		t.Errorf("/dev/sda should be skipped (no uuid/label)")
	}
}

// TestParseLsblkTextInventory_AltHeaderOrderWithoutFSVER guards an older lsblk
// layout (no FSVER column) - offsets must be derived from whatever header is
// present, not hard-coded indices.
func TestParseLsblkTextInventory_AltHeaderOrderWithoutFSVER(t *testing.T) {
	header := []string{"NAME", "FSTYPE", "LABEL", "UUID", "MOUNTPOINT"}
	rows := [][]string{
		{"sdb1", "ext4", "", "abcd-1234-ef56", "/data"},
		{"sdb2", "swap", "swaplbl", "9999-8888", "[SWAP]"},
	}

	got := parseLsblkTextInventory(formatLsblkTable(header, rows))

	if id := got["/dev/sdb1"]; id.UUID != "abcd-1234-ef56" || id.Label != "" {
		t.Errorf("/dev/sdb1 = %+v, want UUID=abcd-1234-ef56 Label=\"\"", id)
	}
	if id := got["/dev/sdb2"]; id.UUID != "9999-8888" || id.Label != "swaplbl" {
		t.Errorf("/dev/sdb2 = %+v, want UUID=9999-8888 Label=swaplbl", id)
	}
}
