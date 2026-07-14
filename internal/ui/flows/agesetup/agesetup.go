// Package agesetup implements orchestrator.AgeSetupUI on the Charm shell:
// the interactive AGE recipient setup used by --newkey and by the installer
// when encryption is enabled. The caller owns the Session (one per setup
// flow). Parity reference: the deleted tview wizard
// (internal/tui/wizard/age.go at commit 8544777).
package agesetup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// UI implements orchestrator.AgeSetupUI.
type UI struct {
	session *shell.Session
}

// New builds the Charm AgeSetupUI on an existing Session.
func New(session *shell.Session) *UI {
	return &UI{session: session}
}

// errBackToKind returns from an input step to the method selector.
var errBackToKind = errors.New("agesetup: back to method selection")

func (u *UI) ConfirmOverwriteExistingRecipient(ctx context.Context, recipientPath string) (bool, error) {
	res, err := shell.Ask(ctx, u.session, components.NewConfirm(
		"Existing AGE recipient",
		fmt.Sprintf("Existing recipient:\n%s\n\nOverwriting stores a new recipient. Existing backups remain decryptable with your old private key.\n\nDelete it and enter a new recipient?", recipientPath),
		components.WithLabels("Overwrite", "Cancel"),
		components.WithDefaultYes(false),
		components.WithDanger(),
	))
	if err != nil {
		if errors.Is(err, shell.ErrAborted) {
			// Parity with tview: Ctrl+C on the modal behaved like Cancel.
			return false, nil
		}
		return false, err
	}
	return res.Answer, nil
}

func (u *UI) CollectRecipientDraft(ctx context.Context, recipientPath string) (*orchestrator.AgeRecipientDraft, error) {
	type kind int
	const (
		kindExisting kind = iota
		kindPassphrase
		kindPrivateKey
	)
	items := []components.SelectorItem[kind]{
		{
			Label:       "Use an existing AGE public key",
			Description: "Paste an age1... or SSH recipient",
			Value:       kindExisting,
		},
		{
			Label:       "Generate from a passphrase",
			Description: "Derives the key from a personal passphrase; not stored on the server",
			Value:       kindPassphrase,
		},
		{
			Label:       "Generate from an existing private key",
			Description: "Derives the public key from an AGE-SECRET-KEY; not stored on the server",
			Value:       kindPrivateKey,
		},
	}

	for {
		choice, err := shell.Ask(ctx, u.session, components.NewSelector(
			"AGE encryption setup", items,
			components.WithSelectorPrompt[kind]("Configure encryption for your backups using the AGE encryption tool.\nChoose how you want to set up your encryption key."),
			components.WithSelectorBack[kind](orchestrator.ErrAgeRecipientSetupAborted),
		))
		if err != nil {
			return nil, u.mapAbort(err)
		}

		switch choice {
		case kindExisting:
			value, err := shell.Ask(ctx, u.session, components.NewInput(
				"AGE recipient",
				"Enter the AGE or SSH public recipient.",
				components.WithValidate(func(v string) error {
					key := strings.TrimSpace(v)
					if key == "" {
						return fmt.Errorf("recipient cannot be empty")
					}
					return orchestrator.ValidateRecipientString(key)
				}),
				components.WithInputBack(errBackToKind),
			))
			if errors.Is(err, errBackToKind) {
				continue
			}
			if err != nil {
				return nil, u.mapAbort(err)
			}
			return &orchestrator.AgeRecipientDraft{
				Kind:      orchestrator.AgeRecipientInputExisting,
				PublicKey: strings.TrimSpace(value),
			}, nil

		case kindPassphrase:
			passphrase, err := shell.Ask(ctx, u.session, components.NewInput(
				"Passphrase",
				"Enter the passphrase used to derive the encryption key.",
				components.WithSecret(),
				components.WithNote("The passphrase is not stored on the server. Keep it offline and secure."),
				components.WithValidate(func(v string) error {
					pass := strings.TrimSpace(v)
					if pass == "" {
						return fmt.Errorf("passphrase cannot be empty")
					}
					return orchestrator.ValidatePassphraseStrength(pass)
				}),
				components.WithInputBack(errBackToKind),
			))
			if errors.Is(err, errBackToKind) {
				continue
			}
			if err != nil {
				return nil, u.mapAbort(err)
			}
			pass := strings.TrimSpace(passphrase)

			_, err = shell.Ask(ctx, u.session, components.NewInput(
				"Confirm passphrase",
				"Re-enter the same passphrase.",
				components.WithSecret(),
				components.WithValidate(func(v string) error {
					if strings.TrimSpace(v) != pass {
						return fmt.Errorf("passphrases do not match")
					}
					return nil
				}),
				components.WithInputBack(errBackToKind),
			))
			if errors.Is(err, errBackToKind) {
				continue
			}
			if err != nil {
				return nil, u.mapAbort(err)
			}
			return &orchestrator.AgeRecipientDraft{
				Kind:       orchestrator.AgeRecipientInputPassphrase,
				Passphrase: pass,
			}, nil

		case kindPrivateKey:
			privateKey, err := shell.Ask(ctx, u.session, components.NewInput(
				"AGE private key",
				"Enter the AGE private key (AGE-SECRET-KEY-...).",
				components.WithSecret(),
				components.WithNote("The private key is not stored on the server. Keep it offline and secure."),
				components.WithValidate(func(v string) error {
					key := strings.TrimSpace(v)
					if key == "" {
						return fmt.Errorf("private key cannot be empty")
					}
					return orchestrator.ValidateAgePrivateKeyString(key)
				}),
				components.WithInputBack(errBackToKind),
			))
			if errors.Is(err, errBackToKind) {
				continue
			}
			if err != nil {
				return nil, u.mapAbort(err)
			}
			return &orchestrator.AgeRecipientDraft{
				Kind:       orchestrator.AgeRecipientInputPrivateKey,
				PrivateKey: strings.TrimSpace(privateKey),
			}, nil
		}
	}
}

func (u *UI) ConfirmAddAnotherRecipient(ctx context.Context, currentCount int) (bool, error) {
	res, err := shell.Ask(ctx, u.session, components.NewConfirm(
		"Add another recipient",
		fmt.Sprintf("Recipient(s) added: %d\n\nAdd another recipient?", currentCount),
		components.WithLabels("Add another", "Finish"),
		components.WithDefaultYes(false),
	))
	if err != nil {
		if errors.Is(err, shell.ErrAborted) {
			// Parity with tview: Ctrl+C behaved like Finish (recipients
			// already saved keep working).
			return false, nil
		}
		return false, err
	}
	return res.Answer, nil
}

func (u *UI) mapAbort(err error) error {
	if errors.Is(err, shell.ErrAborted) {
		return orchestrator.ErrAgeRecipientSetupAborted
	}
	return err
}
