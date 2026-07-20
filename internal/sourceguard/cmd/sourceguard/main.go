// Command sourceguard scans the given files for deceptive Unicode (bidi,
// invisible-format, and confusable-homoglyph runes) and exits non-zero if any
// is found. It is the runnable form of internal/sourceguard, used by the
// pre-commit hook; the package depends only on the standard library plus the
// detector, so it builds even when the rest of the module is a work in progress.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/sourceguard"
)

func run(args []string, stdout, stderr io.Writer) int {
	code := 0
	for _, path := range args {
		content, err := safefs.ReadFileUnderRoot(path)
		if err != nil {
			fmt.Fprintf(stderr, "sourceguard: cannot read %s: %v\n", path, err)
			code = 1
			continue
		}
		checkHomoglyphs := strings.HasSuffix(path, ".go")
		for _, f := range sourceguard.ScanText(string(content), checkHomoglyphs) {
			fmt.Fprintf(stdout, "%s:%d: U+%04X %s\n", path, f.Line, f.Rune, f.Why)
			code = 1
		}
	}
	return code
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
