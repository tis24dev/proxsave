package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"github.com/tis24dev/proxsave/pkg/bech32"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"
)

var ErrAgeRecipientSetupAborted = errors.New("encryption setup aborted by user")

const (
	// Note: dual salt for passphrase-derived keys — keep legacy for decrypting older archives.
	passphraseRecipientSalt       = "proxsave/age-passphrase/v1"
	legacyPassphraseRecipientSalt = "proxmox-backup-go/age-passphrase/v1"
	passphraseScryptN             = 1 << 15
	passphraseScryptR             = 8
	passphraseScryptP             = 1
	minPassphraseLength           = 12
)

var weakPassphraseList = []string{
	"password",
	"123456",
	"123456789",
	"qwerty",
	"abc123",
	"letmein",
	"admin",
	"welcome",
	"iloveyou",
	"monkey",
}

var readPassword = term.ReadPassword

func (o *Orchestrator) EnsureAgeRecipientsReady(ctx context.Context) error {
	if o == nil || o.cfg == nil || !o.cfg.EncryptArchive {
		return nil
	}
	_, err := o.prepareAgeRecipients(ctx)
	return err
}

func (o *Orchestrator) prepareAgeRecipients(ctx context.Context) ([]age.Recipient, error) {
	if o.cfg == nil || !o.cfg.EncryptArchive {
		return nil, nil
	}

	if o.ageRecipientCache != nil && !o.forceNewAgeRecipient {
		return cloneRecipients(o.ageRecipientCache), nil
	}

	recipients, candidatePath, err := o.collectRecipientStrings()
	if err != nil {
		return nil, err
	}

	if len(recipients) == 0 {
		if !o.isInteractiveShell() {
			o.logger.Error("Encryption setup requires interaction. Run the script interactively to complete the AGE recipient setup, then re-run in automated mode.")
			o.logger.Debug("HINT Set AGE_RECIPIENT or AGE_RECIPIENT_FILE to bypass the interactive setup and re-run.")
			return nil, fmt.Errorf("age recipients not configured")
		}

		wizardRecipients, savedPath, err := o.runAgeSetupWizard(ctx, candidatePath)
		if err != nil {
			return nil, err
		}
		recipients = append(recipients, wizardRecipients...)
		if o.cfg.AgeRecipientFile == "" {
			o.cfg.AgeRecipientFile = savedPath
		}
	}

	if len(recipients) == 0 {
		return nil, fmt.Errorf("no AGE recipients configured after setup")
	}

	parsed, err := parseRecipientStrings(recipients)
	if err != nil {
		return nil, err
	}
	o.ageRecipientCache = cloneRecipients(parsed)
	o.forceNewAgeRecipient = false
	return cloneRecipients(parsed), nil
}

func (o *Orchestrator) collectRecipientStrings() ([]string, string, error) {
	recipients := make([]string, 0)
	if o.cfg != nil && !o.forceNewAgeRecipient {
		recipients = append(recipients, o.cfg.AgeRecipients...)
	}

	candidatePath := strings.TrimSpace(o.cfg.AgeRecipientFile)
	if candidatePath == "" {
		candidatePath = o.defaultAgeRecipientFile()
	}

	if candidatePath != "" && !o.forceNewAgeRecipient {
		fileRecipients, err := readRecipientFile(candidatePath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, candidatePath, fmt.Errorf("read AGE recipients from %s: %w", candidatePath, err)
			}
		} else {
			recipients = append(recipients, fileRecipients...)
		}
	}

	return dedupeRecipientStrings(recipients), candidatePath, nil
}

