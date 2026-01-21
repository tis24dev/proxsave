package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// EmailDeliveryMethod represents the email delivery method
type EmailDeliveryMethod string

const (
	EmailDeliveryRelay    EmailDeliveryMethod = "relay"
	EmailDeliverySendmail EmailDeliveryMethod = "sendmail"
	EmailDeliveryPMF      EmailDeliveryMethod = "pmf"
)

// EmailConfig holds email notification configuration
type EmailConfig struct {
	Enabled          bool
	DeliveryMethod   EmailDeliveryMethod
	FallbackSendmail bool
	AttachLogFile    bool
	SubjectOverride  string
	Recipient        string // Empty = auto-detect
	From             string
	CloudRelayConfig CloudRelayConfig
}

// EmailNotifier implements the Notifier interface for Email
type EmailNotifier struct {
	config      EmailConfig
	logger      *logging.Logger
	proxmoxType types.ProxmoxType
}

var (
	// Email validation regex
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

	// sendmailBinaryPath is the binary invoked when EMAIL_DELIVERY_METHOD=sendmail.
	// It is a variable to allow hermetic tests to override it without touching /usr/sbin.
	sendmailBinaryPath = "/usr/sbin/sendmail"

	// pmfLookPathCandidates defines the search order for proxmox-mail-forward.
	// It is a variable to allow hermetic tests to override it and avoid invoking a real system binary.
	pmfLookPathCandidates = []string{
		"/usr/libexec/proxmox-mail-forward",
		"/usr/bin/proxmox-mail-forward",
		"proxmox-mail-forward",
	}

	// Detect queue IDs in sendmail verbose output (e.g., "queued as 1234ABCD123")
	queueIDRegex = regexp.MustCompile(`queued as ([A-Za-z0-9.-]+)`)

	// Detect queue IDs at the beginning of mailq output lines
	mailQueueIDLineRegex = regexp.MustCompile(`^[A-Za-z0-9]{5,}[*!]?$`)

	// Detect remote acceptance IDs from sendmail verbose transcript
	remoteAcceptedRegex = regexp.MustCompile(`Sent\s+\(OK\s+id=([A-Za-z0-9\-]+)\)`)

	// Detect local acceptance IDs from sendmail transcript
	localAcceptedSentRegex = regexp.MustCompile(`Sent\s+\(([A-Za-z0-9\-]+)\s+Message accepted for delivery\)`)
	messageAcceptedRegex   = regexp.MustCompile(`\b([A-Za-z0-9\-]{5,})\b\s+Message accepted for delivery`)

	// Candidate mail log files to inspect for delivery status
	mailLogPaths = []string{
		"/var/log/mail.log",
		"/var/log/maillog",
		"/var/log/mail.err",
	}
)

// NewEmailNotifier creates a new Email notifier
func NewEmailNotifier(config EmailConfig, proxmoxType types.ProxmoxType, logger *logging.Logger) (*EmailNotifier, error) {
	if !config.Enabled {
		return &EmailNotifier{
			config:      config,
			logger:      logger,
			proxmoxType: proxmoxType,
		}, nil
	}

	// Validate delivery method
	if config.DeliveryMethod != EmailDeliveryRelay && config.DeliveryMethod != EmailDeliverySendmail && config.DeliveryMethod != EmailDeliveryPMF {
		return nil, fmt.Errorf("invalid email delivery method: %s (must be 'relay', 'sendmail', or 'pmf')", config.DeliveryMethod)
	}

	// Validate from address
	if config.From == "" {
		config.From = "no-reply@proxmox.tis24.it"
	}

	return &EmailNotifier{
		config:      config,
		logger:      logger,
		proxmoxType: proxmoxType,
	}, nil
}

// Name returns the notifier name
func (e *EmailNotifier) Name() string {
	return "Email"
}

// IsEnabled returns whether email notifications are enabled
func (e *EmailNotifier) IsEnabled() bool {
	return e.config.Enabled
}

// IsCritical returns whether email failures should abort backup (always false)
func (e *EmailNotifier) IsCritical() bool {
	return false // Notification failures never abort backup
}

