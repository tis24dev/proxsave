package support

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

type Meta struct {
	GitHubUser string
	IssueID    string
}

var newEmailNotifier = func(config notify.EmailConfig, proxmoxType types.ProxmoxType, logger *logging.Logger) (notify.Notifier, error) {
	return notify.NewEmailNotifier(config, proxmoxType, logger)
}

// RunIntro prompts for consent and GitHub metadata.
// ok=false means the user declined or aborted; interrupted=true means context cancel / Ctrl+C.
func RunIntro(ctx context.Context, bootstrap *logging.BootstrapLogger) (meta Meta, ok bool, interrupted bool) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("\033[32m================================================\033[0m")
	fmt.Println("\033[32m  SUPPORT & ASSISTANCE MODE\033[0m")
	fmt.Println("\033[32m================================================\033[0m")
	fmt.Println()
	fmt.Println("This mode will send the ProxSave log to the developer for debugging.")
	fmt.Println("\033[33mIf your log contains personal or sensitive information, it will be shared.\033[0m")
	fmt.Println()

	accepted, err := promptYesNoSupport(ctx, reader, "Do you accept and continue? [y/N]: ")
	if err != nil {
		if errors.Is(err, input.ErrInputAborted) || ctx.Err() == context.Canceled {
			bootstrap.Warning("Support mode interrupted by signal")
			return Meta{}, false, true
		}
		bootstrap.Error("ERROR: %v", err)
		return Meta{}, false, false
	}
	if !accepted {
		bootstrap.Warning("Support mode aborted by user (consent not granted)")
		return Meta{}, false, false
	}

	fmt.Println()
	fmt.Println("Before proceeding, you must have an open GitHub issue for this problem.")
	fmt.Println("Emails without a corresponding GitHub issue will not be analyzed.")
	fmt.Println()

	hasIssue, err := promptYesNoSupport(ctx, reader, "Do you confirm that you have already opened a GitHub issue? [y/N]: ")
	if err != nil {
		if errors.Is(err, input.ErrInputAborted) || ctx.Err() == context.Canceled {
			bootstrap.Warning("Support mode interrupted by signal")
			return Meta{}, false, true
		}
		bootstrap.Error("ERROR: %v", err)
		return Meta{}, false, false
	}
	if !hasIssue {
		bootstrap.Warning("Support mode aborted: please open a GitHub issue first")
		return Meta{}, false, false
	}

	// GitHub nickname
	for {
		fmt.Print("Enter your GitHub nickname: ")
		line, err := input.ReadLineWithContext(ctx, reader)
		if err != nil {
			if errors.Is(err, input.ErrInputAborted) || ctx.Err() == context.Canceled {
				bootstrap.Warning("Support mode interrupted by signal")
				return Meta{}, false, true
			}
			bootstrap.Error("ERROR: Failed to read input: %v", err)
			return Meta{}, false, false
		}
		nickname := strings.TrimSpace(line)
		if nickname == "" {
			fmt.Println("GitHub nickname cannot be empty. Please try again.")
			continue
		}
		meta.GitHubUser = nickname
		break
	}

	// GitHub issue number
	for {
		fmt.Print("Enter the GitHub issue number in the format #1234: ")
		line, err := input.ReadLineWithContext(ctx, reader)
		if err != nil {
			if errors.Is(err, input.ErrInputAborted) || ctx.Err() == context.Canceled {
				bootstrap.Warning("Support mode interrupted by signal")
				return Meta{}, false, true
			}
			bootstrap.Error("ERROR: Failed to read input: %v", err)
			return Meta{}, false, false
		}
		issue := strings.TrimSpace(line)
		if issue == "" {
			fmt.Println("Issue number cannot be empty. Please try again.")
			continue
		}
		if !strings.HasPrefix(issue, "#") || len(issue) < 2 {
			fmt.Println("Issue must start with '#' and contain a numeric ID, for example: #1234.")
			continue
		}
		if _, err := strconv.Atoi(issue[1:]); err != nil {
			fmt.Println("Issue must be in the format #1234 with a numeric ID. Please try again.")
			continue
		}
		meta.IssueID = issue
		break
	}

	fmt.Println()
	fmt.Println("Support mode confirmed.")
	fmt.Println("The run will execute in DEBUG mode and a support email with the full log will be sent to github-support@tis24.it at the end.")
	fmt.Println()

	return meta, true, false
}