// runAgeSetupWizard collects AGE recipients interactively.
// Returns (fileRecipients, savedPath, error)
func (o *Orchestrator) runAgeSetupWizard(ctx context.Context, candidatePath string) ([]string, string, error) {
	reader := bufio.NewReader(os.Stdin)
	targetPath := candidatePath
	if targetPath == "" {
		targetPath = o.defaultAgeRecipientFile()
	}

	o.logger.Info("Encryption setup: no AGE recipients found, starting interactive wizard")
	if targetPath == "" {
		return nil, "", fmt.Errorf("unable to determine default path for AGE recipients")
	}

	// Create a child context for the wizard to handle Ctrl+C locally
	wizardCtx, wizardCancel := context.WithCancel(ctx)
	defer wizardCancel()

	// Register local SIGINT handler for wizard - treat Ctrl+C as "Exit setup"
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)
	defer signal.Stop(sigChan) // Cleanup: restore normal signal handling after wizard

	// Handle SIGINT as "exit wizard" instead of "graceful shutdown"
	go func() {
		select {
		case <-sigChan:
			fmt.Println("\n^C detected - exiting setup...")
			wizardCancel()
		case <-wizardCtx.Done():
			// Wizard completed normally or parent context cancelled
		}
	}()

	recipientPath := targetPath
	if o.forceNewAgeRecipient && recipientPath != "" {
		if _, err := os.Stat(recipientPath); err == nil {
			fmt.Printf("WARNING: this will remove the existing AGE recipients stored at %s. Existing backups remain decryptable with your old private key.\n", recipientPath)
			confirm, errPrompt := promptYesNo(wizardCtx, reader, fmt.Sprintf("Delete %s and enter a new recipient? [y/N]: ", recipientPath))
			if errPrompt != nil {
				return nil, "", errPrompt
			}
			if !confirm {
				return nil, "", fmt.Errorf("operation aborted by user")
			}
			if err := backupExistingRecipientFile(recipientPath); err != nil {
				fmt.Printf("NOTE: %v\n", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("failed to inspect existing AGE recipients at %s: %w", recipientPath, err)
		}
	}

	recipients := make([]string, 0)
	for {
		fmt.Println("\n[1] Use an existing AGE public key")
		fmt.Println("[2] Generate an AGE public key using a personal passphrase/password — not stored on the server")
		fmt.Println("[3] Generate an AGE public key from an existing personal private key — not stored on the server")
		fmt.Println("[4] Exit setup")
		option, err := promptOption(wizardCtx, reader, "Select an option [1-4]: ")
		if err != nil {
			return nil, "", err
		}
		if option == "4" {
			return nil, "", ErrAgeRecipientSetupAborted
		}

		var value string
		switch option {
		case "1":
			value, err = promptPublicRecipient(wizardCtx, reader)
		case "2":
			value, err = promptPassphraseRecipient(wizardCtx)
			if err == nil {
				o.logger.Info("Derived deterministic AGE public key from passphrase (no secrets stored)")
			}
		case "3":
			value, err = promptPrivateKeyRecipient(wizardCtx)
		}
		if err != nil {
			o.logger.Warning("Encryption setup: %v", err)
			continue
		}
		if value != "" {
			recipients = append(recipients, value)
		}

		more, err := promptYesNo(wizardCtx, reader, "Add another recipient? [y/N]: ")
		if err != nil {
			return nil, "", err
		}
		if !more {
			break
		}
	}

	if len(recipients) == 0 {
		return nil, "", fmt.Errorf("no recipients provided")
	}

	if err := writeRecipientFile(targetPath, dedupeRecipientStrings(recipients)); err != nil {
		return nil, "", err
	}

	o.logger.Info("Saved AGE recipient to %s", targetPath)
	o.logger.Info("Reminder: keep the AGE private key offline; the server stores only recipients.")
	return recipients, targetPath, nil
}

func (o *Orchestrator) defaultAgeRecipientFile() string {
	if o.cfg == nil || o.cfg.BaseDir == "" {
		return ""
	}
	return filepath.Join(o.cfg.BaseDir, "identity", "age", "recipient.txt")
}

func (o *Orchestrator) isInteractiveShell() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func promptOption(ctx context.Context, reader *bufio.Reader, prompt string) (string, error) {
	for {
		fmt.Print(prompt)
		input, err := readLineWithContext(ctx, reader)
		if err != nil {
			return "", err
		}
		sw := strings.TrimSpace(input)
		switch sw {
		case "1", "2", "3", "4":
			return sw, nil
		case "":
			continue
		}
		fmt.Println("Please enter 1, 2, 3 or 4.")
	}
}

func promptPublicRecipient(ctx context.Context, reader *bufio.Reader) (string, error) {
	fmt.Print("Paste your AGE public recipient (starts with \"age1...\"). Press Enter when done: ")
	line, err := readLineWithContext(ctx, reader)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return "", fmt.Errorf("recipient cannot be empty")
	}
	return value, nil
}

func promptPrivateKeyRecipient(ctx context.Context) (string, error) {
	fmt.Print("Paste your AGE private key (not stored; input is not echoed). Press Enter when done: ")
	secretBytes, err := readPasswordWithContext(ctx)
	fmt.Println()
	if err != nil {
		return "", err
	}
	defer zeroBytes(secretBytes)

	secret := strings.TrimSpace(string(secretBytes))
	defer resetString(&secret)
	if secret == "" {
		return "", fmt.Errorf("private key cannot be empty")
	}
	identity, err := age.ParseX25519Identity(secret)
	if err != nil {
		return "", fmt.Errorf("invalid AGE private key: %w", err)
	}
	return identity.Recipient().String(), nil
}

