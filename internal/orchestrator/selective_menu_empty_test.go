package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A category turned on and off again stayed in the count, so "c" accepted an empty
// selection and the restore ran with zero categories. The CLI now refuses it as the
// TUI does (WithMinSelected(1)), with the TUI's text, and shows the menu again.
func TestCategoryMenuRefusesAnEmptySelectionLikeTheTUI(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	cats := []Category{
		{ID: "common_a", Name: "common_a", Type: CategoryTypeCommon},
		{ID: "common_b", Name: "common_b", Type: CategoryTypeCommon},
	}
	for _, in := range []string{"1\n1\nc\n0\n", "a\n1\n2\nc\n0\n", "1\nn\nc\n0\n"} {
		t.Run(strings.ReplaceAll(in, "\n", " "), func(t *testing.T) {
			oldOut := os.Stdout
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe: %v", err)
			}
			os.Stdout = w
			var out bytes.Buffer
			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(&out, r)
				close(done)
			}()

			got, menuErr := ShowCategorySelectionMenuWithReader(context.Background(), bufio.NewReader(strings.NewReader(in)), logger, cats, SystemTypePVE)

			_ = w.Close()
			os.Stdout = oldOut
			<-done

			if !errors.Is(menuErr, ErrRestoreAborted) {
				t.Fatalf("input %q: got %d categories, err %v; want the menu shown again and then cancelled", in, len(got), menuErr)
			}
			if !strings.Contains(out.String(), "Select at least 1 item(s); 0 selected") {
				t.Fatalf("input %q: the TUI's text is missing from:\n%s", in, out.String())
			}
		})
	}
}