// Send sends an email notification
func (e *EmailNotifier) Send(ctx context.Context, data *NotificationData) (*NotificationResult, error) {
	startTime := time.Now()
	result := &NotificationResult{
		Method:   "email",
		Metadata: make(map[string]interface{}),
	}

	if !e.config.Enabled {
		e.logger.Debug("Email notifications disabled")
		result.Success = false
		result.Duration = time.Since(startTime)
		return result, nil
	}

	// Always print the configured delivery method at INFO level for operational clarity.
	switch e.config.DeliveryMethod {
	case EmailDeliveryRelay:
		if e.config.FallbackSendmail {
			e.logger.Info("Email delivery method: relay (fallback: pmf enabled)")
		} else {
			e.logger.Info("Email delivery method: relay (fallback: disabled)")
		}
	case EmailDeliverySendmail:
		e.logger.Info("Email delivery method: sendmail (/usr/sbin/sendmail)")
	case EmailDeliveryPMF:
		e.logger.Info("Email delivery method: pmf (proxmox-mail-forward)")
	default:
		e.logger.Info("Email delivery method: %s", e.config.DeliveryMethod)
	}

	// Resolve recipient
	recipient := strings.TrimSpace(e.config.Recipient)
	autoDetected := false
	if recipient == "" {
		e.logger.Debug("Email recipient not configured, attempting auto-detection...")
		detectedRecipient, err := e.detectRecipient(ctx)
		if err != nil {
			e.logger.Warning("WARNING: Failed to detect email recipient: %v", err)
			if e.config.DeliveryMethod == EmailDeliveryPMF {
				e.logger.Info("  Proceeding anyway because EMAIL_DELIVERY_METHOD=pmf routes via Proxmox Notifications; recipient is only used for the To: header")
			} else {
				e.logger.Warning("WARNING: Email notification skipped because no valid recipient is available")
				e.logger.Info("  Configure EMAIL_RECIPIENT or set an email address for root@pam inside Proxmox")
				result.Success = false
				result.Duration = time.Since(startTime)
				result.Error = fmt.Errorf("no valid email recipient: %w", err)
				return result, nil
			}
		} else {
			recipient = detectedRecipient
			e.logger.Debug("Auto-detected email recipient: %s", recipient)
			autoDetected = true
		}
	}

	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		if e.config.DeliveryMethod == EmailDeliveryRelay || e.config.DeliveryMethod == EmailDeliverySendmail {
			e.logger.Warning("WARNING: Email recipient is empty after configuration/detection")
			e.logger.Info("  Configure EMAIL_RECIPIENT or set an email address for root@pam inside Proxmox")
			result.Success = false
			result.Duration = time.Since(startTime)
			result.Error = fmt.Errorf("no valid email recipient configured")
			return result, nil
		}
		e.logger.Warning("WARNING: Email recipient is empty after configuration/detection")
		e.logger.Info("  EMAIL_DELIVERY_METHOD=pmf routes via Proxmox Notifications; recipient is only used for the To: header")
	}

	if e.config.DeliveryMethod == EmailDeliveryRelay && isRootRecipient(recipient) {
		if autoDetected {
			e.logger.Warning("WARNING: Auto-detected recipient %s belongs to root and will be rejected", recipient)
		} else {
			e.logger.Warning("WARNING: Configured email recipient %s belongs to root and will be rejected", recipient)
		}
		e.logger.Info("  Configure EMAIL_RECIPIENT with a non-root mailbox to enable notifications")
		result.Success = false
		result.Duration = time.Since(startTime)
		result.Error = fmt.Errorf("recipient %s is not allowed (root accounts are blocked)", recipient)
		return result, nil
	}

	// Validate recipient email format
	if recipient != "" && !emailRegex.MatchString(recipient) {
		e.logger.Warning("WARNING: Invalid email format: %s", recipient)
	}

	// Build email subject and body
	subject := BuildEmailSubject(data)
	if strings.TrimSpace(e.config.SubjectOverride) != "" {
		subject = strings.TrimSpace(e.config.SubjectOverride)
	}
	htmlBody := BuildEmailHTML(data)
	textBody := BuildEmailPlainText(data)

	// Attempt delivery based on method
	var err error
	var relayErr error // Store original relay error if fallback is used

	if e.config.DeliveryMethod == EmailDeliveryRelay {
		result.Method = "email-relay"
		err = e.sendViaRelay(ctx, recipient, subject, htmlBody, textBody, data)

		// Fallback to PMF if relay fails and fallback is enabled
		if err != nil && e.config.FallbackSendmail {
			relayErr = err // Store original relay error
			e.logger.Warning("WARNING: Cloud relay failed: %v", err)

			e.logger.Info("Attempting fallback to pmf...")

			result.Method = "email-pmf-fallback"
			result.UsedFallback = true
			backend, backendPath, sendErr := e.sendViaPMF(ctx, recipient, subject, htmlBody, textBody, data)
			if backend != "" {
				result.Metadata["email_backend"] = backend
			}
			if backendPath != "" {
				result.Metadata["email_backend_path"] = backendPath
			}
			err = sendErr

			// If fallback succeeds, preserve the original relay error for logging
			if err == nil {
				result.Error = relayErr
			}
		}
	} else if e.config.DeliveryMethod == EmailDeliveryPMF {
		result.Method = "email-pmf"
		backend, backendPath, sendErr := e.sendViaPMF(ctx, recipient, subject, htmlBody, textBody, data)
		if backend != "" {
			result.Metadata["email_backend"] = backend
		}
		if backendPath != "" {
			result.Metadata["email_backend_path"] = backendPath
		}
		err = sendErr
	} else {
		result.Method = "email-sendmail"
		queueID, backend, backendPath, sendErr := e.sendViaSendmail(ctx, recipient, subject, htmlBody, textBody, data)
		if queueID != "" {
			result.Metadata["mail_queue_id"] = queueID
		}
		if backend != "" {
			result.Metadata["email_backend"] = backend
		}
		if backendPath != "" {
			result.Metadata["email_backend_path"] = backendPath
		}
		err = sendErr
	}

	// Handle result
	result.Duration = time.Since(startTime)

	if err != nil {
		// Both primary and fallback failed (or no fallback configured)
		e.logger.Warning("WARNING: Failed to send email notification: %v", err)
		result.Success = false
		result.Error = err
		return result, nil // Non-critical error
	}

	// Success (either primary or fallback)
	if result.UsedFallback {
		// Fallback succeeded after relay failure
		e.logger.Warning("⚠️ Email sent via fallback after relay failure")
	}

	// Log according to delivery method to avoid implying guaranteed inbox delivery
	if result.Method == "email-relay" {
		// Cloud relay confirmed the request (HTTP 200 from worker)
		e.logger.Info("Email relay accepted request for %s (%s)", recipient, describeEmailMethod(result.Method))
	} else {
		// Local delivery path: we only know the message was handed off (not necessarily delivered)
		backend := describeEmailMethod(result.Method)
		if v, ok := result.Metadata["email_backend"].(string); ok && strings.TrimSpace(v) != "" {
			backend = strings.TrimSpace(v)
		}

		recipientHint := strings.TrimSpace(recipient)
		if recipientHint == "" {
			recipientHint = "(recipient not set - routed by Proxmox Notifications)"
		}
		e.logger.Info("Email notification handed off to %s for %s", backend, recipientHint)
	}

	result.Success = true
	return result, nil
}