func promptYesNoSupport(ctx context.Context, reader *bufio.Reader, prompt string) (bool, error) {
	for {
		fmt.Print(prompt)
		line, err := input.ReadLineWithContext(ctx, reader)
		if err != nil {
			return false, err
		}
		answer := strings.TrimSpace(strings.ToLower(line))
		if answer == "y" || answer == "yes" {
			return true, nil
		}
		if answer == "" || answer == "n" || answer == "no" {
			return false, nil
		}
		fmt.Println("Please answer with 'y' or 'n'.")
	}
}

// BuildSupportStats builds a minimal BackupStats suitable for support email/log attachment.
func BuildSupportStats(logger *logging.Logger, hostname string, proxmoxType types.ProxmoxType, proxmoxVersion, toolVersion string, startTime, endTime time.Time, exitCode int, mode string) *orchestrator.BackupStats {
	if logger == nil {
		return nil
	}
	logPath := logger.GetLogFilePath()
	stats := &orchestrator.BackupStats{
		Hostname:       hostname,
		ProxmoxType:    proxmoxType,
		ProxmoxVersion: proxmoxVersion,
		Version:        toolVersion,
		ScriptVersion:  toolVersion,
		Timestamp:      startTime,
		StartTime:      startTime,
		EndTime:        endTime,
		Duration:       endTime.Sub(startTime),
		LogFilePath:    logPath,
		ExitCode:       exitCode,
		LocalStatus:    "ok",
	}
	if exitCode != 0 {
		stats.LocalStatus = "error"
	}
	if strings.TrimSpace(mode) != "" {
		stats.LocalStatusSummary = fmt.Sprintf("Support wrapper mode=%s", strings.TrimSpace(mode))
	} else {
		stats.LocalStatusSummary = "Support wrapper"
	}
	return stats
}

func SendEmail(ctx context.Context, cfg *config.Config, logger *logging.Logger, proxmoxType types.ProxmoxType, stats *orchestrator.BackupStats, meta Meta, buildSignature string) {
	if stats == nil {
		logging.Warning("Support mode: cannot send support email because stats are nil")
		return
	}

	subject := "SUPPORT REQUEST"
	if strings.TrimSpace(meta.GitHubUser) != "" || strings.TrimSpace(meta.IssueID) != "" {
		subjectParts := []string{"SUPPORT REQUEST"}
		if strings.TrimSpace(meta.GitHubUser) != "" {
			subjectParts = append(subjectParts, fmt.Sprintf("Nickname: %s", strings.TrimSpace(meta.GitHubUser)))
		}
		if strings.TrimSpace(meta.IssueID) != "" {
			subjectParts = append(subjectParts, fmt.Sprintf("Issue: %s", strings.TrimSpace(meta.IssueID)))
		}
		subject = strings.Join(subjectParts, " - ")
	}

	if sig := strings.TrimSpace(buildSignature); sig != "" {
		subject = fmt.Sprintf("%s - Build: %s", subject, sig)
	}

	from := ""
	if cfg != nil {
		from = cfg.EmailFrom
	}

	emailConfig := notify.EmailConfig{
		Enabled:          true,
		DeliveryMethod:   notify.EmailDeliverySendmail,
		FallbackSendmail: false,
		AttachLogFile:    true,
		Recipient:        "github-support@tis24.it",
		From:             from,
		SubjectOverride:  subject,
	}

	emailNotifier, err := newEmailNotifier(emailConfig, proxmoxType, logger)
	if err != nil {
		logging.Warning("Support mode: failed to initialize support email notifier: %v", err)
		return
	}

	adapter := orchestrator.NewNotificationAdapter(emailNotifier, logger)
	if err := adapter.Notify(ctx, stats); err != nil {
		logging.Critical("Support mode: FAILED to send support email: %v", err)
		fmt.Println("\033[33m⚠️  CRITICAL: Support email failed to send!\033[0m")
		return
	}

	logging.Info("Support mode: support email handed off to local MTA for github-support@tis24.it (check mailq and /var/log/mail.log for delivery)")
}
