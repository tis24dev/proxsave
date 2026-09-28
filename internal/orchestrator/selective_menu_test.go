package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestShowRestoreModeMenu_ParsesChoicesAndRetries(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)

	oldIn := os.Stdin
	oldOut := os.Stdout
	t.Cleanup(func() {
		os.Stdin = oldIn
		os.Stdout = oldOut
	})

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = inW.Close()
		_ = out.Close()
	})

	if _, err := inW.WriteString("99\n2\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = inW.Close()

	got, err := ShowRestoreModeMenu(context.Background(), logger, SystemTypePVE)
	if err != nil {
		t.Fatalf("ShowRestoreModeMenu error: %v", err)
	}
	if got != RestoreModeStorage {
		t.Fatalf("got=%q want=%q", got, RestoreModeStorage)
	}
}

func TestShowRestoreModeMenu_CancelReturnsErrRestoreAborted(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)

	oldIn := os.Stdin
	oldOut := os.Stdout
	t.Cleanup(func() {
		os.Stdin = oldIn
		os.Stdout = oldOut
	})

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdin = inR
	os.Stdout = out
	t.Cleanup(func() {
		_ = inR.Close()
		_ = inW.Close()
		_ = out.Close()
	})

	if _, err := inW.WriteString("0\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	_ = inW.Close()

	_, err = ShowRestoreModeMenu(context.Background(), logger, SystemTypePVE)
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v want=%v", err, ErrRestoreAborted)
	}
}

func TestShowRestoreModeMenu_ContextCanceledReturnsErrRestoreAborted(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	oldIn := os.Stdin
	oldOut := os.Stdout
	t.Cleanup(func() { os.Stdout = oldOut })
	t.Cleanup(func() { os.Stdin = oldIn })

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	_ = inW.Close()
	os.Stdin = inR
	t.Cleanup(func() { _ = inR.Close() })

	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", os.DevNull, err)
	}
	os.Stdout = out
	t.Cleanup(func() { _ = out.Close() })

	_, err = ShowRestoreModeMenu(ctx, logger, SystemTypePVE)
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v want=%v", err, ErrRestoreAborted)
	}
}

// The toggle help names the numbers the menu really accepts: it read "1-9" with 18
// or more categories listed.
func TestShowCategorySelectionMenu_HelpNamesTheToggleRange(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	many := make([]Category, 0, 12)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("common_%02d", i)
		many = append(many, Category{ID: id, Name: id, Type: CategoryTypeCommon})
	}

	cases := []struct {
		name string
		cats []Category
		want string
	}{
		{"twelve", many, "  1-12   - Toggle category selection\n"},
		{"one", many[:1], "  1      - Toggle category selection\n"},
		{"none", nil, "  1      - Toggle category selection\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

			_, menuErr := ShowCategorySelectionMenuWithReader(context.Background(), bufio.NewReader(strings.NewReader("0\n")), logger, tc.cats, SystemTypePVE)

			_ = w.Close()
			os.Stdout = oldOut
			<-done

			if !errors.Is(menuErr, ErrRestoreAborted) {
				t.Fatalf("menu error = %v; want ErrRestoreAborted", menuErr)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("help line %q missing from:\n%s", tc.want, out.String())
			}
		})
	}
}