func describeEmailMethod(method string) string {
	switch method {
	case "email-relay":
		return "cloud relay"
	case "email-sendmail":
		return "sendmail"
	case "email-pmf":
		return "proxmox-mail-forward"
	case "email-pmf-fallback":
		return "proxmox-mail-forward fallback"
	default:
		return method
	}
}

// isRootRecipient detects if the provided recipient belongs to the root user (root@host).
func isRootRecipient(recipient string) bool {
	addr := strings.ToLower(strings.TrimSpace(recipient))
	if addr == "" {
		return false
	}
	parts := strings.SplitN(addr, "@", 2)
	if len(parts) != 2 {
		return false
	}
	return parts[0] == "root"
}

// detectRecipient attempts to auto-detect the email recipient from Proxmox configuration
// Replicates Bash logic: jq -r '.[] | select(.userid=="root@pam") | .email'
func (e *EmailNotifier) detectRecipient(ctx context.Context) (string, error) {
	var cmd *exec.Cmd

	switch e.proxmoxType {
	case types.ProxmoxVE:
		// Try to get root user email from PVE
		cmd = exec.CommandContext(ctx, "pveum", "user", "list", "--output-format", "json")

	case types.ProxmoxBS:
		// Try to get root user email from PBS
		cmd = exec.CommandContext(ctx, "proxmox-backup-manager", "user", "list", "--output-format", "json")

	default:
		return "", fmt.Errorf("unknown Proxmox type: %s", e.proxmoxType)
	}

	// Execute command
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to query Proxmox user list: %w", err)
	}

	// Parse JSON array to find root@pam user
	// Replicates: jq -r '.[] | select(.userid=="root@pam") | .email'
	var users []map[string]interface{}
	if err := json.Unmarshal(output, &users); err != nil {
		return "", fmt.Errorf("failed to parse user list JSON: %w", err)
	}

	// Search for root@pam user specifically
	for _, user := range users {
		userid, useridOk := user["userid"].(string)
		if !useridOk {
			continue
		}

		// Check if this is the root@pam user
		if userid == "root@pam" {
			email, emailOk := user["email"].(string)
			if emailOk && email != "" {
				e.logger.Debug("Found root@pam email: %s", email)
				return email, nil
			}
			// root@pam found but no email configured
			return "", fmt.Errorf("root@pam user exists but has no email configured")
		}
	}

	return "", fmt.Errorf("root@pam user not found in Proxmox configuration")
}

// sendViaRelay sends email via cloud relay
func (e *EmailNotifier) sendViaRelay(ctx context.Context, recipient, subject, htmlBody, textBody string, data *NotificationData) error {
	// Build payload
	payload := EmailRelayPayload{
		To:            recipient,
		Subject:       subject,
		Report:        buildReportData(data),
		Timestamp:     time.Now().Unix(),
		ServerMAC:     data.ServerMAC,
		ScriptVersion: data.ScriptVersion,
		ServerID:      data.ServerID,
	}

	// Send via cloud relay
	return sendViaCloudRelay(ctx, e.config.CloudRelayConfig, payload, e.logger)
}

// isMTAServiceActive checks if a Mail Transfer Agent service is running
func (e *EmailNotifier) isMTAServiceActive(ctx context.Context) (bool, string) {
	services := []string{"postfix", "sendmail", "exim4"}

	if _, err := exec.LookPath("systemctl"); err != nil {
		return false, "systemctl not available"
	}

	for _, service := range services {
		cmd := exec.CommandContext(ctx, "systemctl", "is-active", service)
		if err := cmd.Run(); err == nil {
			e.logger.Debug("MTA service %s is active", service)
			return true, service
		}
	}

	return false, "no MTA service active"
}

// checkMTAConfiguration checks if MTA configuration files exist
func (e *EmailNotifier) checkMTAConfiguration() (bool, string) {
	configFiles := []struct {
		path string
		mta  string
	}{
		{"/etc/postfix/main.cf", "Postfix"},
		{"/etc/mail/sendmail.cf", "Sendmail"},
		{"/etc/exim4/exim4.conf", "Exim4"},
	}

	for _, cf := range configFiles {
		if info, err := os.Stat(cf.path); err == nil && !info.IsDir() {
			e.logger.Debug("Found %s configuration at %s", cf.mta, cf.path)
			return true, cf.mta
		}
	}

	return false, "no MTA configuration found"
}

