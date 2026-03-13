package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"filippo.io/age"
)

type AgeRecipientSetupResult struct {
	RecipientPath            string
	WroteRecipientFile       bool
	ReusedExistingRecipients bool
}

func (o *Orchestrator) EnsureAgeRecipientsReadyWithUI(ctx context.Context, ui AgeSetupUI) error {
	if o == nil || o.cfg == nil || !o.cfg.EncryptArchive {
		return nil
	}
	_, _, err := o.prepareAgeRecipientsWithUI(ctx, ui)
	return err
}

func (o *Orchestrator) EnsureAgeRecipientsReadyWithUIDetails(ctx context.Context, ui AgeSetupUI) (*AgeRecipientSetupResult, error) {
	if o == nil || o.cfg == nil || !o.cfg.EncryptArchive {
		return nil, nil
	}
	_, result, err := o.prepareAgeRecipientsWithUI(ctx, ui)
	return result, err
}

func (o *Orchestrator) EnsureAgeRecipientsReadyWithDetails(ctx context.Context) (*AgeRecipientSetupResult, error) {
	return o.EnsureAgeRecipientsReadyWithUIDetails(ctx, nil)
}

func (o *Orchestrator) prepareAgeRecipientsWithUI(ctx context.Context, ui AgeSetupUI) ([]age.Recipient, *AgeRecipientSetupResult, error) {
	if o.cfg == nil || !o.cfg.EncryptArchive {
		return nil, nil, nil
	}

	if o.ageRecipientCache != nil && !o.forceNewAgeRecipient {
		return cloneRecipients(o.ageRecipientCache), &AgeRecipientSetupResult{ReusedExistingRecipients: true}, nil
	}

	recipients, candidatePath, err := o.collectRecipientStrings()
	if err != nil {
		return nil, nil, err
	}

	result := &AgeRecipientSetupResult{}
	if len(recipients) > 0 && !o.forceNewAgeRecipient {
		result.ReusedExistingRecipients = true
	}

	if len(recipients) == 0 {
		if ui == nil {
			if !o.isInteractiveShell() {
				if o.logger != nil {
					o.logger.Error("Encryption setup requires interaction. Run the script interactively to complete the AGE recipient setup, then re-run in automated mode.")
					o.logger.Debug("HINT Set AGE_RECIPIENT or AGE_RECIPIENT_FILE to bypass the interactive setup and re-run.")
				}
				return nil, nil, fmt.Errorf("age recipients not configured")
			}
			ui = newCLIAgeSetupUI(nil, o.logger)
		}

		wizardRecipients, setupResult, err := o.runAgeSetupWorkflow(ctx, candidatePath, ui)
		if err != nil {
			return nil, nil, err
		}
		recipients = append(recipients, wizardRecipients...)
		result = setupResult
		if o.cfg.AgeRecipientFile == "" {
			o.cfg.AgeRecipientFile = setupResult.RecipientPath
		}
	}

	if len(recipients) == 0 {
		return nil, nil, fmt.Errorf("no AGE recipients configured after setup")
	}

	parsed, err := parseRecipientStrings(recipients)
	if err != nil {
		return nil, nil, err
	}
	o.ageRecipientCache = cloneRecipients(parsed)
	o.forceNewAgeRecipient = false
	return cloneRecipients(parsed), result, nil
}

func (o *Orchestrator) runAgeSetupWorkflow(ctx context.Context, candidatePath string, ui AgeSetupUI) ([]string, *AgeRecipientSetupResult, error) {
	targetPath := strings.TrimSpace(candidatePath)
	if targetPath == "" {
		targetPath = o.defaultAgeRecipientFile()
	}
	if targetPath == "" {
		return nil, nil, fmt.Errorf("unable to determine default path for AGE recipients")
	}

	if o.logger != nil {
		o.logger.Info("Encryption setup: no AGE recipients found, starting interactive wizard")
	}

	if o.forceNewAgeRecipient {
		if _, err := os.Stat(targetPath); err == nil {
			confirm, err := ui.ConfirmOverwriteExistingRecipient(ctx, targetPath)
			if err != nil {
				return nil, nil, mapAgeSetupAbort(err)
			}
			if !confirm {
				return nil, nil, ErrAgeRecipientSetupAborted
			}
			if err := backupExistingRecipientFile(targetPath); err != nil && o.logger != nil {
				o.logger.Warning("NOTE: %v", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("failed to inspect existing AGE recipients at %s: %w", targetPath, err)
		}
	}

	recipients := make([]string, 0)
	for {
		draft, err := ui.CollectRecipientDraft(ctx, targetPath)
		if err != nil {
			return nil, nil, mapAgeSetupAbort(err)
		}
		if draft == nil {
			return nil, nil, ErrAgeRecipientSetupAborted
		}

		value, err := resolveAgeRecipientDraft(draft)
		if err != nil {
			if o.logger != nil {
				o.logger.Warning("Encryption setup: %v", err)
			}
			continue
		}
		recipients = append(recipients, value)

		more, err := ui.ConfirmAddAnotherRecipient(ctx, len(recipients))
		if err != nil {
			return nil, nil, mapAgeSetupAbort(err)
		}
		if !more {
			break
		}
	}

	recipients = dedupeRecipientStrings(recipients)
	if len(recipients) == 0 {
		return nil, nil, fmt.Errorf("no recipients provided")
	}

	if err := writeRecipientFile(targetPath, recipients); err != nil {
		return nil, nil, err
	}

	if o.logger != nil {
		o.logger.Info("Saved AGE recipient to %s", targetPath)
		o.logger.Info("Reminder: keep the AGE private key offline; the server stores only recipients.")
	}
	return recipients, &AgeRecipientSetupResult{
		RecipientPath:      targetPath,
		WroteRecipientFile: true,
	}, nil
}

func resolveAgeRecipientDraft(draft *AgeRecipientDraft) (string, error) {
	if draft == nil {
		return "", fmt.Errorf("recipient draft is required")
	}

	switch draft.Kind {
	case AgeRecipientInputExisting:
		value := strings.TrimSpace(draft.PublicKey)
		if err := ValidateRecipientString(value); err != nil {
			return "", err
		}
		return value, nil
	case AgeRecipientInputPassphrase:
		passphrase := strings.TrimSpace(draft.Passphrase)
		defer resetString(&passphrase)
		if passphrase == "" {
			return "", fmt.Errorf("passphrase cannot be empty")
		}
		if err := validatePassphraseStrength([]byte(passphrase)); err != nil {
			return "", err
		}
		recipient, err := deriveDeterministicRecipientFromPassphrase(passphrase)
		if err != nil {
			return "", err
		}
		return recipient, nil
	case AgeRecipientInputPrivateKey:
		privateKey := strings.TrimSpace(draft.PrivateKey)
		defer resetString(&privateKey)
		return ParseAgePrivateKeyRecipient(privateKey)
	default:
		return "", fmt.Errorf("unsupported AGE setup input kind: %d", draft.Kind)
	}
}

func mapAgeSetupAbort(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrAgeRecipientSetupAborted) {
		return ErrAgeRecipientSetupAborted
	}
	return err
}
