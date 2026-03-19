package orchestrator

import "context"

type AgeRecipientInputKind int

const (
	AgeRecipientInputExisting AgeRecipientInputKind = iota
	AgeRecipientInputPassphrase
	AgeRecipientInputPrivateKey
)

type AgeRecipientDraft struct {
	Kind       AgeRecipientInputKind
	PublicKey  string
	Passphrase string
	PrivateKey string
}

type AgeSetupUI interface {
	ConfirmOverwriteExistingRecipient(ctx context.Context, recipientPath string) (bool, error)
	CollectRecipientDraft(ctx context.Context, recipientPath string) (*AgeRecipientDraft, error)
	ConfirmAddAnotherRecipient(ctx context.Context, currentCount int) (bool, error)
}