// checkRelayHostConfigured checks if Postfix relay host is configured
func (e *EmailNotifier) checkRelayHostConfigured(ctx context.Context) (bool, string) {
	configPath := "/etc/postfix/main.cf"
	if _, err := os.Stat(configPath); err != nil {
		return false, "main.cf not found"
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		e.logger.Debug("Failed to read postfix config: %v", err)
		return false, "cannot read config"
	}

	// Look for relayhost setting
	re := regexp.MustCompile(`(?m)^relayhost\s*=\s*(.+)$`)
	matches := re.FindStringSubmatch(string(content))

	if len(matches) > 1 {
		relayhost := strings.TrimSpace(matches[1])
		if relayhost != "" && relayhost != "[]" {
			e.logger.Debug("Relay host configured: %s", relayhost)
			return true, relayhost
		}
	}

	e.logger.Debug("No relay host configured in Postfix")
	return false, "no relay host"
}

// checkMailQueue checks the mail queue status
func (e *EmailNotifier) checkMailQueue(ctx context.Context) (int, error) {
	// Try mailq command (works for both Postfix and Sendmail)
	mailqPath := "/usr/bin/mailq"
	if _, err := exec.LookPath("mailq"); err != nil {
		if _, err := exec.LookPath(mailqPath); err != nil {
			return 0, fmt.Errorf("mailq command not found")
		}
	} else {
		mailqPath = "mailq"
	}

	cmd := exec.CommandContext(ctx, mailqPath)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("mailq failed: %w", err)
	}

	// Parse output to count queued messages
	outputStr := string(output)
	if strings.Contains(outputStr, "Mail queue is empty") {
		e.logger.Debug("Mail queue is empty")
		return 0, nil
	}

	// Count lines that look like queue entries
	lines := strings.Split(outputStr, "\n")
	queueCount := 0
	for _, line := range lines {
		// Basic heuristic: lines with queue IDs (hex strings) and @ symbols
		if len(line) > 10 && strings.Contains(line, "@") {
			// Skip header and footer lines
			if !strings.Contains(line, "Mail queue") && !strings.Contains(line, "Total requests") {
				queueCount++
			}
		}
	}

	if queueCount > 0 {
		e.logger.Debug("Found %d message(s) in mail queue", queueCount)
	}

	return queueCount, nil
}

// detectQueueEntry scans the mail queue for a recipient and returns the latest queue ID.
func (e *EmailNotifier) detectQueueEntry(ctx context.Context, recipient string) (string, string, error) {
	mailqPath := "/usr/bin/mailq"
	if _, err := exec.LookPath("mailq"); err == nil {
		mailqPath = "mailq"
	} else if _, err := exec.LookPath(mailqPath); err != nil {
		return "", "", fmt.Errorf("mailq command not found")
	}

	cmd := exec.CommandContext(ctx, mailqPath)
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("mailq failed: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	lowerRecipient := strings.ToLower(strings.TrimSpace(recipient))
	var currentID string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) > 0 && mailQueueIDLineRegex.MatchString(fields[0]) {
			currentID = strings.TrimSuffix(strings.TrimSuffix(fields[0], "*"), "!")
			continue
		}

		if currentID != "" && lowerRecipient != "" && strings.Contains(strings.ToLower(trimmed), lowerRecipient) {
			return currentID, trimmed, nil
		}
	}

	return "", "", nil
}

// tailMailLog reads the last maxLines from the first available mail log file.
func (e *EmailNotifier) tailMailLog(maxLines int) ([]string, string) {
	for _, logFile := range mailLogPaths {
		if _, err := os.Stat(logFile); err != nil {
			continue
		}

		cmd := exec.Command("tail", "-n", strconv.Itoa(maxLines), logFile)
		output, err := cmd.Output()
		if err != nil {
			continue
		}

		lines := strings.Split(strings.TrimRight(string(output), "\n"), "\n")
		return lines, logFile
	}

	// Fallback to journald if traditional log files are unavailable
	if _, err := exec.LookPath("journalctl"); err == nil {
		args := []string{"--no-pager", "-n", strconv.Itoa(maxLines)}
		for _, unit := range []string{"postfix.service", "sendmail.service", "exim4.service"} {
			args = append(args, "-u", unit)
		}

		cmd := exec.Command("journalctl", args...)
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			lines := strings.Split(strings.TrimRight(string(output), "\n"), "\n")
			return lines, "journalctl"
		}
	}

	return nil, ""
}

// checkRecentMailLogs checks recent mail log entries for errors
func (e *EmailNotifier) checkRecentMailLogs() []string {
	lines, _ := e.tailMailLog(50)
	if len(lines) == 0 {
		return nil
	}

	var errors []string
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") ||
			strings.Contains(lower, "failed") ||
			strings.Contains(lower, "rejected") ||
			strings.Contains(lower, "deferred") ||
			strings.Contains(lower, "connection refused") ||
			strings.Contains(lower, "timeout") {
			errors = append(errors, strings.TrimSpace(line))
		}
	}

	return errors
}

// extractQueueID attempts to parse a queue ID from sendmail verbose output.
func extractQueueID(outputs ...string) string {
	for _, text := range outputs {
		if text == "" {
			continue
		}
		matches := queueIDRegex.FindStringSubmatch(text)
		if len(matches) > 1 {
			return strings.TrimSpace(matches[1])
		}
	}
	return ""
}

