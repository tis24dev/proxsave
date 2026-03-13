package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/tis24dev/proxsave/internal/logging"
)

type cliAgeSetupUI struct {
	reader *bufio.Reader
	logger *logging.Logger
}

func newCLIAgeSetupUI(reader *bufio.Reader, logger *logging.Logger) AgeSetupUI {
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}
	return &cliAgeSetupUI{
		reader: reader,
		logger: logger,
	}
}

func (u *cliAgeSetupUI) ConfirmOverwriteExistingRecipient(ctx context.Context, recipientPath string) (bool, error) {
	fmt.Printf("WARNING: this will remove the existing AGE recipients stored at %s. Existing backups remain decryptable with your old private key.\n", recipientPath)
	return promptYesNoAge(ctx, u.reader, fmt.Sprintf("Delete %s and enter a new recipient? [y/N]: ", recipientPath))
}

func (u *cliAgeSetupUI) CollectRecipientDraft(ctx context.Context, recipientPath string) (*AgeRecipientDraft, error) {
	for {
		fmt.Println("\n[1] Use an existing AGE public key")
		fmt.Println("[2] Generate an AGE public key using a personal passphrase/password - not stored on the server")
		fmt.Println("[3] Generate an AGE public key from an existing personal private key - not stored on the server")
		fmt.Println("[4] Exit setup")

		option, err := promptOptionAge(ctx, u.reader, "Select an option [1-4]: ")
		if err != nil {
			return nil, err
		}
		if option == "4" {
			return nil, ErrAgeRecipientSetupAborted
		}

		switch option {
		case "1":
			value, err := promptPublicRecipientAge(ctx, u.reader)
			if err != nil {
				u.warn(err)
				continue
			}
			return &AgeRecipientDraft{Kind: AgeRecipientInputExisting, PublicKey: value}, nil
		case "2":
			passphrase, err := promptAndConfirmPassphraseAge(ctx)
			if err != nil {
				u.warn(err)
				continue
			}
			return &AgeRecipientDraft{Kind: AgeRecipientInputPassphrase, Passphrase: passphrase}, nil
		case "3":
			privateKey, err := promptPrivateKeyValueAge(ctx)
			if err != nil {
				u.warn(err)
				continue
			}
			return &AgeRecipientDraft{Kind: AgeRecipientInputPrivateKey, PrivateKey: privateKey}, nil
		}
	}
}

func (u *cliAgeSetupUI) ConfirmAddAnotherRecipient(ctx context.Context, currentCount int) (bool, error) {
	return promptYesNoAge(ctx, u.reader, "Add another recipient? [y/N]: ")
}

func (u *cliAgeSetupUI) warn(err error) {
	if err == nil {
		return
	}
	if u.logger != nil {
		u.logger.Warning("Encryption setup: %v", err)
		return
	}
	fmt.Printf("WARNING: %v\n", err)
}
