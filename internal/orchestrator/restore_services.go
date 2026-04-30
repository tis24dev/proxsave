// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

var (
	serviceStopTimeout        = 45 * time.Second
	serviceStopNoBlockTimeout = 15 * time.Second
	serviceStartTimeout       = 30 * time.Second
	serviceVerifyTimeout      = 30 * time.Second
	serviceStatusCheckTimeout = 5 * time.Second
	servicePollInterval       = 500 * time.Millisecond
	serviceRetryDelay         = 500 * time.Millisecond
)

type restoreCommandResult struct {
	out []byte
	err error
}

type restoreCommandProgress struct {
	enabled  bool
	service  string
	action   string
	deadline time.Time
}

type serviceInactiveWaiter struct {
	ctx             context.Context
	logger          *logging.Logger
	service         string
	timeout         time.Duration
	deadline        time.Time
	progressEnabled bool
	ticker          *time.Ticker
}

func stopPVEClusterServices(ctx context.Context, logger *logging.Logger) error {
	services := []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd"}
	for _, service := range services {
		if err := stopServiceWithRetries(ctx, logger, service); err != nil {
			return fmt.Errorf("failed to stop PVE services (%s): %w", service, err)
		}
	}
	return nil
}

func startPVEClusterServices(ctx context.Context, logger *logging.Logger) error {
	services := []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd"}
	for _, service := range services {
		if err := startServiceWithRetries(ctx, logger, service); err != nil {
			return fmt.Errorf("failed to start PVE services (%s): %w", service, err)
		}
	}
	return nil
}

