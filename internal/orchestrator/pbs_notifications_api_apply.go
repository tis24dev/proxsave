// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

type pbsNotificationEndpointSection struct {
	section      proxmoxNotificationSection
	redactFlags  []string
	redactIndex  []int
	positional   []string
	sectionKey   string
	endpointType string
}

type pbsNotificationDesiredState struct {
	endpoints    []pbsNotificationEndpointSection
	matchers     map[string]proxmoxNotificationSection
	matcherNames []string
}

// gotifyTokenRedactIndex is the token positional index in
// `notification endpoint gotify create <name> <server> <token> ...`.
const gotifyTokenRedactIndex = 6

func applyPBSNotificationsViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) error {
	desired, present, err := loadPBSNotificationDesiredState(stageRoot, logger)
	if err != nil || !present {
		return err
	}

	if strict {
		if err := removeExtraPBSNotificationMatchers(ctx, logger, desired.matchers); err != nil {
			return err
		}
	}
	if err := syncPBSNotificationEndpoints(ctx, logger, desired.endpoints, strict); err != nil {
		return err
	}
	return syncPBSNotificationMatchers(ctx, desired)
}

func loadPBSNotificationDesiredState(stageRoot string, logger *logging.Logger) (pbsNotificationDesiredState, bool, error) {
	cfgSections, privSections, present, err := readPBSNotificationStageSections(stageRoot)
	if err != nil || !present {
		return pbsNotificationDesiredState{}, present, err
	}

	desired := buildPBSNotificationDesiredState(cfgSections, privSections, logger)
	return desired, true, nil
}

func readPBSNotificationStageSections(stageRoot string) ([]proxmoxNotificationSection, []proxmoxNotificationSection, bool, error) {
	cfgRaw, cfgPresent, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/notifications.cfg")
	if err != nil || !cfgPresent {
		return nil, nil, cfgPresent, err
	}
	privRaw, _, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/notifications-priv.cfg")
	if err != nil {
		return nil, nil, true, err
	}

	cfgSections, err := parseProxmoxNotificationSections(cfgRaw)
	if err != nil {
		return nil, nil, true, fmt.Errorf("parse staged notifications.cfg: %w", err)
	}
	privSections, err := parseProxmoxNotificationSections(privRaw)
	if err != nil {
		return nil, nil, true, fmt.Errorf("parse staged notifications-priv.cfg: %w", err)
	}
	return cfgSections, privSections, true, nil
}

func buildPBSNotificationDesiredState(cfgSections, privSections []proxmoxNotificationSection, logger *logging.Logger) pbsNotificationDesiredState {
	privByKey, privRedactFlagsByKey := pbsNotificationPrivMaps(privSections)
	desired := pbsNotificationDesiredState{matchers: make(map[string]proxmoxNotificationSection)}

	for _, section := range cfgSections {
		typ := strings.TrimSpace(section.Type)
		name := strings.TrimSpace(section.Name)
		if typ == "" || name == "" {
			continue
		}
		switch typ {
		case "smtp", "sendmail", "gotify", "webhook":
			if endpoint, ok := buildPBSNotificationEndpoint(section, privByKey, privRedactFlagsByKey, logger); ok {
				desired.endpoints = append(desired.endpoints, endpoint)
			}
		case "matcher":
			desired.matchers[name] = section
		default:
			logger.Warning("PBS notifications API apply: unknown section %q (%s); skipping", typ, name)
		}
	}

	desired.matcherNames = sortedPBSMatcherNames(desired.matchers)
	return desired
}

func pbsNotificationPrivMaps(sections []proxmoxNotificationSection) (map[string][]proxmoxNotificationEntry, map[string][]string) {
	privByKey := make(map[string][]proxmoxNotificationEntry)
	redactByKey := make(map[string][]string)
	for _, section := range sections {
		typ := strings.TrimSpace(section.Type)
		name := strings.TrimSpace(section.Name)
		if typ == "" || name == "" {
			continue
		}
		key := pbsNotificationSectionKey(typ, name)
		privByKey[key] = append([]proxmoxNotificationEntry{}, section.Entries...)
		redactByKey[key] = append([]string(nil), notificationRedactFlagsFromEntries(section.Entries)...)
	}
	return privByKey, redactByKey
}

