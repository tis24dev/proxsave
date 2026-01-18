package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

type networkPreflightResult struct {
	Tool        string
	Args        []string
	Output      string
	Skipped     bool
	SkipReason  string
	ExitError   error
	CheckedAt   time.Time
	CommandHint string
}

func (r networkPreflightResult) CommandLine() string {
	if strings.TrimSpace(r.Tool) == "" {
		return ""
	}
	if len(r.Args) == 0 {
		return r.Tool
	}
	return r.Tool + " " + strings.Join(r.Args, " ")
}

func (r networkPreflightResult) Ok() bool {
	return !r.Skipped && r.ExitError == nil
}

func (r networkPreflightResult) Summary() string {
	if r.Skipped {
		return fmt.Sprintf("Network preflight: SKIPPED (%s)", strings.TrimSpace(r.SkipReason))
	}
	if r.ExitError == nil {
		return fmt.Sprintf("Network preflight: OK (%s)", r.CommandLine())
	}
	return fmt.Sprintf("Network preflight: FAILED (%s)", r.CommandLine())
}

func (r networkPreflightResult) Details() string {
	var b strings.Builder
	if !r.CheckedAt.IsZero() {
		b.WriteString("GeneratedAt: " + r.CheckedAt.Format(time.RFC3339) + "\n")
	}
	b.WriteString(r.Summary())
	if hint := strings.TrimSpace(r.CommandHint); hint != "" {
		b.WriteString("\nHint: " + hint)
	}
	if r.Skipped {
		return b.String()
	}
	if out := strings.TrimSpace(r.Output); out != "" {
		b.WriteString("\n\n")
		b.WriteString(out)
	}
	if r.ExitError != nil {
		b.WriteString("\n\nExit error: " + r.ExitError.Error())
	}
	return b.String()
}

func runNetworkPreflightValidation(ctx context.Context, timeout time.Duration, logger *logging.Logger) networkPreflightResult {
	return runNetworkPreflightValidationWithDeps(ctx, timeout, logger, commandAvailable, restoreCmd.Run)
}

func runNetworkPreflightValidationWithDeps(
	ctx context.Context,
	timeout time.Duration,
	logger *logging.Logger,
	available func(string) bool,
	run func(context.Context, string, ...string) ([]byte, error),
) (result networkPreflightResult) {
	done := logging.DebugStart(logger, "network preflight", "timeout=%s", timeout)
	defer func() {
		switch {
		case result.Ok():
			done(nil)
		case result.ExitError != nil:
			done(result.ExitError)
		case result.Skipped && strings.TrimSpace(result.SkipReason) != "":
			done(fmt.Errorf("skipped: %s", strings.TrimSpace(result.SkipReason)))
		default:
			done(errors.New("preflight validation failed"))
		}
	}()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if available == nil || run == nil {
		logging.DebugStep(logger, "network preflight", "Skipped: validator dependencies not available")
		result = networkPreflightResult{
			Skipped:    true,
			SkipReason: "validator dependencies not available",
			CheckedAt:  nowRestore(),
		}
		return result
	}

	type candidate struct {
		Tool              string
		Args              []string
		UnsupportedOption string
	}

	candidates := []candidate{
		{Tool: "ifquery", Args: []string{"--check", "-a"}, UnsupportedOption: "--check"},
		{Tool: "ifreload", Args: []string{"--check", "-a"}, UnsupportedOption: "--check"},
		{Tool: "ifup", Args: []string{"--no-act", "-a"}, UnsupportedOption: "--no-act"},
		{Tool: "ifup", Args: []string{"-n", "-a"}, UnsupportedOption: "-n"},
	}
	logging.DebugStep(logger, "network preflight", "Validator order: ifquery --check -a -> ifreload --check -a -> ifup --no-act -a -> ifup -n -a")

	var foundAny bool
	now := nowRestore()

	for _, cand := range candidates {
		if strings.TrimSpace(cand.Tool) == "" {
			continue
		}
		if !available(cand.Tool) {
			logging.DebugStep(logger, "network preflight", "Skip %s: not available", cand.Tool)
			continue
		}
		foundAny = true

		logging.DebugStep(logger, "network preflight", "Run %s", cand.Tool+" "+strings.Join(cand.Args, " "))
		ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
		output, err := run(ctxTimeout, cand.Tool, cand.Args...)
		cancel()

		outText := string(output)
		if err == nil {
			logging.DebugStep(logger, "network preflight", "OK: %s", cand.Tool)
			result = networkPreflightResult{
				Tool:      cand.Tool,
				Args:      cand.Args,
				Output:    strings.TrimSpace(outText),
				CheckedAt: now,
			}
			return result
		}

		if cand.UnsupportedOption != "" && looksLikeUnsupportedOption(outText, cand.UnsupportedOption) {
			logging.DebugStep(logger, "network preflight", "Unsupported flag detected (%s) for %s; trying next validator", cand.UnsupportedOption, cand.Tool)
			continue
		}

		logging.DebugStep(logger, "network preflight", "FAILED: %s (error=%v)", cand.Tool, err)
		result = networkPreflightResult{
			Tool:      cand.Tool,
			Args:      cand.Args,
			Output:    strings.TrimSpace(outText),
			ExitError: err,
			CheckedAt: now,
		}
		return result
	}

	if !foundAny {
		logging.DebugStep(logger, "network preflight", "Skipped: no validator binary available")
		result = networkPreflightResult{
			Skipped:    true,
			SkipReason: "no validator binary available (ifquery/ifreload/ifup)",
			CheckedAt:  now,
		}
		return result
	}

	logging.DebugStep(logger, "network preflight", "Skipped: no compatible validator found (unsupported flags)")
	result = networkPreflightResult{
		Skipped:     true,
		SkipReason:  "no compatible validator found (unsupported flags)",
		CheckedAt:   now,
		CommandHint: "Install ifupdown2 (ifquery/ifreload) or ifupdown tools to enable validation.",
		ExitError:   errors.New("no compatible validator"),
	}
	return result
}

func looksLikeUnsupportedOption(output, option string) bool {
	low := strings.ToLower(output)
	opt := strings.ToLower(strings.TrimSpace(option))
	if opt == "" {
		return false
	}
	if !strings.Contains(low, opt) {
		return false
	}
	indicators := []string{
		"unrecognized option",
		"unknown option",
		"illegal option",
		"invalid option",
		"bad option",
	}
	for _, ind := range indicators {
		if strings.Contains(low, ind) {
			return true
		}
	}
	return false
}
