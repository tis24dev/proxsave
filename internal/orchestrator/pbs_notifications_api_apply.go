package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func applyPBSNotificationsViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	cfgRaw, cfgPresent, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/notifications.cfg")
	if err != nil {
		return err
	}
	if !cfgPresent {
		return nil
	}
	privRaw, _, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/notifications-priv.cfg")
	if err != nil {
		return err
	}

	cfgSections, err := parseProxmoxNotificationSections(cfgRaw)
	if err != nil {
		return fmt.Errorf("parse staged notifications.cfg: %w", err)
	}
	privSections, err := parseProxmoxNotificationSections(privRaw)
	if err != nil {
		return fmt.Errorf("parse staged notifications-priv.cfg: %w", err)
	}

	privByKey := make(map[string][]proxmoxNotificationEntry)
	privRedactFlagsByKey := make(map[string][]string)
	for _, s := range privSections {
		if strings.TrimSpace(s.Type) == "" || strings.TrimSpace(s.Name) == "" {
			continue
		}
		key := fmt.Sprintf("%s:%s", strings.TrimSpace(s.Type), strings.TrimSpace(s.Name))
		privByKey[key] = append([]proxmoxNotificationEntry{}, s.Entries...)
		privRedactFlagsByKey[key] = append([]string(nil), notificationRedactFlagsFromEntries(s.Entries)...)
	}

	type endpointSection struct {
		section      proxmoxNotificationSection
		redactFlags  []string
		redactIndex  []int
		positional   []string
		sectionKey   string
		endpointType string
	}

	var endpoints []endpointSection
	var matchers []proxmoxNotificationSection

	for _, s := range cfgSections {
		typ := strings.TrimSpace(s.Type)
		name := strings.TrimSpace(s.Name)
		if typ == "" || name == "" {
			continue
		}
		switch typ {
		case "smtp", "sendmail", "gotify", "webhook":
			key := fmt.Sprintf("%s:%s", typ, name)
			if priv, ok := privByKey[key]; ok && len(priv) > 0 {
				s.Entries = append(s.Entries, priv...)
			}
			redactFlags := notificationRedactFlags(s)
			if extra := privRedactFlagsByKey[key]; len(extra) > 0 {
				redactFlags = append(redactFlags, extra...)
			}

			pos := []string{}
			entries := s.Entries

			switch typ {
			case "smtp":
				recipients, remaining, ok := popEntryValue(entries, "recipients", "mailto", "mail-to")
				if !ok || strings.TrimSpace(recipients) == "" {
					logger.Warning("PBS notifications API apply: smtp endpoint %s missing recipients; skipping", name)
					continue
				}
				pos = append(pos, recipients)
				s.Entries = remaining
			case "sendmail":
				mailto, remaining, ok := popEntryValue(entries, "mailto", "mail-to", "recipients")
				if !ok || strings.TrimSpace(mailto) == "" {
					logger.Warning("PBS notifications API apply: sendmail endpoint %s missing mailto; skipping", name)
					continue
				}
				pos = append(pos, mailto)
				s.Entries = remaining
			case "gotify":
				server, remaining, ok := popEntryValue(entries, "server")
				if !ok || strings.TrimSpace(server) == "" {
					logger.Warning("PBS notifications API apply: gotify endpoint %s missing server; skipping", name)
					continue
				}
				token, remaining2, ok := popEntryValue(remaining, "token")
				if !ok || strings.TrimSpace(token) == "" {
					logger.Warning("PBS notifications API apply: gotify endpoint %s missing token; skipping", name)
					continue
				}
				pos = append(pos, server, token)
				s.Entries = remaining2
			case "webhook":
				url, remaining, ok := popEntryValue(entries, "url")
				if !ok || strings.TrimSpace(url) == "" {
					logger.Warning("PBS notifications API apply: webhook endpoint %s missing url; skipping", name)
					continue
				}
				pos = append(pos, url)
				s.Entries = remaining
			}

			redactIndex := []int(nil)
			if typ == "gotify" {
				// proxmox-backup-manager notification endpoint gotify create/update <name> <server> <token>
				redactIndex = []int{6}
			}

			endpoints = append(endpoints, endpointSection{
				section:      s,
				redactFlags:  redactFlags,
				redactIndex:  redactIndex,
				positional:   pos,
				sectionKey:   key,
				endpointType: typ,
			})
		case "matcher":
			matchers = append(matchers, s)
		default:
			logger.Warning("PBS notifications API apply: unknown section %q (%s); skipping", typ, name)
		}
	}

	// Endpoints first (matchers refer to targets/endpoints).
	for _, typ := range []string{"smtp", "sendmail", "gotify", "webhook"} {
		desiredNames := make(map[string]endpointSection)
		for _, e := range endpoints {
			if e.endpointType != typ {
				continue
			}
			name := strings.TrimSpace(e.section.Name)
			if name == "" {
				continue
			}
			desiredNames[name] = e
		}

		names := make([]string, 0, len(desiredNames))
		for name := range desiredNames {
			names = append(names, name)
		}
		sort.Strings(names)

		if strict {
			out, err := runPBSManager(ctx, "notification", "endpoint", typ, "list", "--output-format=json")
			if err != nil {
				return err
			}
			current, err := parsePBSListIDs(out, "name", "id")
			if err != nil {
				return fmt.Errorf("parse endpoint list (%s): %w", typ, err)
			}
			for _, name := range current {
				if _, ok := desiredNames[name]; ok {
					continue
				}
				if _, err := runPBSManager(ctx, "notification", "endpoint", typ, "remove", name); err != nil {
					// Built-in endpoints may not be removable; keep going.
					logger.Warning("PBS notifications API apply: endpoint remove %s:%s failed (continuing): %v", typ, name, err)
				}
			}
		}

		for _, name := range names {
			e := desiredNames[name]
			flags := buildProxmoxManagerFlags(e.section.Entries)
			createArgs := append([]string{"notification", "endpoint", typ, "create", name}, e.positional...)
			createArgs = append(createArgs, flags...)
			if _, err := runPBSManagerRedacted(ctx, createArgs, e.redactFlags, e.redactIndex); err != nil {
				updateArgs := append([]string{"notification", "endpoint", typ, "update", name}, e.positional...)
				updateArgs = append(updateArgs, flags...)
				if _, upErr := runPBSManagerRedacted(ctx, updateArgs, e.redactFlags, e.redactIndex); upErr != nil {
					return fmt.Errorf("endpoint %s:%s: %v (create) / %v (update)", typ, name, err, upErr)
				}
			}
		}
	}

	// Then matchers.
	desiredMatchers := make(map[string]proxmoxNotificationSection, len(matchers))
	for _, m := range matchers {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		desiredMatchers[name] = m
	}

	matcherNames := make([]string, 0, len(desiredMatchers))
	for name := range desiredMatchers {
		matcherNames = append(matcherNames, name)
	}
	sort.Strings(matcherNames)

	if strict {
		out, err := runPBSManager(ctx, "notification", "matcher", "list", "--output-format=json")
		if err != nil {
			return err
		}
		current, err := parsePBSListIDs(out, "name", "id")
		if err != nil {
			return fmt.Errorf("parse matcher list: %w", err)
		}
		for _, name := range current {
			if _, ok := desiredMatchers[name]; ok {
				continue
			}
			if _, err := runPBSManager(ctx, "notification", "matcher", "remove", name); err != nil {
				// Built-in matchers may not be removable; keep going.
				logger.Warning("PBS notifications API apply: matcher remove %s failed (continuing): %v", name, err)
			}
		}
	}

	for _, name := range matcherNames {
		m := desiredMatchers[name]
		flags := buildProxmoxManagerFlags(m.Entries)
		createArgs := append([]string{"notification", "matcher", "create", name}, flags...)
		if _, err := runPBSManager(ctx, createArgs...); err != nil {
			updateArgs := append([]string{"notification", "matcher", "update", name}, flags...)
			if _, upErr := runPBSManager(ctx, updateArgs...); upErr != nil {
				return fmt.Errorf("matcher %s: %v (create) / %v (update)", name, err, upErr)
			}
		}
	}

	return nil
}