// promptPassphraseRecipient derives a deterministic AGE public key from a passphrase
func promptPassphraseRecipient(ctx context.Context) (string, error) {
	pass, err := promptAndConfirmPassphrase(ctx)
	if err != nil {
		return "", err
	}
	defer resetString(&pass)

	recipient, err := deriveDeterministicRecipientFromPassphrase(pass)
	if err != nil {
		return "", err
	}
	return recipient, nil
}

// promptAndConfirmPassphrase asks the user to enter a passphrase twice and checks strength.
func promptAndConfirmPassphrase(ctx context.Context) (string, error) {
	fmt.Print("Enter the passphrase to derive your AGE public key (input is not echoed). Press Enter when done: ")
	passBytes, err := readPasswordWithContext(ctx)
	fmt.Println()
	if err != nil {
		return "", err
	}
	defer zeroBytes(passBytes)

	trimmed := bytes.TrimSpace(passBytes)
	if len(trimmed) == 0 {
		return "", fmt.Errorf("passphrase cannot be empty")
	}
	if err := validatePassphraseStrength(trimmed); err != nil {
		return "", err
	}
	pass := string(trimmed)
	zeroBytes(trimmed)

	fmt.Print("Re-enter the passphrase to confirm: ")
	confirmBytes, err := readPasswordWithContext(ctx)
	fmt.Println()
	if err != nil {
		resetString(&pass)
		return "", err
	}
	defer zeroBytes(confirmBytes)

	confirmTrimmed := bytes.TrimSpace(confirmBytes)
	if len(confirmTrimmed) == 0 {
		resetString(&pass)
		return "", fmt.Errorf("confirmation cannot be empty")
	}
	if string(confirmTrimmed) != pass {
		resetString(&pass)
		return "", fmt.Errorf("passphrases do not match")
	}
	zeroBytes(confirmTrimmed)

	return pass, nil
}

func promptYesNo(ctx context.Context, reader *bufio.Reader, prompt string) (bool, error) {
	fmt.Print(prompt)
	input, err := readLineWithContext(ctx, reader)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func dedupeRecipientStrings(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func parseRecipientStrings(values []string) ([]age.Recipient, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("no AGE recipients configured")
	}
	parsed := make([]age.Recipient, 0, len(values))
	for _, value := range dedupeRecipientStrings(values) {
		recipient, err := parseRecipientString(value)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, recipient)
	}
	return parsed, nil
}

func parseRecipientString(value string) (age.Recipient, error) {
	switch {
	case strings.HasPrefix(value, "age1"):
		return age.ParseX25519Recipient(value)
	case strings.HasPrefix(strings.ToLower(value), "ssh-"):
		return agessh.ParseRecipient(value)
	default:
		return nil, fmt.Errorf("unsupported AGE recipient format: %s", value)
	}
}

func readRecipientFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var recipients []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		recipients = append(recipients, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return recipients, nil
}

func writeRecipientFile(path string, recipients []string) error {
	if len(recipients) == 0 {
		return fmt.Errorf("no recipients to write")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create recipient directory: %w", err)
	}
	content := strings.Join(recipients, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write recipient file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod recipient file: %w", err)
	}
	return nil
}

func cloneRecipients(src []age.Recipient) []age.Recipient {
	if len(src) == 0 {
		return nil
	}
	dst := make([]age.Recipient, len(src))
	copy(dst, src)
	return dst
}

func mapInputError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
		return ErrAgeRecipientSetupAborted
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "use of closed file") ||
		strings.Contains(errStr, "bad file descriptor") ||
		strings.Contains(errStr, "file already closed") {
		return ErrAgeRecipientSetupAborted
	}
	return err
}

// readLineWithContext reads a single line from the reader and supports cancellation.
func readLineWithContext(ctx context.Context, r *bufio.Reader) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, e := r.ReadString('\n')
		ch <- res{s: s, err: mapInputError(e)}
	}()
	select {
	case <-ctx.Done():
		return "", ErrAgeRecipientSetupAborted
	case out := <-ch:
		return out.s, out.err
	}
}

// readPasswordWithContext reads a password (no echo) and supports cancellation.
func readPasswordWithContext(ctx context.Context) ([]byte, error) {
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, e := readPassword(int(os.Stdin.Fd()))
		ch <- res{b: b, err: mapInputError(e)}
	}()
	select {
	case <-ctx.Done():
		return nil, ErrAgeRecipientSetupAborted
	case out := <-ch:
		return out.b, out.err
	}
}

