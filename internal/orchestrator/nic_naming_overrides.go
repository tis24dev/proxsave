package orchestrator

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

type nicNamingOverrideRuleKind string

const (
	nicNamingOverrideUdev        nicNamingOverrideRuleKind = "udev"
	nicNamingOverrideSystemdLink nicNamingOverrideRuleKind = "systemd-link"
)

type nicNamingOverrideRule struct {
	Kind   nicNamingOverrideRuleKind
	Source string
	Line   int
	Name   string
	MAC    string
}

type nicNamingOverrideReport struct {
	Rules []nicNamingOverrideRule
}

func (r nicNamingOverrideReport) Empty() bool {
	return len(r.Rules) == 0
}

func (r nicNamingOverrideReport) Summary() string {
	if len(r.Rules) == 0 {
		return "NIC naming overrides: none"
	}
	udevCount := 0
	linkCount := 0
	for _, rule := range r.Rules {
		switch rule.Kind {
		case nicNamingOverrideUdev:
			udevCount++
		case nicNamingOverrideSystemdLink:
			linkCount++
		}
	}
	if udevCount > 0 && linkCount > 0 {
		return fmt.Sprintf("NIC naming overrides detected: udev=%d systemd-link=%d", udevCount, linkCount)
	}
	if udevCount > 0 {
		return fmt.Sprintf("NIC naming overrides detected: udev=%d", udevCount)
	}
	return fmt.Sprintf("NIC naming overrides detected: systemd-link=%d", linkCount)
}

func (r nicNamingOverrideReport) Details(maxLines int) string {
	if len(r.Rules) == 0 || maxLines == 0 {
		return ""
	}
	limit := maxLines
	if limit < 0 || limit > len(r.Rules) {
		limit = len(r.Rules)
	}

	lines := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		rule := r.Rules[i]
		meta := ""
		if strings.TrimSpace(rule.MAC) != "" {
			meta = " mac=" + rule.MAC
		}
		ref := rule.Source
		if rule.Line > 0 {
			ref = fmt.Sprintf("%s:%d", ref, rule.Line)
		}
		lines = append(lines, fmt.Sprintf("- %s %s name=%s%s", rule.Kind, ref, rule.Name, meta))
	}
	if len(r.Rules) > limit {
		lines = append(lines, fmt.Sprintf("... and %d more", len(r.Rules)-limit))
	}
	return strings.Join(lines, "\n")
}

func detectNICNamingOverrideRules(logger *logging.Logger) (report nicNamingOverrideReport, err error) {
	done := logging.DebugStart(logger, "NIC naming override detect", "udev_dir=/etc/udev/rules.d systemd_dir=/etc/systemd/network")
	defer func() { done(err) }()

	logging.DebugStep(logger, "NIC naming override detect", "Scan udev persistent net naming rules")
	udevRules, err := scanUdevNetNamingOverrides(logger, "/etc/udev/rules.d")
	if err != nil {
		return report, err
	}
	logging.DebugStep(logger, "NIC naming override detect", "Udev naming override rules found=%d", len(udevRules))
	report.Rules = append(report.Rules, udevRules...)

	logging.DebugStep(logger, "NIC naming override detect", "Scan systemd .link naming rules")
	linkRules, err := scanSystemdLinkNamingOverrides(logger, "/etc/systemd/network")
	if err != nil {
		return report, err
	}
	logging.DebugStep(logger, "NIC naming override detect", "Systemd-link naming override rules found=%d", len(linkRules))
	report.Rules = append(report.Rules, linkRules...)

	logging.DebugStep(logger, "NIC naming override detect", "Total naming override rules detected=%d", len(report.Rules))

	sort.Slice(report.Rules, func(i, j int) bool {
		if report.Rules[i].Kind != report.Rules[j].Kind {
			return report.Rules[i].Kind < report.Rules[j].Kind
		}
		if report.Rules[i].Source != report.Rules[j].Source {
			return report.Rules[i].Source < report.Rules[j].Source
		}
		if report.Rules[i].Line != report.Rules[j].Line {
			return report.Rules[i].Line < report.Rules[j].Line
		}
		return report.Rules[i].Name < report.Rules[j].Name
	})

	return report, nil
}

