package orchestrator

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestPromptDecryptIdentity_CancelReturnsAborted(t *testing.T) {
	// Focus starts on the password field; tab to Cancel and submit.
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyTab, tcell.KeyEnter})

	_, err := promptDecryptIdentity("backup", "/tmp/config.env", "sig", "")
	if err != ErrDecryptAborted {
		t.Fatalf("err=%v; want %v", err, ErrDecryptAborted)
	}
}

func TestPromptDecryptIdentity_PassphraseReturnsIdentity(t *testing.T) {
	passphrase := "test passphrase"

	var seq []simKey
	for _, r := range passphrase {
		seq = append(seq, simKey{Key: tcell.KeyRune, R: r})
	}
	seq = append(seq, simKey{Key: tcell.KeyTab}, simKey{Key: tcell.KeyEnter})
	withSimAppSequence(t, seq)

	ids, err := promptDecryptIdentity("backup", "/tmp/config.env", "sig", "")
	if err != nil {
		t.Fatalf("promptDecryptIdentity error: %v", err)
	}
	if len(ids) == 0 {
		t.Fatalf("expected at least one identity")
	}
}

