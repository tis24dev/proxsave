package testutil

import (
	"context"
	"errors"
)

var ErrAgeSetupUIStubAborted = errors.New("age setup ui stub aborted")

// AgeSetupUIStub is a reusable scripted UI double for age recipient setup flows.
type AgeSetupUIStub[T any] struct {
	Overwrite bool
	Drafts    []*T
	AddMore   []bool
	AbortErr  error

	OverwriteCalls int
	CollectCalls   int
	AddCalls       int
}

func (u *AgeSetupUIStub[T]) ConfirmOverwriteExistingRecipient(ctx context.Context, recipientPath string) (bool, error) {
	u.OverwriteCalls++
	return u.Overwrite, nil
}

func (u *AgeSetupUIStub[T]) CollectRecipientDraft(ctx context.Context, recipientPath string) (*T, error) {
	u.CollectCalls++
	if len(u.Drafts) == 0 {
		if u.AbortErr != nil {
			return nil, u.AbortErr
		}
		return nil, ErrAgeSetupUIStubAborted
	}
	draft := u.Drafts[0]
	u.Drafts = u.Drafts[1:]
	return draft, nil
}

func (u *AgeSetupUIStub[T]) ConfirmAddAnotherRecipient(ctx context.Context, currentCount int) (bool, error) {
	u.AddCalls++
	if len(u.AddMore) == 0 {
		return false, nil
	}
	next := u.AddMore[0]
	u.AddMore = u.AddMore[1:]
	return next, nil
}
