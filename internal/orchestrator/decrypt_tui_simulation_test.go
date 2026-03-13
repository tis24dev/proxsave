package orchestrator

import (
	"context"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestTUIWorkflowUIPromptDecryptSecret_CancelReturnsAborted(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyTab, tcell.KeyEnter})

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	_, err := ui.PromptDecryptSecret(context.Background(), "backup", "")
	if err != ErrDecryptAborted {
		t.Fatalf("err=%v; want %v", err, ErrDecryptAborted)
	}
}

func TestTUIWorkflowUIPromptDecryptSecret_PassphraseReturnsSecret(t *testing.T) {
	passphrase := "test passphrase"

	var seq []simKey
	for _, r := range passphrase {
		seq = append(seq, simKey{Key: tcell.KeyRune, R: r})
	}
	seq = append(seq, simKey{Key: tcell.KeyTab}, simKey{Key: tcell.KeyEnter})
	withSimAppSequence(t, seq)

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	secret, err := ui.PromptDecryptSecret(context.Background(), "backup", "")
	if err != nil {
		t.Fatalf("PromptDecryptSecret error: %v", err)
	}
	if secret != passphrase {
		t.Fatalf("secret=%q; want %q", secret, passphrase)
	}
}

func TestTUIWorkflowUIPromptDecryptSecret_ZeroInputAborts(t *testing.T) {
	withSimAppSequence(t, []simKey{
		{Key: tcell.KeyRune, R: '0'},
		{Key: tcell.KeyTab},
		{Key: tcell.KeyEnter},
	})

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	_, err := ui.PromptDecryptSecret(context.Background(), "backup", "")
	if err != ErrDecryptAborted {
		t.Fatalf("err=%v; want %v", err, ErrDecryptAborted)
	}
}
