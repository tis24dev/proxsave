package main

import (
	"errors"
	"fmt"
	"testing"
)

// The encryption step reports its abort as "encryption setup aborted by user"; the
// install must stop on it like on any other abort, even when the text arrives without
// errInteractiveAborted in its chain (wrapped with %v somewhere on the way).
func TestIsInstallAbortedErrorRecognisesTheEncryptionAbortText(t *testing.T) {
	for _, err := range []error{
		errors.New("encryption setup aborted by user"),
		fmt.Errorf("configure encryption: %v", errors.New("encryption setup aborted by user")),
	} {
		if !isInstallAbortedError(err) {
			t.Fatalf("isInstallAbortedError(%q) = false, want true", err)
		}
	}
}