func stopPBSServices(ctx context.Context, logger *logging.Logger) error {
	if _, err := restoreCmd.Run(ctx, "which", "systemctl"); err != nil {
		return fmt.Errorf("systemctl not available: %w", err)
	}
	services := []string{"proxmox-backup-proxy", "proxmox-backup"}
	var failures []string
	for _, service := range services {
		if err := stopServiceWithRetries(ctx, logger, service); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", service, err))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func startPBSServices(ctx context.Context, logger *logging.Logger) error {
	if _, err := restoreCmd.Run(ctx, "which", "systemctl"); err != nil {
		return fmt.Errorf("systemctl not available: %w", err)
	}
	services := []string{"proxmox-backup", "proxmox-backup-proxy"}
	var failures []string
	for _, service := range services {
		if err := startServiceWithRetries(ctx, logger, service); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", service, err))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func unmountEtcPVE(ctx context.Context, logger *logging.Logger) error {
	output, err := restoreCmd.Run(ctx, "umount", "/etc/pve")
	msg := strings.TrimSpace(string(output))
	if err != nil {
		if strings.Contains(msg, "not mounted") {
			logger.Info("Skipping umount /etc/pve (already unmounted)")
			return nil
		}
		if msg != "" {
			return fmt.Errorf("umount /etc/pve failed: %s", msg)
		}
		return fmt.Errorf("umount /etc/pve failed: %w", err)
	}
	if msg != "" {
		logger.Debug("umount /etc/pve output: %s", msg)
	}
	return nil
}

func runCommandWithTimeout(ctx context.Context, logger *logging.Logger, timeout time.Duration, name string, args ...string) error {
	return execCommand(ctx, logger, timeout, name, args...)
}

func execCommand(ctx context.Context, logger *logging.Logger, timeout time.Duration, name string, args ...string) error {
	execCtx, cancel := commandContextWithTimeout(ctx, timeout)
	defer cancel()
	output, err := restoreCmd.Run(execCtx, name, args...)
	msg := strings.TrimSpace(string(output))
	if err != nil {
		return restoreCommandError(execCtx, timeout, name, args, msg, err)
	}
	logRestoreCommandOutput(logger, name, args, msg)
	return nil
}

func commandContextWithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func restoreCommandError(execCtx context.Context, timeout time.Duration, name string, args []string, msg string, err error) error {
	command := fmt.Sprintf("%s %s", name, strings.Join(args, " "))
	if timeout > 0 && (errors.Is(execCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("%s timed out after %s", command, timeout)
	}
	if msg != "" {
		return fmt.Errorf("%s failed: %s", command, msg)
	}
	return fmt.Errorf("%s failed: %w", command, err)
}

func logRestoreCommandOutput(logger *logging.Logger, name string, args []string, msg string) {
	if msg != "" && logger != nil {
		logger.Debug("%s %s: %s", name, strings.Join(args, " "), msg)
	}
}

func stopServiceWithRetries(ctx context.Context, logger *logging.Logger, service string) error {
	attempts := []struct {
		description string
		args        []string
		timeout     time.Duration
	}{
		{"stop (no-block)", []string{"stop", "--no-block", service}, serviceStopNoBlockTimeout},
		{"stop (blocking)", []string{"stop", service}, serviceStopTimeout},
		{"aggressive stop", []string{"kill", "--signal=SIGTERM", "--kill-who=all", service}, serviceStopTimeout},
		{"force kill", []string{"kill", "--signal=SIGKILL", "--kill-who=all", service}, serviceStopTimeout},
	}

	var lastErr error
	for i, attempt := range attempts {
		if i > 0 {
			if err := sleepWithContext(ctx, serviceRetryDelay); err != nil {
				return err
			}
		}

		if logger != nil {
			logger.Debug("Attempting %s for %s (%d/%d)", attempt.description, service, i+1, len(attempts))
		}

		if err := runCommandWithTimeoutCountdown(ctx, logger, attempt.timeout, service, attempt.description, "systemctl", attempt.args...); err != nil {
			lastErr = err
			continue
		}
		if err := waitForServiceInactive(ctx, logger, service, serviceVerifyTimeout); err != nil {
			lastErr = err
			continue
		}
		resetFailedService(ctx, logger, service)
		return nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("unable to stop %s", service)
	}
	return lastErr
}

func startServiceWithRetries(ctx context.Context, logger *logging.Logger, service string) error {
	attempts := []struct {
		description string
		args        []string
	}{
		{"start", []string{"start", service}},
		{"retry start", []string{"start", service}},
		{"aggressive restart", []string{"restart", service}},
	}

	var lastErr error
	for i, attempt := range attempts {
		if i > 0 {
			if err := sleepWithContext(ctx, serviceRetryDelay); err != nil {
				return err
			}
		}

		if logger != nil {
			logger.Debug("Attempting %s for %s (%d/%d)", attempt.description, service, i+1, len(attempts))
		}

		if err := runCommandWithTimeout(ctx, logger, serviceStartTimeout, "systemctl", attempt.args...); err != nil {
			lastErr = err
			continue
		}
		return nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("unable to start %s", service)
	}
	return lastErr
}

func runCommandWithTimeoutCountdown(ctx context.Context, logger *logging.Logger, timeout time.Duration, service, action, name string, args ...string) error {
	if timeout <= 0 {
		return execCommand(ctx, logger, timeout, name, args...)
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resultCh := startRestoreCommand(execCtx, name, args...)
	progress := newRestoreCommandProgress(service, action, timeout)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case r := <-resultCh:
			progress.clear()
			return finishRestoreCommandResult(execCtx, logger, timeout, name, args, r)
		case <-ticker.C:
			progress.write(time.Until(progress.deadline))
		case <-execCtx.Done():
			return finishRestoreCommandTimeout(logger, name, args, timeout, resultCh, progress)
		}
	}
}

func startRestoreCommand(ctx context.Context, name string, args ...string) <-chan restoreCommandResult {
	resultCh := make(chan restoreCommandResult, 1)
	go func() {
		out, err := restoreCmd.Run(ctx, name, args...)
		resultCh <- restoreCommandResult{out: out, err: err}
	}()
	return resultCh
}

func newRestoreCommandProgress(service, action string, timeout time.Duration) restoreCommandProgress {
	return restoreCommandProgress{
		enabled:  isTerminal(int(os.Stderr.Fd())),
		service:  service,
		action:   action,
		deadline: time.Now().Add(timeout),
	}
}

func (progress restoreCommandProgress) write(left time.Duration) {
	if !progress.enabled {
		return
	}
	seconds := int(left.Round(time.Second).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	fmt.Fprintf(os.Stderr, "\rStopping %s: %s (attempt timeout in %ds)...", progress.service, progress.action, seconds)
}

func (progress restoreCommandProgress) clear() {
	if !progress.enabled {
		return
	}
	fmt.Fprint(os.Stderr, "\r")
	fmt.Fprintln(os.Stderr, strings.Repeat(" ", 80))
	fmt.Fprint(os.Stderr, "\r")
}

func (progress restoreCommandProgress) newline() {
	if progress.enabled {
		fmt.Fprintln(os.Stderr)
	}
}

func finishRestoreCommandResult(execCtx context.Context, logger *logging.Logger, timeout time.Duration, name string, args []string, result restoreCommandResult) error {
	msg := strings.TrimSpace(string(result.out))
	if result.err != nil {
		return restoreCommandError(execCtx, timeout, name, args, msg, result.err)
	}
	logRestoreCommandOutput(logger, name, args, msg)
	return nil
}

func finishRestoreCommandTimeout(logger *logging.Logger, name string, args []string, timeout time.Duration, resultCh <-chan restoreCommandResult, progress restoreCommandProgress) error {
	progress.write(0)
	progress.newline()
	select {
	case result := <-resultCh:
		logRestoreCommandOutput(logger, name, args, strings.TrimSpace(string(result.out)))
	default:
	}
	return fmt.Errorf("%s %s timed out after %s", name, strings.Join(args, " "), timeout)
}

func waitForServiceInactive(ctx context.Context, logger *logging.Logger, service string, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	waiter := newServiceInactiveWaiter(ctx, logger, service, timeout)
	defer waiter.ticker.Stop()
	for {
		remaining := time.Until(waiter.deadline)
		if err := waiter.ensureTimeRemaining(remaining); err != nil {
			return err
		}
		active, err := isServiceActive(ctx, service, minDuration(remaining, serviceStatusCheckTimeout))
		if err != nil {
			return err
		}
		if !active {
			waiter.logStopped()
			return nil
		}
		if err := waiter.sleepOrCancel(remaining); err != nil {
			return err
		}
		waiter.writeProgress(remaining)
	}
}

func newServiceInactiveWaiter(ctx context.Context, logger *logging.Logger, service string, timeout time.Duration) serviceInactiveWaiter {
	return serviceInactiveWaiter{
		ctx:             ctx,
		logger:          logger,
		service:         service,
		timeout:         timeout,
		deadline:        time.Now().Add(timeout),
		progressEnabled: isTerminal(int(os.Stderr.Fd())),
		ticker:          time.NewTicker(1 * time.Second),
	}
}

func (waiter serviceInactiveWaiter) ensureTimeRemaining(remaining time.Duration) error {
	if remaining > 0 {
		return nil
	}
	waiter.writeNewline()
	return fmt.Errorf("%s still active after %s", waiter.service, waiter.timeout)
}

func (waiter serviceInactiveWaiter) logStopped() {
	if waiter.logger != nil {
		waiter.logger.Debug("%s stopped successfully", waiter.service)
	}
}

func (waiter serviceInactiveWaiter) sleepOrCancel(remaining time.Duration) error {
	timer := time.NewTimer(minDuration(remaining, servicePollInterval))
	defer timer.Stop()
	select {
	case <-waiter.ctx.Done():
		waiter.writeNewline()
		return waiter.ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (waiter serviceInactiveWaiter) writeProgress(remaining time.Duration) {
	select {
	case <-waiter.ticker.C:
		if waiter.progressEnabled {
			seconds := int(remaining.Round(time.Second).Seconds())
			if seconds < 0 {
				seconds = 0
			}
			fmt.Fprintf(os.Stderr, "\rWaiting for %s to stop (%ds remaining)...", waiter.service, seconds)
		}
	default:
	}
}

func (waiter serviceInactiveWaiter) writeNewline() {
	if waiter.progressEnabled {
		fmt.Fprintln(os.Stderr)
	}
}

func resetFailedService(ctx context.Context, logger *logging.Logger, service string) {
	resetCtx, cancel := context.WithTimeout(ctx, serviceStatusCheckTimeout)
	defer cancel()

	if _, err := restoreCmd.Run(resetCtx, "systemctl", "reset-failed", service); err != nil {
		if logger != nil {
			logger.Debug("systemctl reset-failed %s ignored: %v", service, err)
		}
	}
}

func isServiceActive(ctx context.Context, service string, timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		timeout = serviceStatusCheckTimeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output, err := restoreCmd.Run(checkCtx, "systemctl", "is-active", service)
	msg := strings.TrimSpace(string(output))
	if err == nil {
		return true, nil
	}
	if errors.Is(checkCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return false, fmt.Errorf("systemctl is-active %s timed out after %s", service, timeout)
	}
	if msg == "" {
		msg = err.Error()
	}
	return parseSystemctlActiveState(service, msg)
}

func parseSystemctlActiveState(service, msg string) (bool, error) {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "deactivating") || strings.Contains(lower, "activating") {
		return true, nil
	}
	if strings.Contains(lower, "inactive") || strings.Contains(lower, "failed") || strings.Contains(lower, "dead") {
		return false, nil
	}
	return false, fmt.Errorf("systemctl is-active %s failed: %s", service, msg)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