func scanUdevNetNamingOverrides(logger *logging.Logger, dir string) (rules []nicNamingOverrideRule, err error) {
	done := logging.DebugStart(logger, "scan udev naming overrides", "dir=%s", dir)
	defer func() { done(err) }()

	logging.DebugStep(logger, "scan udev naming overrides", "ReadDir: %s", dir)
	entries, err := restoreFS.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
			logging.DebugStep(logger, "scan udev naming overrides", "Directory not present; skipping (%v)", err)
			return nil, nil
		}
		return nil, err
	}

	logging.DebugStep(logger, "scan udev naming overrides", "Found %d entry(ies)", len(entries))
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		path := filepath.Join(dir, name)
		logging.DebugStep(logger, "scan udev naming overrides", "Inspect file: %s", path)
		data, err := restoreFS.ReadFile(path)
		if err != nil {
			logging.DebugStep(logger, "scan udev naming overrides", "Skip file: read failed: %v", err)
			continue
		}
		found := parseUdevNetNamingOverrides(path, string(data))
		if len(found) > 0 {
			logging.DebugStep(logger, "scan udev naming overrides", "Detected %d naming override rule(s) in %s", len(found), path)
		}
		rules = append(rules, found...)
	}
	return rules, nil
}

func parseUdevNetNamingOverrides(source string, content string) []nicNamingOverrideRule {
	var rules []nicNamingOverrideRule
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, mac := parseUdevNetNamingOverrideLine(line)
		if name == "" {
			continue
		}
		rules = append(rules, nicNamingOverrideRule{
			Kind:   nicNamingOverrideUdev,
			Source: source,
			Line:   lineNo,
			Name:   name,
			MAC:    mac,
		})
	}
	return rules
}

func parseUdevNetNamingOverrideLine(line string) (name, mac string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", ""
	}

	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, `subsystem=="net"`) {
		return "", ""
	}

	parts := strings.Split(trimmed, ",")
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		switch {
		case strings.HasPrefix(p, "NAME:="):
			name = strings.TrimSpace(strings.TrimPrefix(p, "NAME:="))
			name = strings.TrimSpace(strings.Trim(name, `"'`))
		case strings.HasPrefix(p, "NAME="):
			name = strings.TrimSpace(strings.TrimPrefix(p, "NAME="))
			name = strings.TrimSpace(strings.Trim(name, `"'`))
		case strings.HasPrefix(p, "ATTR{address}=="):
			mac = strings.TrimSpace(strings.TrimPrefix(p, "ATTR{address}=="))
			mac = normalizeMAC(strings.TrimSpace(strings.Trim(mac, `"'`)))
		}
	}

	return strings.TrimSpace(name), strings.TrimSpace(mac)
}

func scanSystemdLinkNamingOverrides(logger *logging.Logger, dir string) (rules []nicNamingOverrideRule, err error) {
	done := logging.DebugStart(logger, "scan systemd link naming overrides", "dir=%s", dir)
	defer func() { done(err) }()

	logging.DebugStep(logger, "scan systemd link naming overrides", "ReadDir: %s", dir)
	entries, err := restoreFS.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
			logging.DebugStep(logger, "scan systemd link naming overrides", "Directory not present; skipping (%v)", err)
			return nil, nil
		}
		return nil, err
	}

	logging.DebugStep(logger, "scan systemd link naming overrides", "Found %d entry(ies)", len(entries))
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || !strings.HasSuffix(strings.ToLower(name), ".link") {
			continue
		}
		path := filepath.Join(dir, name)
		logging.DebugStep(logger, "scan systemd link naming overrides", "Inspect file: %s", path)
		data, err := restoreFS.ReadFile(path)
		if err != nil {
			logging.DebugStep(logger, "scan systemd link naming overrides", "Skip file: read failed: %v", err)
			continue
		}
		found := parseSystemdLinkNamingOverrides(path, string(data))
		if len(found) > 0 {
			logging.DebugStep(logger, "scan systemd link naming overrides", "Detected %d naming override rule(s) in %s", len(found), path)
		}
		rules = append(rules, found...)
	}
	return rules, nil
}

func parseSystemdLinkNamingOverrides(source, content string) []nicNamingOverrideRule {
	var macs []string
	linkName := ""
	section := ""

	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch section {
		case "match":
			if key == "macaddress" {
				for _, raw := range strings.Fields(value) {
					normalized := normalizeMAC(raw)
					if normalized != "" {
						macs = append(macs, normalized)
					}
				}
			}
		case "link":
			if key == "name" {
				linkName = strings.TrimSpace(value)
			}
		}
	}

	linkName = strings.TrimSpace(strings.Trim(linkName, `"'`))
	if linkName == "" || len(macs) == 0 {
		return nil
	}

	sort.Strings(macs)
	unique := make([]string, 0, len(macs))
	seen := make(map[string]struct{}, len(macs))
	for _, m := range macs {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		unique = append(unique, m)
	}

	rules := make([]nicNamingOverrideRule, 0, len(unique))
	for _, m := range unique {
		rules = append(rules, nicNamingOverrideRule{
			Kind:   nicNamingOverrideSystemdLink,
			Source: source,
			Line:   0,
			Name:   linkName,
			MAC:    m,
		})
	}
	return rules
}