func buildPBSNotificationEndpoint(section proxmoxNotificationSection, privByKey map[string][]proxmoxNotificationEntry, privRedactFlagsByKey map[string][]string, logger *logging.Logger) (pbsNotificationEndpointSection, bool) {
	typ := strings.TrimSpace(section.Type)
	name := strings.TrimSpace(section.Name)
	key := pbsNotificationSectionKey(typ, name)

	if priv := privByKey[key]; len(priv) > 0 {
		section.Entries = append(section.Entries, priv...)
	}
	positional, entries, ok := pbsEndpointPositionalArgs(typ, name, section.Entries, logger)
	if !ok {
		return pbsNotificationEndpointSection{}, false
	}
	section.Entries = entries

	redactFlags := notificationRedactFlags(section)
	if extra := privRedactFlagsByKey[key]; len(extra) > 0 {
		redactFlags = append(redactFlags, extra...)
	}

	return pbsNotificationEndpointSection{
		section:      section,
		redactFlags:  redactFlags,
		redactIndex:  pbsEndpointRedactIndexes(typ),
		positional:   positional,
		sectionKey:   key,
		endpointType: typ,
	}, true
}

func pbsEndpointPositionalArgs(typ, name string, entries []proxmoxNotificationEntry, logger *logging.Logger) ([]string, []proxmoxNotificationEntry, bool) {
	switch typ {
	case "smtp":
		return pbsEndpointSinglePositional(typ, name, entries, logger, "recipients", "mailto", "mail-to")
	case "sendmail":
		return pbsEndpointSinglePositional(typ, name, entries, logger, "mailto", "mail-to", "recipients")
	case "gotify":
		return pbsGotifyEndpointPositionals(name, entries, logger)
	case "webhook":
		return pbsEndpointSinglePositional(typ, name, entries, logger, "url")
	default:
		return nil, entries, false
	}
}

func pbsEndpointSinglePositional(typ, name string, entries []proxmoxNotificationEntry, logger *logging.Logger, keys ...string) ([]string, []proxmoxNotificationEntry, bool) {
	value, remaining, ok := popEntryValue(entries, keys...)
	if !ok || strings.TrimSpace(value) == "" {
		logger.Warning("PBS notifications API apply: %s endpoint %s missing %s; skipping", typ, name, keys[0])
		return nil, entries, false
	}
	return []string{value}, remaining, true
}

func pbsGotifyEndpointPositionals(name string, entries []proxmoxNotificationEntry, logger *logging.Logger) ([]string, []proxmoxNotificationEntry, bool) {
	server, remaining, ok := popEntryValue(entries, "server")
	if !ok || strings.TrimSpace(server) == "" {
		logger.Warning("PBS notifications API apply: gotify endpoint %s missing server; skipping", name)
		return nil, entries, false
	}
	token, remaining, ok := popEntryValue(remaining, "token")
	if !ok || strings.TrimSpace(token) == "" {
		logger.Warning("PBS notifications API apply: gotify endpoint %s missing token; skipping", name)
		return nil, entries, false
	}
	return []string{server, token}, remaining, true
}

func pbsEndpointRedactIndexes(typ string) []int {
	if typ == "gotify" {
		return []int{gotifyTokenRedactIndex}
	}
	return nil
}