// inspectMailLogStatus looks for a delivery status line for the given queue ID.
func (e *EmailNotifier) inspectMailLogStatus(queueID string) (status, matchedLine, logPath string) {
	lines, logPath := e.tailMailLog(80)
	if len(lines) == 0 || logPath == "" {
		return "", "", logPath
	}

	relevant := lines
	if queueID != "" {
		filtered := make([]string, 0, len(lines))
		for _, line := range lines {
			if strings.Contains(line, queueID) {
				filtered = append(filtered, line)
			}
		}
		if len(filtered) > 0 {
			relevant = filtered
		}
	}

	for i := len(relevant) - 1; i >= 0; i-- {
		line := strings.TrimSpace(relevant[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "status=sent"):
			return "sent", line, logPath
		case strings.Contains(lower, "status=deferred"):
			return "deferred", line, logPath
		case strings.Contains(lower, "status=bounced"), strings.Contains(lower, "status=softbounce"):
			return "bounced", line, logPath
		case strings.Contains(lower, "status=expired"):
			return "expired", line, logPath
		case strings.Contains(lower, "status=rejected") || strings.Contains(lower, "rejected "):
			return "rejected", line, logPath
		case strings.Contains(lower, "connection refused"),
			strings.Contains(lower, "host not found"),
			strings.Contains(lower, "no route to host"),
			strings.Contains(lower, "timeout"):
			return "error", line, logPath
		}
	}

	if len(relevant) > 0 {
		line := strings.TrimSpace(relevant[len(relevant)-1])
		if line != "" {
			return "unknown", line, logPath
		}
	}

	return "", "", logPath
}

// logMailLogStatus writes a human-readable summary based on inspectMailLogStatus results.
func (e *EmailNotifier) logMailLogStatus(queueID, status, matchedLine, logPath string) {
	if queueID == "" && status == "" {
		return
	}

	displayID := queueID
	if strings.TrimSpace(displayID) == "" {
		displayID = "(unknown)"
	}

	switch status {
	case "sent":
		e.logger.Info("Mail log (%s) reports status=sent for queue ID %s", logPath, displayID)
	case "deferred":
		e.logger.Warning("Mail log (%s) reports status=deferred for queue ID %s", logPath, displayID)
	case "bounced":
		e.logger.Warning("Mail log (%s) reports status=bounced for queue ID %s", logPath, displayID)
	case "expired":
		e.logger.Warning("Mail log (%s) reports status=expired for queue ID %s", logPath, displayID)
	case "rejected":
		e.logger.Warning("Mail log (%s) reports status=rejected for queue ID %s", logPath, displayID)
	case "error":
		e.logger.Warning("Mail log (%s) reports delivery errors for queue ID %s", logPath, displayID)
	case "unknown":
		e.logger.Debug("Mail log (%s) has entries for queue ID %s, but status is inconclusive", logPath, displayID)
	default:
		if status == "" {
			if queueID != "" && logPath != "" {
				e.logger.Info("Mail log (%s) has no recent entries for queue ID %s (delivery status pending)", logPath, displayID)
			}
			return
		}
	}

	if matchedLine != "" {
		if e.logger.GetLevel() <= types.LogLevelDebug {
			e.logger.Debug("Mail log entry: %s", matchedLine)
		} else if status != "sent" {
			// Surface a truncated version even outside debug when status is problematic
			line := matchedLine
			if len(line) > 200 {
				line = line[:200] + "..."
			}
			e.logger.Info("  Details: %s", line)
		}
	}
}

// summarizeSendmailTranscript extracts human-readable highlights from a verbose sendmail transcript.
func summarizeSendmailTranscript(transcript string) (highlights []string, remoteID string, localQueueID string) {
	lines := strings.Split(transcript, "\n")
	var loggedLocalConn, loggedRemoteConn, loggedRcpt, loggedClose bool

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		lower := strings.ToLower(line)

		switch {
		case !loggedLocalConn && strings.Contains(lower, "connecting to") && strings.Contains(lower, "via relay"):
			highlights = append(highlights, fmt.Sprintf("Local relay connection: %s", line))
			loggedLocalConn = true
			continue
		case !loggedRemoteConn && strings.Contains(lower, "connecting to") && (strings.Contains(lower, "via esmtp") || strings.Contains(lower, "via smtp")) && !strings.Contains(lower, "via relay"):
			highlights = append(highlights, fmt.Sprintf("Remote relay connection: %s", line))
			loggedRemoteConn = true
			continue
		case !loggedRcpt && strings.Contains(lower, "recipient ok"):
			highlights = append(highlights, fmt.Sprintf("Recipient accepted by remote server: %s", line))
			loggedRcpt = true
			continue
		}

		if remoteID == "" {
			if matches := remoteAcceptedRegex.FindStringSubmatch(line); len(matches) > 1 {
				remoteID = matches[1]
				highlights = append(highlights, fmt.Sprintf("Remote server accepted message (remote_id=%s)", remoteID))
				continue
			}
		}

		if localQueueID == "" {
			if matches := localAcceptedSentRegex.FindStringSubmatch(line); len(matches) > 1 {
				localQueueID = matches[1]
				highlights = append(highlights, fmt.Sprintf("Local MTA queued message with ID %s", localQueueID))
				continue
			}
			if matches := messageAcceptedRegex.FindStringSubmatch(line); len(matches) > 1 {
				localQueueID = matches[1]
				highlights = append(highlights, fmt.Sprintf("Local MTA queued message with ID %s", localQueueID))
				continue
			}
		}

		if !loggedClose && strings.Contains(lower, "closing connection") {
			highlights = append(highlights, fmt.Sprintf("SMTP session closed: %s", line))
			loggedClose = true
		}
	}

	return highlights, remoteID, localQueueID
}

