package backup

import (
	"errors"
	"testing"
)

// Regression for age-close-demoted-on-prior-error (2026-06-09 audit): each
// create*Archive registered a finalizeEncryption defer that only promoted the age
// Close error when the named err was still nil; otherwise it logged a warning and
// discarded it. Because the compressor's closeIntoErr defer runs FIRST (LIFO) and
// can set err, a real age Close failure (final-chunk flush -> truncated/undecryptable
// archive) was downgraded to a warning. The shared finalizeEncryptionInto now folds
// the age error in via errors.Join so it is never lost.

func TestFinalizeEncryptionInto_DoesNotDemoteAgeErrorBehindPriorError(t *testing.T) {
	prior := errors.New("close gzip writer: boom") // e.g. compressor Close ran first
	ageErr := errors.New("age final chunk flush failed")

	err := prior
	finalizeEncryptionInto(&err, func() error { return ageErr })

	if !errors.Is(err, ageErr) {
		t.Errorf("age finalize error was dropped behind a prior error; err=%v", err)
	}
	if !errors.Is(err, prior) {
		t.Errorf("prior error was dropped; err=%v", err)
	}
}

func TestFinalizeEncryptionInto_SurfacesAgeErrorWhenNoPrior(t *testing.T) {
	ageErr := errors.New("age final chunk flush failed")
	var err error
	finalizeEncryptionInto(&err, func() error { return ageErr })
	if !errors.Is(err, ageErr) {
		t.Errorf("age finalize error not surfaced; err=%v", err)
	}
}

func TestFinalizeEncryptionInto_LeavesPriorErrorUntouchedOnSuccess(t *testing.T) {
	prior := errors.New("prior")
	err := prior
	finalizeEncryptionInto(&err, func() error { return nil })
	if !errors.Is(err, prior) {
		t.Errorf("prior error must be preserved when finalize succeeds; err=%v", err)
	}
}