func backupExistingRecipientFile(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	backupPath := fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
	if err := os.Rename(path, backupPath); err != nil {
		if removeErr := os.Remove(path); removeErr != nil {
			return fmt.Errorf("failed to backup recipient file: %w (also failed to remove: %v)", err, removeErr)
		}
		return fmt.Errorf("renamed recipient file failed, removed original: %w", err)
	}
	return nil
}

// DeriveDeterministicRecipientFromPassphrase derives an AGE recipient from a passphrase (exported for TUI wizard)
func DeriveDeterministicRecipientFromPassphrase(passphrase string) (string, error) {
	return deriveDeterministicRecipientFromPassphrase(passphrase)
}

func deriveDeterministicRecipientFromPassphrase(passphrase string) (string, error) {
	return deriveDeterministicRecipientFromPassphraseWithSalt(passphrase, passphraseRecipientSalt)
}

func deriveDeterministicRecipientFromPassphraseWithSalt(passphrase, salt string) (string, error) {
	key, err := deriveCurve25519ScalarFromPassphraseWithSalt(passphrase, salt)
	if err != nil {
		return "", err
	}
	public, err := curve25519.X25519(key, curve25519.Basepoint)
	if err != nil {
		return "", fmt.Errorf("derive X25519 public key: %w", err)
	}
	recipient, err := bech32.Encode("age", public)
	if err != nil {
		return "", fmt.Errorf("encode passphrase recipient: %w", err)
	}
	return recipient, nil
}

func clampCurve25519Scalar(k []byte) {
	if len(k) != curve25519.ScalarSize {
		return
	}
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
}

func deriveCurve25519ScalarFromPassphrase(passphrase string) ([]byte, error) {
	return deriveCurve25519ScalarFromPassphraseWithSalt(passphrase, passphraseRecipientSalt)
}

func deriveCurve25519ScalarFromPassphraseWithSalt(passphrase, salt string) ([]byte, error) {
	key, err := scrypt.Key([]byte(passphrase), []byte(salt), passphraseScryptN, passphraseScryptR, passphraseScryptP, curve25519.ScalarSize)
	if err != nil {
		return nil, fmt.Errorf("derive key from passphrase: %w", err)
	}
	clampCurve25519Scalar(key)
	return key, nil
}

func deriveDeterministicIdentityFromPassphrase(passphrase string) (age.Identity, error) {
	return deriveDeterministicIdentityFromPassphraseWithSalt(passphrase, passphraseRecipientSalt)
}

func deriveDeterministicIdentityFromPassphraseWithSalt(passphrase, salt string) (age.Identity, error) {
	key, err := deriveCurve25519ScalarFromPassphraseWithSalt(passphrase, salt)
	if err != nil {
		return nil, err
	}
	secret, err := bech32.Encode("AGE-SECRET-KEY-", key)
	if err != nil {
		return nil, fmt.Errorf("encode secret key: %w", err)
	}
	secret = strings.ToUpper(secret)
	return age.ParseX25519Identity(secret)
}

func deriveDeterministicIdentitiesFromPassphrase(passphrase string) ([]age.Identity, error) {
	salts := []string{passphraseRecipientSalt, legacyPassphraseRecipientSalt}
	seen := make(map[string]struct{}, len(salts))
	ids := make([]age.Identity, 0, len(salts))

	for _, salt := range salts {
		id, err := deriveDeterministicIdentityFromPassphraseWithSalt(passphrase, salt)
		if err != nil {
			return nil, err
		}
		rec, err := deriveDeterministicRecipientFromPassphraseWithSalt(passphrase, salt)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[rec]; ok {
			continue
		}
		seen[rec] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func validatePassphraseStrength(pass []byte) error {
	passStr := string(pass)
	if len(passStr) < minPassphraseLength {
		return fmt.Errorf("passphrase too short; use at least %d characters", minPassphraseLength)
	}

	var hasLower, hasUpper, hasDigit, hasSymbol bool
	for _, r := range passStr {
		switch {
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			hasSymbol = true
		}
	}

	classes := 0
	for _, flag := range []bool{hasLower, hasUpper, hasDigit, hasSymbol} {
		if flag {
			classes++
		}
	}
	if classes < 3 {
		return fmt.Errorf("passphrase must include characters from at least three categories (uppercase, lowercase, digits, symbols)")
	}

	lower := strings.ToLower(passStr)
	for _, weak := range weakPassphraseList {
		if lower == weak {
			return fmt.Errorf("passphrase is too common; choose a more unique phrase")
		}
	}
	return nil
}