func sortedPBSMatcherNames(matchers map[string]proxmoxNotificationSection) []string {
	names := make([]string, 0, len(matchers))
	for name := range matchers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func removeExtraPBSNotificationMatchers(ctx context.Context, logger *logging.Logger, desired map[string]proxmoxNotificationSection) error {
	current, err := listPBSNotificationIDs(ctx, "matcher", "list")
	if err != nil {
		return err
	}
	for _, name := range current {
		if _, ok := desired[name]; ok {
			continue
		}
		if _, err := runPBSManager(ctx, "notification", "matcher", "remove", name); err != nil {
			logger.Warning("PBS notifications API apply: matcher remove %s failed (continuing): %v", name, err)
		}
	}
	return nil
}

func syncPBSNotificationEndpoints(ctx context.Context, logger *logging.Logger, endpoints []pbsNotificationEndpointSection, strict bool) error {
	for _, typ := range []string{"smtp", "sendmail", "gotify", "webhook"} {
		desired := pbsEndpointsByName(endpoints, typ)
		if strict {
			if err := removeExtraPBSNotificationEndpoints(ctx, logger, typ, desired); err != nil {
				return err
			}
		}
		if err := upsertPBSNotificationEndpoints(ctx, typ, desired); err != nil {
			return err
		}
	}
	return nil
}

func pbsEndpointsByName(endpoints []pbsNotificationEndpointSection, typ string) map[string]pbsNotificationEndpointSection {
	desired := make(map[string]pbsNotificationEndpointSection)
	for _, endpoint := range endpoints {
		if endpoint.endpointType != typ {
			continue
		}
		name := strings.TrimSpace(endpoint.section.Name)
		if name != "" {
			desired[name] = endpoint
		}
	}
	return desired
}

func removeExtraPBSNotificationEndpoints(ctx context.Context, logger *logging.Logger, typ string, desired map[string]pbsNotificationEndpointSection) error {
	current, err := listPBSNotificationIDs(ctx, "endpoint", typ, "list")
	if err != nil {
		return err
	}
	for _, name := range current {
		if _, ok := desired[name]; ok {
			continue
		}
		if _, err := runPBSManager(ctx, "notification", "endpoint", typ, "remove", name); err != nil {
			logger.Warning("PBS notifications API apply: endpoint remove %s:%s failed (continuing): %v", typ, name, err)
		}
	}
	return nil
}

func upsertPBSNotificationEndpoints(ctx context.Context, typ string, desired map[string]pbsNotificationEndpointSection) error {
	names := sortedPBSEndpointNames(desired)
	for _, name := range names {
		if err := upsertPBSNotificationEndpoint(ctx, typ, name, desired[name]); err != nil {
			return err
		}
	}
	return nil
}

func sortedPBSEndpointNames(desired map[string]pbsNotificationEndpointSection) []string {
	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func upsertPBSNotificationEndpoint(ctx context.Context, typ, name string, endpoint pbsNotificationEndpointSection) error {
	flags := buildProxmoxManagerFlags(endpoint.section.Entries)
	createArgs := append([]string{"notification", "endpoint", typ, "create", name}, endpoint.positional...)
	createArgs = append(createArgs, flags...)
	if _, err := runPBSManagerRedacted(ctx, createArgs, endpoint.redactFlags, endpoint.redactIndex); err != nil {
		updateArgs := append([]string{"notification", "endpoint", typ, "update", name}, endpoint.positional...)
		updateArgs = append(updateArgs, flags...)
		if _, upErr := runPBSManagerRedacted(ctx, updateArgs, endpoint.redactFlags, endpoint.redactIndex); upErr != nil {
			return fmt.Errorf("endpoint %s:%s: %w", typ, name, errors.Join(err, upErr))
		}
	}
	return nil
}

func syncPBSNotificationMatchers(ctx context.Context, desired pbsNotificationDesiredState) error {
	for _, name := range desired.matcherNames {
		if err := upsertPBSNotificationMatcher(ctx, name, desired.matchers[name]); err != nil {
			return err
		}
	}
	return nil
}

func upsertPBSNotificationMatcher(ctx context.Context, name string, matcher proxmoxNotificationSection) error {
	flags := buildProxmoxManagerFlags(matcher.Entries)
	createArgs := append([]string{"notification", "matcher", "create", name}, flags...)
	if _, err := runPBSManager(ctx, createArgs...); err != nil {
		updateArgs := append([]string{"notification", "matcher", "update", name}, flags...)
		if _, upErr := runPBSManager(ctx, updateArgs...); upErr != nil {
			return fmt.Errorf("matcher %s: %w", name, errors.Join(err, upErr))
		}
	}
	return nil
}

func listPBSNotificationIDs(ctx context.Context, args ...string) ([]string, error) {
	out, err := runPBSManager(ctx, append([]string{"notification"}, args...)...)
	if err != nil {
		return nil, err
	}
	current, err := parsePBSListIDs(out, "name", "id")
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", strings.Join(args, " "), err)
	}
	return current, nil
}

func pbsNotificationSectionKey(typ, name string) string {
	return fmt.Sprintf("%s:%s", strings.TrimSpace(typ), strings.TrimSpace(name))
}