func (e *EmailNotifier) buildEmailMessage(recipient, subject, htmlBody, textBody string, data *NotificationData) (emailMessage, toHeader string) {
	e.logger.Debug("=== Building email message ===")

	// Encode subject in Base64 for proper UTF-8 handling
	encodedSubject := base64.StdEncoding.EncodeToString([]byte(subject))

	// Build email headers and body
	var email strings.Builder
	toHeader = strings.TrimSpace(recipient)
	if toHeader == "" {
		toHeader = "root"
	}
	email.WriteString(fmt.Sprintf("To: %s\n", toHeader))
	email.WriteString(fmt.Sprintf("From: %s\n", e.config.From))
	email.WriteString(fmt.Sprintf("Subject: =?UTF-8?B?%s?=\n", encodedSubject))
	email.WriteString("MIME-Version: 1.0\n")

	// Decide whether to attach log file
	attachLog := e.config.AttachLogFile && data != nil && strings.TrimSpace(data.LogFilePath) != ""

	if attachLog {
		// Try to read log file; on failure, fall back to plain multipart/alternative
		logPath := strings.TrimSpace(data.LogFilePath)
		content, err := os.ReadFile(logPath)
		if err != nil {
			e.logger.Warning("Failed to read log file for email attachment (%s): %v", logPath, err)
			attachLog = false
		} else {
			mixedBoundary := "mixed_boundary_42"
			altBoundary := "alt_boundary_42"

			email.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\n", mixedBoundary))
			email.WriteString("\n")

			// First part: multipart/alternative with text and HTML bodies
			email.WriteString(fmt.Sprintf("--%s\n", mixedBoundary))
			email.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\n", altBoundary))
			email.WriteString("\n")

			// Plain text part
			email.WriteString(fmt.Sprintf("--%s\n", altBoundary))
			email.WriteString("Content-Type: text/plain; charset=UTF-8\n")
			email.WriteString("Content-Transfer-Encoding: 8bit\n")
			email.WriteString("\n")
			email.WriteString(textBody)
			email.WriteString("\n\n")

			// HTML part
			email.WriteString(fmt.Sprintf("--%s\n", altBoundary))
			email.WriteString("Content-Type: text/html; charset=UTF-8\n")
			email.WriteString("Content-Transfer-Encoding: 8bit\n")
			email.WriteString("\n")
			email.WriteString(htmlBody)
			email.WriteString("\n\n")

			email.WriteString(fmt.Sprintf("--%s--\n", altBoundary))
			email.WriteString("\n")

			// Second part: log file attachment (Base64 encoded)
			filename := filepath.Base(logPath)
			if filename == "" {
				filename = "backup.log"
			}

			email.WriteString(fmt.Sprintf("--%s\n", mixedBoundary))
			email.WriteString(fmt.Sprintf("Content-Type: text/plain; charset=UTF-8; name=\"%s\"\n", filename))
			email.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\n", filename))
			email.WriteString("Content-Transfer-Encoding: base64\n")
			email.WriteString("\n")

			encoded := base64.StdEncoding.EncodeToString(content)
			const maxLineLength = 76
			for i := 0; i < len(encoded); i += maxLineLength {
				end := i + maxLineLength
				if end > len(encoded) {
					end = len(encoded)
				}
				email.WriteString(encoded[i:end])
				email.WriteString("\n")
			}
			email.WriteString("\n")
			email.WriteString(fmt.Sprintf("--%s--\n", mixedBoundary))
		}
	}

	if !attachLog {
		// Fallback / default: simple multipart/alternative (no attachment)
		altBoundary := "boundary42"
		email.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\n", altBoundary))
		email.WriteString("\n")

		// Plain text part
		email.WriteString(fmt.Sprintf("--%s\n", altBoundary))
		email.WriteString("Content-Type: text/plain; charset=UTF-8\n")
		email.WriteString("Content-Transfer-Encoding: 8bit\n")
		email.WriteString("\n")
		email.WriteString(textBody)
		email.WriteString("\n\n")

		// HTML part
		email.WriteString(fmt.Sprintf("--%s\n", altBoundary))
		email.WriteString("Content-Type: text/html; charset=UTF-8\n")
		email.WriteString("Content-Transfer-Encoding: 8bit\n")
		email.WriteString("\n")
		email.WriteString(htmlBody)
		email.WriteString("\n\n")

		email.WriteString(fmt.Sprintf("--%s--\n", altBoundary))
	}

	e.logger.Debug("Email message built (%d bytes)", email.Len())
	return email.String(), toHeader
}

