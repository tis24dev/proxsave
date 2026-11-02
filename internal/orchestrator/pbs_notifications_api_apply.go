// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

type pbsNotificationEndpointSection struct {
	section      proxmoxNotificationSection
	redactFlags  []string
	sectionKey   string
	endpointType string
}

type pbsNotificationDesiredState struct {
	endpoints    []pbsNotificationEndpointSection
	matchers     map[string]proxmoxNotificationSection
	matcherNames []string
}

// pbsNotificationApplyReport is an ACCOUNT of what the apply observed and what it did, not
// a verdict on whether it "succeeded". There is deliberately no succeeded/applied field:
// the presence of an input is never evidence that an operation occurred, and several facts
// are true simultaneously -- the file was staged AND it was empty AND three live objects
// were deleted. No bool carries that; the caller phrases the operator sentence from these
// counters.
//
// WHERE THE COUNTERS LIVE, and why not in runPBSManagerRedacted:
//  1. That choke point is shared with the sibling appliers driven from the same ctx, so a
//     counter there collects their commands too.
//  2. It cannot separate a mutating call from a read-only one without sniffing argv for
//     "create"/"update"/"remove" -- inferring intent from a string is the same class of
//     unfounded affirmation this change removes.
//  3. upsertPBSNotificationEndpoint issues create and, on failure, update: one logical
//     change, two commands. A command counter double-counts and counts the FAILED create
//     as work.
//  4. Clean mode with an empty staged cfg issues read-only list commands and mutates
//     nothing; "commands ran" is not a usable proxy for "work done".
//
// Every mutating counter below is incremented only AFTER proxmox-backup-manager
// acknowledged the command.
type pbsNotificationApplyReport struct {
	staged      bool // etc/proxmox-backup/notifications.cfg existed in the stage
	stagedEmpty bool // ...and its content was empty or whitespace-only
	sections    int  // section headers parseProxmoxNotificationSections recognised
	planned     int  // endpoints + matchers the desired state actually names

	droppedUnknownType int
	droppedIncomplete  int
	droppedTypes       []string // endpoint kinds that lost at least one section

	endpointsUpserted int
	matchersUpserted  int
	removed           []string // objects deleted, named: "matcher:x", "smtp:y"
	removeFailed      []string // objects PBS refused to delete (left in place)
	removalsSkipped   []string // endpoint kinds whose Clean removal pass was not run
	// listFailed are the kinds ("matcher" or an endpoint type) whose live objects this
	// PBS could not list, so their Clean removal pass was not run; PBS 3.2 has no
	// webhook list.
	listFailed []pbsNotificationListFailure
	// emptyCleanRefused is set when Clean met an empty staged notifications.cfg: it
	// removes nothing on that evidence.
	emptyCleanRefused bool
}

// mutated reports whether at least one mutating command was acknowledged. This is the
// predicate the old `applied` bool was pretending to be.
//
// It is deliberately NOT "the live state now differs": an update that rewrites identical
// values is counted, because we cannot observe the difference and must not claim to. The
// caller therefore says "applied", with counts, and never "changed".
func (r pbsNotificationApplyReport) mutated() bool {
	return r.endpointsUpserted > 0 || r.matchersUpserted > 0 || len(r.removed) > 0
}

func (r pbsNotificationApplyReport) dropped() int {
	return r.droppedUnknownType + r.droppedIncomplete
}

func applyPBSNotificationsViaAPI(ctx context.Context, logger *logging.Logger, stageRoot string, strict bool) (pbsNotificationApplyReport, error) {
	var rep pbsNotificationApplyReport

	desired, err := loadPBSNotificationDesiredState(stageRoot, logger, &rep)
	if err != nil || !rep.staged {
		return rep, err
	}
	rep.planned = len(desired.endpoints) + len(desired.matchers)

	// Clean mode still runs on an empty desired state: the removals are REAL WORK and must
	// be counted, not short-circuited, or a run that deleted everything would report
	// "nothing was applied". Whether Clean should be ALLOWED to wipe a live configuration
	// on the authority of a staged file that names nothing is a policy question this change
	// deliberately does not answer -- it makes the wipe visible and warns that its only
	// evidence was such a file.
	// An empty staged file names nothing: Clean does not wipe the live configuration on
	// that evidence alone.
	removals := strict && !rep.stagedEmpty
	rep.emptyCleanRefused = strict && rep.stagedEmpty
	// The order lets every removal go through: a stale matcher goes first (it may hold a
	// stale endpoint), and a stale endpoint last, once the matchers no longer use it
	// (PBS refuses to delete an endpoint a matcher references).
	if removals {
		if err := removeExtraPBSNotificationMatchers(ctx, logger, desired.matchers, &rep); err != nil {
			return rep, err
		}
	}
	if err := syncPBSNotificationEndpoints(ctx, desired.endpoints, &rep); err != nil {
		return rep, err
	}
	if err := syncPBSNotificationMatchers(ctx, desired, &rep); err != nil {
		return rep, err
	}
	if removals {
		if err := removeStalePBSNotificationEndpoints(ctx, logger, desired.endpoints, &rep); err != nil {
			return rep, err
		}
	}
	// No error is synthesised from rep.removeFailed here. That would make err != nil mean
	// two different things -- "aborted mid-flight" and "completed but could not finish
	// cleanup" -- and the caller's partial-apply sentence would then be false on a fully
	// successful Clean.
	return rep, nil
}