func (e *EmailNotifier) sendViaPMF(ctx context.Context, recipient, subject, htmlBody, textBody string, data *NotificationData) (backend, backendPath string, err error) {
	e.logger.Debug("sendViaPMF() starting")

	var pmfPath string
	for _, candidate := range pmfLookPathCandidates {
		if path, err := exec.LookPath(candidate); err == nil {
			pmfPath = path
			break
		}
	}
	if pmfPath == "" {
		return "", "", fmt.Errorf("proxmox-mail-forward not found - please install/configure Proxmox Notifications or use EMAIL_DELIVERY_METHOD=sendmail or relay")
	}
	e.logger.Debug("✓ Proxmox mail forwarder found at %s", pmfPath)

	emailMessage, toHeader := e.buildEmailMessage(recipient, subject, htmlBody, textBody, data)

	e.logger.Debug("=== Sending email via proxmox-mail-forward ===")
	e.logger.Debug("proxmox-mail-forward routing is handled by Proxmox Notifications; To=%q is only a mail header", toHeader)

	cmd := exec.CommandContext(ctx, pmfPath)
	cmd.Stdin = strings.NewReader(emailMessage)

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	startTime := time.Now()
	err = cmd.Run()
	duration := time.Since(startTime)

	e.logger.Debug("proxmox-mail-forward completed in %v", duration)

	if stdoutBuf.Len() > 0 {
		stdoutStr := strings.TrimSpace(stdoutBuf.String())
		if stdoutStr != "" {
			e.logger.Debug("proxmox-mail-forward stdout: %s", stdoutStr)
		}
	}
	if stderrBuf.Len() > 0 {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if stderrStr != "" {
			e.logger.Debug("proxmox-mail-forward stderr: %s", stderrStr)
		}
	}

	if err != nil {
		e.logger.Error("❌ proxmox-mail-forward failed: %v", err)
		return "proxmox-mail-forward", pmfPath, fmt.Errorf("proxmox-mail-forward failed: %w (stderr: %s)", err, strings.TrimSpace(stderrBuf.String()))
	}

	e.logger.Debug("✅ Email handed off to proxmox-mail-forward successfully")
	return "proxmox-mail-forward", pmfPath, nil
}

// sendViaSendmail sends email via local sendmail command
func (e *EmailNotifier) sendViaSendmail(ctx context.Context, recipient, subject, htmlBody, textBody string, data *NotificationData) (queueID, backend, backendPath string, err error) {
	e.logger.Debug("sendViaSendmail() starting for recipient: %s", recipient)

	// ========================================================================
	// PRE-FLIGHT MTA DIAGNOSTIC CHECKS
	// ========================================================================
	e.logger.Debug("=== Pre-flight MTA diagnostic checks ===")

	// Track initial mail queue size for post-send comparison
	initialQueueCount := -1

	// Check if sendmail exists
	sendmailPath := sendmailBinaryPath
	if _, err := exec.LookPath(sendmailPath); err != nil {
		return "", "", "", fmt.Errorf("sendmail not found at %s - please install/configure a local MTA (e.g. postfix) or use EMAIL_DELIVERY_METHOD=relay/pmf", sendmailPath)
	}
	e.logger.Debug("✓ Sendmail binary found at %s", sendmailPath)

	// Check MTA service status
	if active, service := e.isMTAServiceActive(ctx); active {
		e.logger.Debug("✓ MTA service '%s' is active", service)
	} else {
		e.logger.Warning("⚠ No MTA service appears to be running (checked: postfix, sendmail, exim4)")
		e.logger.Warning("  Emails may be accepted but not delivered. Consider using EMAIL_DELIVERY_METHOD=relay")
	}

	// Check MTA configuration
	if hasConfig, mtaType := e.checkMTAConfiguration(); hasConfig {
		e.logger.Debug("✓ %s configuration found", mtaType)

		// For Postfix, check relay configuration
		if mtaType == "Postfix" {
			if hasRelay, relayHost := e.checkRelayHostConfigured(ctx); hasRelay {
				e.logger.Debug("✓ SMTP relay configured: %s", relayHost)
			} else {
				e.logger.Debug("ℹ No relay host configured (using direct delivery)")
			}
		}
	} else {
		e.logger.Warning("⚠ No MTA configuration file found")
		e.logger.Warning("  Sendmail may queue emails but not deliver them")
	}

	// Check current mail queue
	if queueCount, err := e.checkMailQueue(ctx); err == nil {
		initialQueueCount = queueCount
		if queueCount > 0 {
			e.logger.Warning("⚠ %d message(s) currently in mail queue (previous emails may be stuck)", queueCount)
			if queueCount > 10 {
				e.logger.Warning("  Large queue detected - check mail server configuration with 'mailq' and /var/log/mail.log")
			}
		} else {
			e.logger.Debug("Mail queue initially empty before sending email")
		}
	} else {
		e.logger.Debug("Could not inspect mail queue before sending: %v", err)
	}

	emailMessage, _ := e.buildEmailMessage(recipient, subject, htmlBody, textBody, data)

	// ========================================================================
	// SEND EMAIL WITH VERBOSE OUTPUT
	// ========================================================================
	e.logger.Debug("=== Sending email via sendmail ===")

	// Build sendmail arguments
	args := []string{"-t", "-oi"}

	// Add verbose flag only when logger runs at debug level
	if e.logger.GetLevel() >= types.LogLevelDebug {
		args = append(args, "-v")
		e.logger.Debug("Verbose mode enabled (-v flag)")
	}

	// Create sendmail command
	cmd := exec.CommandContext(ctx, sendmailPath, args...)
	cmd.Stdin = strings.NewReader(emailMessage)

	// Capture stdout and stderr separately
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Execute
	startTime := time.Now()
	err = cmd.Run()
	duration := time.Since(startTime)

	e.logger.Debug("Sendmail command completed in %v", duration)

	// Log stdout if available
	if stdoutBuf.Len() > 0 {
		stdoutStr := strings.TrimSpace(stdoutBuf.String())
		if stdoutStr != "" {
			e.logger.Debug("Sendmail stdout: %s", stdoutStr)
			highlights, _, derivedQueueID := summarizeSendmailTranscript(stdoutStr)
			if len(highlights) > 0 && e.logger.GetLevel() <= types.LogLevelDebug {
				for _, msg := range highlights {
					e.logger.Debug("SMTP summary: %s", msg)
				}
			}
			if queueID == "" && derivedQueueID != "" {
				queueID = derivedQueueID
				e.logger.Debug("Detected queue ID from SMTP transcript: %s", queueID)
			}
		}
	}

	// Log stderr (check for warnings)
	if stderrBuf.Len() > 0 {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if strings.Contains(strings.ToLower(stderrStr), "warning") {
			e.logger.Warning("Sendmail warning: %s", stderrStr)
		} else {
			e.logger.Debug("Sendmail stderr: %s", stderrStr)
		}
	}

	if id := extractQueueID(stdoutBuf.String(), stderrBuf.String()); id != "" {
		queueID = id
		e.logger.Debug("Detected mail queue ID from sendmail output: %s", queueID)
	}

	if err != nil {
		e.logger.Error("❌ Sendmail command failed: %v", err)
		return "", "sendmail", sendmailPath, fmt.Errorf("sendmail failed: %w (stderr: %s)", err, stderrBuf.String())
	}

	// ========================================================================
	// POST-SEND VERIFICATION
	// ========================================================================
	e.logger.Debug("=== Post-send verification ===")

	// Brief pause to let sendmail process the message
	time.Sleep(500 * time.Millisecond)

	// Check queue again to see if message is stuck
	if queueCount, err := e.checkMailQueue(ctx); err == nil {
		if queueCount > 0 {
			// If queue grew after sending, highlight this explicitly
			if initialQueueCount >= 0 && queueCount > initialQueueCount {
				e.logger.Warning("⚠ Mail queue size increased from %d to %d after sending email (messages may be queued or delayed)", initialQueueCount, queueCount)
				e.logger.Info("  Suggestion: run 'mailq' and inspect /var/log/mail.log to verify delivery and diagnose issues")
			} else {
				e.logger.Debug("ℹ Mail queue size: %d (message may be queued for delivery)", queueCount)
			}
		} else {
			e.logger.Debug("✓ Mail queue is empty (message likely processed)")
		}
	} else {
		e.logger.Debug("Could not inspect mail queue after sending: %v", err)
	}

	// Check recent mail logs for errors (always surface summary, details only in debug)
	recentErrors := e.checkRecentMailLogs()
	if len(recentErrors) > 0 {
		e.logger.Warning("⚠ Recent mail log entries indicate potential delivery issues (found %d error-like lines)", len(recentErrors))
		e.logger.Info("  Suggestion: inspect /var/log/mail.log (or maillog/mail.err) on this host for details")

		if e.logger.GetLevel() <= types.LogLevelDebug {
			if len(recentErrors) <= 5 {
				e.logger.Debug("Recent mail log entries (%d found):", len(recentErrors))
				for _, errLine := range recentErrors {
					if len(errLine) > 200 {
						errLine = errLine[:200] + "..."
					}
					e.logger.Debug("  %s", errLine)
				}
			} else {
				e.logger.Debug("Recent mail log entries (%d found, showing first 5):", len(recentErrors))
				for i := 0; i < 5; i++ {
					errLine := recentErrors[i]
					if len(errLine) > 200 {
						errLine = errLine[:200] + "..."
					}
					e.logger.Debug("  %s", errLine)
				}
			}
		}
	}

	if queueID != "" {
		status, matchedLine, logPath := e.inspectMailLogStatus(queueID)
		e.logMailLogStatus(queueID, status, matchedLine, logPath)
	} else {
		e.logger.Debug("Sendmail did not report a queue ID; attempting to detect from mail queue output")
		if detectedID, queueLine, err := e.detectQueueEntry(ctx, recipient); err == nil {
			if detectedID != "" {
				queueID = detectedID
				e.logger.Info("Detected queue ID %s for %s by inspecting mail queue output", queueID, recipient)
				if queueLine != "" && e.logger.GetLevel() <= types.LogLevelDebug {
					e.logger.Debug("Mail queue entry: %s", queueLine)
				}
				status, matchedLine, logPath := e.inspectMailLogStatus(queueID)
				e.logMailLogStatus(queueID, status, matchedLine, logPath)
			} else {
				e.logger.Debug("No matching mail queue entry found for %s immediately after sending", recipient)
			}
		} else {
			e.logger.Debug("Unable to inspect mail queue entries for %s: %v", recipient, err)
		}
	}

	e.logger.Debug("✅ Email handed off to sendmail successfully")
	e.logger.Info("NOTE: Sendmail exit code 0 means email accepted to queue, not necessarily delivered")
	e.logger.Info("  To verify actual delivery, check: mailq and /var/log/mail.log")

	return queueID, "sendmail", sendmailPath, nil
}