func loadPBSNotificationDesiredState(stageRoot string, logger *logging.Logger, rep *pbsNotificationApplyReport) (pbsNotificationDesiredState, error) {
	cfgSections, privSections, err := readPBSNotificationStageSections(stageRoot, rep)
	if err != nil || !rep.staged {
		return pbsNotificationDesiredState{}, err
	}
	return buildPBSNotificationDesiredState(cfgSections, privSections, logger, rep), nil
}

func readPBSNotificationStageSections(stageRoot string, rep *pbsNotificationApplyReport) ([]proxmoxNotificationSection, []proxmoxNotificationSection, error) {
	cfgRaw, cfgPresent, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/notifications.cfg")
	// NOTE: on a non-ENOENT read error readStageFileOptional returns (present=false, err!=nil),
	// so rep.staged stays false AND err is non-nil. The caller must therefore never print the
	// "!staged" sentence on the error path, or an unreadable staged file gets reported as
	// "this backup contains no notifications.cfg".
	rep.staged = cfgPresent
	if err != nil || !cfgPresent {
		return nil, nil, err
	}
	// readStageFileOptional trims, so "" here means the staged file was empty or
	// whitespace-only. This is the single fact the old `present` bool folded into "the file
	// exists, therefore we have a desired state", and the whole of the bug hangs off it.
	rep.stagedEmpty = cfgRaw == ""

	privRaw, _, err := readStageFileOptional(stageRoot, "etc/proxmox-backup/notifications-priv.cfg")
	if err != nil {
		return nil, nil, err
	}

	cfgSections, err := parseProxmoxNotificationSections(cfgRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("parse staged notifications.cfg: %w", err)
	}
	privSections, err := parseProxmoxNotificationSections(privRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("parse staged notifications-priv.cfg: %w", err)
	}
	rep.sections = len(cfgSections)
	return cfgSections, privSections, nil
}

func buildPBSNotificationDesiredState(cfgSections, privSections []proxmoxNotificationSection, logger *logging.Logger, rep *pbsNotificationApplyReport) pbsNotificationDesiredState {
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
			endpoint, ok := buildPBSNotificationEndpoint(section, privByKey, privRedactFlagsByKey, logger)
			if !ok {
				// buildPBSNotificationEndpoint already warned with the specific missing
				// field. Record the KIND as well: in Clean mode a kind that lost a section
				// has an incomplete desired set, so "live but not desired" no longer means
				// "absent from the backup".
				rep.droppedIncomplete++
				rep.droppedTypes = appendUniquePBSString(rep.droppedTypes, typ)
				continue
			}
			desired.endpoints = append(desired.endpoints, endpoint)
		case "matcher":
			desired.matchers[name] = section
		default:
			if logger != nil {
				logger.Warning("PBS notifications API apply: unknown section %q (%s); skipping", typ, name)
			}
			rep.droppedUnknownType++
		}
	}

	desired.matcherNames = sortedPBSMatcherNames(desired.matchers)
	return desired
}

func appendUniquePBSString(items []string, want string) []string {
	if slices.Contains(items, want) {
		return items
	}
	return append(items, want)
}

func containsPBSApplyString(items []string, want string) bool {
	return slices.Contains(items, want)
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
	if !pbsEndpointHasRequiredKeys(typ, name, section.Entries, logger) {
		return pbsNotificationEndpointSection{}, false
	}

	redactFlags := notificationRedactFlags(section)
	if extra := privRedactFlagsByKey[key]; len(extra) > 0 {
		redactFlags = append(redactFlags, extra...)
	}

	return pbsNotificationEndpointSection{
		section:      section,
		redactFlags:  redactFlags,
		sectionKey:   key,
		endpointType: typ,
	}, true
}

// pbsEndpointRequiredKeys are the settings "notification endpoint <type> create"
// requires besides the name (PBS 4.2.8 usage, measured on pve-test): the name is its
// only positional argument, every setting goes as a flag. A section missing one is
// skipped, as PBS would refuse it.
var pbsEndpointRequiredKeys = map[string][]string{
	"smtp":    {"from-address", "server"},
	"gotify":  {"server", "token"},
	"webhook": {"method", "url"},
}

func pbsEndpointHasRequiredKeys(typ, name string, entries []proxmoxNotificationEntry, logger *logging.Logger) bool {
	for _, key := range pbsEndpointRequiredKeys[typ] {
		if value, _, ok := popEntryValue(entries, key); !ok || strings.TrimSpace(value) == "" {
			if logger != nil {
				logger.Warning("PBS notifications API apply: %s endpoint %s missing %s; skipping", typ, name, key)
			}
			return false
		}
	}
	return true
}

func sortedPBSMatcherNames(matchers map[string]proxmoxNotificationSection) []string {
	names := make([]string, 0, len(matchers))
	for name := range matchers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// pbsNotificationListFailure is a kind whose live objects could not be listed.
type pbsNotificationListFailure struct {
	kind  string
	cause string
}

// recordPBSNotificationListFailure turns a failed list into a skipped removal pass; a
// cancellation is returned instead.
func recordPBSNotificationListFailure(ctx context.Context, logger *logging.Logger, kind string, err error, rep *pbsNotificationApplyReport) error {
	if ctx.Err() != nil {
		return err
	}
	if logger != nil {
		logger.Debug("PBS notifications API apply: %s list failed, its removals are skipped: %v", kind, err)
	}
	cause, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	rep.listFailed = append(rep.listFailed, pbsNotificationListFailure{kind: kind, cause: cause})
	return nil
}

func removeExtraPBSNotificationMatchers(ctx context.Context, logger *logging.Logger, desired map[string]proxmoxNotificationSection, rep *pbsNotificationApplyReport) error {
	current, err := listPBSNotificationIDs(ctx, "matcher", "list")
	if err != nil {
		return recordPBSNotificationListFailure(ctx, logger, "matcher", err, rep)
	}
	for _, name := range current {
		if _, ok := desired[name]; ok {
			continue
		}
		if _, err := runPBSManager(ctx, "notification", "matcher", "delete", name); err != nil {
			// The nil guard the rest of this file already uses and this loop did not:
			// logWithLabel locks a mutex on the receiver with no nil-receiver check, so a
			// nil logger panics here rather than no-opping.
			if logger != nil {
				logger.Warning("PBS notifications API apply: matcher delete %s failed (continuing): %v", name, err)
			}
			rep.removeFailed = append(rep.removeFailed, "matcher:"+name)
			continue
		}
		rep.removed = append(rep.removed, "matcher:"+name)
	}
	return nil
}

func syncPBSNotificationEndpoints(ctx context.Context, endpoints []pbsNotificationEndpointSection, rep *pbsNotificationApplyReport) error {
	for _, typ := range []string{"smtp", "sendmail", "gotify", "webhook"} {
		if err := upsertPBSNotificationEndpoints(ctx, typ, pbsEndpointsByName(endpoints, typ), rep); err != nil {
			return err
		}
	}
	return nil
}

// removeStalePBSNotificationEndpoints is the Clean removal pass over the endpoints,
// run after the matchers are updated.
func removeStalePBSNotificationEndpoints(ctx context.Context, logger *logging.Logger, endpoints []pbsNotificationEndpointSection, rep *pbsNotificationApplyReport) error {
	for _, typ := range []string{"smtp", "sendmail", "gotify", "webhook"} {
		// A kind whose staged section we could not REBUILD has an incomplete desired
		// set, so "live but not in desired" stops meaning "absent from the backup" and
		// starts meaning "we failed to translate it". Deleting on that evidence
		// destroys configuration the backup DID contain and cannot restore. Leaving
		// stale objects is recoverable by hand; deleting a live endpoint is not.
		if containsPBSApplyString(rep.droppedTypes, typ) {
			rep.removalsSkipped = appendUniquePBSString(rep.removalsSkipped, typ)
			continue
		}
		if err := removeExtraPBSNotificationEndpoints(ctx, logger, typ, pbsEndpointsByName(endpoints, typ), rep); err != nil {
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

func removeExtraPBSNotificationEndpoints(ctx context.Context, logger *logging.Logger, typ string, desired map[string]pbsNotificationEndpointSection, rep *pbsNotificationApplyReport) error {
	current, err := listPBSNotificationIDs(ctx, "endpoint", typ, "list")
	if err != nil {
		return recordPBSNotificationListFailure(ctx, logger, typ, err, rep)
	}
	for _, name := range current {
		if _, ok := desired[name]; ok {
			continue
		}
		if _, err := runPBSManager(ctx, "notification", "endpoint", typ, "delete", name); err != nil {
			if logger != nil {
				logger.Warning("PBS notifications API apply: endpoint delete %s:%s failed (continuing): %v", typ, name, err)
			}
			rep.removeFailed = append(rep.removeFailed, typ+":"+name)
			continue
		}
		rep.removed = append(rep.removed, typ+":"+name)
	}
	return nil
}

// The leaves (upsertPBSNotificationEndpoint / upsertPBSNotificationMatcher) stay pure:
// counting happens in the loop, AFTER the error check, so the create-then-update fallback
// counts once and a failure counts zero.
func upsertPBSNotificationEndpoints(ctx context.Context, typ string, desired map[string]pbsNotificationEndpointSection, rep *pbsNotificationApplyReport) error {
	for _, name := range sortedPBSEndpointNames(desired) {
		if err := upsertPBSNotificationEndpoint(ctx, typ, name, desired[name]); err != nil {
			return err
		}
		rep.endpointsUpserted++
	}
	return nil
}

func sortedPBSEndpointNames(desired map[string]pbsNotificationEndpointSection) []string {
	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func upsertPBSNotificationEndpoint(ctx context.Context, typ, name string, endpoint pbsNotificationEndpointSection) error {
	flags := buildProxmoxManagerFlags(endpoint.section.Entries)
	createArgs := []string{"notification", "endpoint", typ, "create", name}
	createArgs = append(createArgs, flags...)
	if _, err := runPBSManagerRedacted(ctx, createArgs, endpoint.redactFlags, nil); err != nil {
		updateArgs := []string{"notification", "endpoint", typ, "update", name}
		updateArgs = append(updateArgs, flags...)
		if _, upErr := runPBSManagerRedacted(ctx, updateArgs, endpoint.redactFlags, nil); upErr != nil {
			return fmt.Errorf("endpoint %s:%s: %w", typ, name, errors.Join(err, upErr))
		}
	}
	return nil
}

func syncPBSNotificationMatchers(ctx context.Context, desired pbsNotificationDesiredState, rep *pbsNotificationApplyReport) error {
	for _, name := range desired.matcherNames {
		if err := upsertPBSNotificationMatcher(ctx, name, desired.matchers[name]); err != nil {
			return err
		}
		rep.matchersUpserted++
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

// listPBSNotificationIDs lists the live objects of one kind that a Clean restore may
// remove. Built-in objects (origin builtin or modified-builtin: default-matcher,
// mail-to-root) are left out: PBS resets them on delete instead of removing them.
func listPBSNotificationIDs(ctx context.Context, args ...string) ([]string, error) {
	cmd := append(append([]string{"notification"}, args...), "--output-format=json")
	out, err := runPBSManager(ctx, cmd...)
	if err != nil {
		return nil, err
	}
	current, err := parsePBSListIDs(out, "name", "id")
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", strings.Join(args, " "), err)
	}
	data := unwrapPBSJSONData(out)
	if len(data) == 0 {
		return current, nil
	}
	var rows []struct {
		Name   string `json:"name"`
		Origin string `json:"origin"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parse %s: %w", strings.Join(args, " "), err)
	}
	builtin := make(map[string]bool)
	for _, row := range rows {
		if origin := strings.TrimSpace(row.Origin); origin == "builtin" || origin == "modified-builtin" {
			builtin[strings.TrimSpace(row.Name)] = true
		}
	}
	return slices.DeleteFunc(current, func(name string) bool { return builtin[name] }), nil
}

func pbsNotificationSectionKey(typ, name string) string {
	return fmt.Sprintf("%s:%s", strings.TrimSpace(typ), strings.TrimSpace(name))
}
