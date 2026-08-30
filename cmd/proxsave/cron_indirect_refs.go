// Package main contains the proxsave command entrypoint.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// This file answers the ONE question the canonical cron matcher deliberately
// cannot: does this crontab schedule ProxSave through something that is NOT the
// proxsave binary itself?
//
// commandTokenMatchesTarget (runtime_helpers.go) decides whether a cron line IS a
// proxsave entry, and everything that DELETES a cron line keys off it. Its
// narrowness is a guarantee rather than an oversight: it reads the command token's
// basename only, so a line like "cp /usr/local/bin/proxsave /backup/" is never
// mistaken for a proxsave job and removed. Nothing in this file widens it, and
// nothing in this file removes a line.
//
// The gap that narrowness leaves is what issue #298 hit. Four hosts ran
//
//	MM 02 * * * /usr/local/sbin/proxsave-nas-guard
//
// an operator wrapper that verifies the final NAS mount really is CIFS/SMB before
// invoking ProxSave. Its basename is "proxsave-nas-guard", not "proxsave", so the
// --upgrade retrofit saw NO proxsave cron entry at all: it installed the daemon,
// announced that "the cron entry was removed", removed nothing, and left every
// host running two backups a night, the loser of the 02:00 race exiting
// ExitBackupSkipped (16).
//
// The detector below is therefore SEPARATE and ADDITIVE. It only ever looks at
// lines commandTokenMatchesTarget has already rejected, and its only outcomes are
// a refusal (on the unattended --upgrade retrofit) and a warning (on the paths an
// operator asked for explicitly). It never edits the crontab, because an
// operator's wrapper is not ours to rewrite: it can carry a mount guard, an flock,
// its own logging and its own exit handling, and deleting it on a guess destroys a
// hand-written safety net. Detect, say exactly which line and why, change nothing.

const (
	// cronProbeReadScripts / cronProbeNamesOnly name indirectProxsaveCronRefs' second
	// argument at the call site, because a bare true/false there says nothing about
	// what it costs. cronProbeReadScripts also OPENS AND READS the command of every
	// unmatched cron line (bounded, see maxCronWrapperProbeBytes); cronProbeNamesOnly
	// stays purely lexical and touches no disk. The scheduler-time advisory in
	// deriveSchedulerTimeFromCrontab runs inside the install wizard and inside
	// --upgrade-config-json, so it takes the lexical form; the daemon paths run once
	// and must not miss a wrapper, so they pay for the read.
	cronProbeReadScripts = true
	cronProbeNamesOnly   = false

	// maxCronWrapperProbeBytes bounds the content probe. A cron wrapper is a shell
	// script of a few KiB, so the cap costs no real detection while keeping a cron
	// line that points at a large binary from being slurped into memory during an
	// upgrade. It bounds the READ, not the open: a command on a stalled network mount
	// can still block in open(2), exactly as every other cron-touching helper here can.
	maxCronWrapperProbeBytes = 64 * 1024
)

// indirectCronRef is one crontab line that appears to run ProxSave WITHOUT its
// command token being the proxsave binary, i.e. exactly the shape
// dropCanonicalCronLines is blind to by design.
//
// Line is the trimmed crontab line as the operator wrote it, quoted back verbatim
// so they can find it in `crontab -l` without guessing; Command is the cron command
// token; Reason is the single rule that fired, in operator-facing words, so that
// even a false positive explains itself instead of looking like a malfunction.
type indirectCronRef struct {
	Line    string
	Command string
	Reason  string

	// Source is the file the line came from, empty for the root user's own crontab
	// (the one `crontab -l` prints). It is set for /etc/crontab and /etc/cron.d/*,
	// because "remove that entry with crontab -e" is wrong advice for those: they are
	// plain files, edited with an editor, and on some hosts owned by a package.
	Source string
}

// cronCommandRunners are the command names that exist to RUN ANOTHER COMMAND. They
// are the only commands for which this file looks past the cron command token at
// the rest of the line, and that gate is the whole reason doing so is safe here
// while it would be wrong in commandTokenMatchesTarget: "cp /usr/local/bin/proxsave
// /backup/" keeps its documented protection because cp is not a runner, whereas
// "flock -n /var/lock/x /usr/local/bin/proxsave --backup" IS a proxsave job whose
// command token happens to be flock.
//
// su/sudo/doas/runuser earn their place the same way: they only ever match when the
// arguments name proxsave, in which case the line really does run ProxSave.
var cronCommandRunners = map[string]struct{}{
	"sh": {}, "bash": {}, "dash": {}, "ash": {}, "ksh": {}, "zsh": {}, "busybox": {},
	"env": {}, "nice": {}, "ionice": {}, "nohup": {}, "setsid": {}, "stdbuf": {},
	"flock": {}, "timeout": {}, "chronic": {}, "eatmydata": {},
	"su": {}, "sudo": {}, "doas": {}, "runuser": {}, "systemd-cat": {}, "systemd-run": {},
}

// indirectProxsaveCronRefs returns every crontab line that looks like an INDIRECT
// ProxSave invocation. A line whose command token is the proxsave binary is never
// returned: that one is canonical, dropCanonicalCronLines already owns it, and
// reporting it here would turn every normal host into a refusal.
//
// Rules fire BEHAVIOUR FIRST, and the first match wins so the reported reason is
// the strongest one:
//
//  1. CONTENT (probeScriptContent only). The command is an absolute path to a small
//     readable text file that references the proxsave binary by path. This is the
//     strongest evidence available without executing anything, and it is what
//     catches a wrapper with a completely neutral name, e.g. /usr/local/sbin/nas-guard.
//
//  2. INSTALL TREE. A directory component of the command's path is exactly
//     "proxsave", i.e. the command is a script stored inside a ProxSave install root.
//
//  3. RUNNER. The command is a known command runner (see cronCommandRunners) and
//     some later word on the line names the proxsave binary. This is the only place
//     the rest of the line is read at all.
//
//  4. NAME, and only as a FALLBACK. The command's basename carries "proxsave" as a
//     whole component ("proxsave-nas-guard", "proxsave_wrapper.sh") AND rule 1 could
//     not read it. ProxSave installs a binary called exactly "proxsave", so
//     "proxsave-anything" is by definition the operator's own file: the name records
//     who wrote it, never what it does. When the script CAN be read the reading
//     decides, so an ordinary proxsave-metrics-exporter that never invokes the binary
//     is left alone however it is named. Rules 2 and 3 are not gated that way: a path
//     inside the install tree and a runner naming the binary are facts about the cron
//     line itself, not guesses about a file's contents.
//
//     The rule deliberately does not look for "proxmox-backup" as a component either:
//     proxmox-backup-client, -proxy, -manager and -file-restore are stock PBS binaries
//     present on nearly every target host, and flagging them would refuse the migration
//     almost everywhere. A proxmox-backup-named wrapper is left to rules 1 to 3, which
//     need the actual binary path and therefore cannot be confused by PBS.
//
// False positives are cheaper than false negatives here, because the caller's answer
// to a positive is "refuse and tell the operator", never "delete". That tolerance is
// what pays for rule 4 surviving at all: an unreadable proxsave-named command costs
// the operator one --daemon-setup and they keep a working cron schedule meanwhile,
// while missing it costs them two backups a night with no message, which is what #298
// was.
//
// The residual false negative is a neutrally-named wrapper this cannot read (an
// unreadable file, a compiled binary, a command over a stalled mount). It is not
// treated as suspicious: every ordinary cron job on the host is unreadable-as-text
// too, so refusing on "cannot tell" would refuse on essentially every host and the
// warning would stop meaning anything.
//
// It logs NOTHING and writes NOTHING: --upgrade-config-json reaches this through
// deriveSchedulerTimeFromCrontab and its stdout must stay pure JSON.
func indirectProxsaveCronRefs(lines []string, probeScriptContent bool) []indirectCronRef {
	return indirectProxsaveCronRefsWithToken(lines, probeScriptContent, cronCommandToken)
}

// indirectProxsaveCronRefsWithToken is indirectProxsaveCronRefs parameterised by the
// command extractor, which is the ONLY thing that differs between the user crontab
// format (`crontab -l`, command in field 6) and the system one (/etc/crontab and
// /etc/cron.d, a USER field first and the command in field 7). The three rules, the
// canonical-entry exclusion and the fail-quiet behaviour are shared verbatim, so the
// two habitats can never drift into judging the same wrapper differently.
func indirectProxsaveCronRefsWithToken(lines []string, probeScriptContent bool, commandToken func(string) string) []indirectCronRef {
	var refs []indirectCronRef
	for _, line := range lines {
		token := strings.Trim(commandToken(line), "\"'")
		if token == "" {
			// Blank line, comment, env assignment, or a schedule with no command:
			// the extractor already rejected all four.
			continue
		}
		if commandTokenMatchesTarget(token) {
			continue // canonical proxsave entry: not indirect, and not ours to report
		}
		// Behavioural rules first, the name LAST and only as a fallback. ProxSave installs a
		// binary called exactly "proxsave", so "proxsave-anything" is by definition the
		// operator's own file and its name reports who wrote it, never what it does. When the
		// script can be read, the reading decides: a wrapper that calls the binary is caught by
		// content, and one that does not is left alone however it is named - which is what stops
		// an ordinary proxsave-metrics-exporter from refusing an upgrade. The name votes only
		// when nothing could be read (a compiled wrapper, an unreadable file, a command on a
		// stalled mount), where it is the last thing standing between the operator and #298.
		reason := ""
		probed, readable := false, false
		if probeScriptContent {
			probed = true
			var references bool
			references, readable = scriptProxsaveProbe(token)
			if references {
				reason = fmt.Sprintf("script %s calls the proxsave binary", token)
			}
		}
		if reason == "" {
			switch {
			case pathLivesInProxsaveTree(token):
				reason = fmt.Sprintf("command under ProxSave install tree (%s)", filepath.Dir(token))
			case cronRunnerNamesProxsave(line, token):
				reason = fmt.Sprintf("runner %q; cron line references the proxsave binary", filepath.Base(token))
			case (!probed || !readable) && basenameHasProxsaveComponent(token):
				reason = fmt.Sprintf("command %q is named after proxsave and could not be read", filepath.Base(token))
			}
		}
		if reason == "" {
			continue
		}
		refs = append(refs, indirectCronRef{
			Line:    strings.TrimSpace(line),
			Command: token,
			Reason:  reason,
		})
	}
	return refs
}

// wrapperCronLines is the plain-lines view of indirectProxsaveCronRefs: given the
// crontab, it returns just the lines that already schedule ProxSave through a command
// this codebase does not own, verbatim and in crontab order.
//
// It exists because applyCronMode does not need the reasons, only the answer to "is
// this host already scheduled by something that is not us?", and a caller that only
// needs a yes/no should not have to reach into the finding struct to get it. The
// contract it publishes is deliberately two-valued and error-free: a NON-EMPTY result
// means positively identified; an EMPTY result means "none found, OR could not be
// told", and the caller must treat those two identically. Collapsing "cannot tell"
// into "none" is safe ONLY because the caller's fallback for "none" is to keep doing
// exactly what it does today.
//
// It pays for the content probe (cronProbeReadScripts): its callers run once, on an
// explicit operator command, and a wrapper missed here costs a duplicate nightly
// backup.
func wrapperCronLines(lines []string) []string {
	refs := indirectProxsaveCronRefs(lines, cronProbeReadScripts)
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Line)
	}
	return out
}

// basenameHasProxsaveComponent reports whether the command's basename carries
// "proxsave" as a WHOLE component, splitting on the three separators a script name
// actually uses ("-", "_", "."). Component equality, not substring containment, is
// what keeps the existing guarantee that "/usr/local/bin/proxsavex" is a different
// binary and not this one (TestFilterCronLines pins that case).
//
// It answers true for a bare "proxsave" too. Every caller has already excluded the
// canonical case via commandTokenMatchesTarget, and inside the content probe that
// overlap is wanted: a wrapper naming /opt/site/proxsave is naming our binary.
func basenameHasProxsaveComponent(token string) bool {
	base := strings.ToLower(filepath.Base(strings.Trim(token, "\"'")))
	for _, part := range strings.FieldsFunc(base, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		if part == "proxsave" {
			return true
		}
	}
	return false
}

// pathLivesInProxsaveTree reports whether a DIRECTORY component of the command's
// path is exactly "proxsave", i.e. the command is a script stored inside a ProxSave
// install root (/opt/proxsave/script/proxmox-backup.sh, the pre-0.30 Bash entry
// point, being the case that matters most: the daemon migration never calls
// dropLegacyBashCronLines, so such a line would otherwise survive the migration and
// double-schedule the host).
//
// Exact component equality again, and only "proxsave": a directory literally named
// proxmox-backup exists on stock PBS hosts (/etc/proxmox-backup, /var/lib/proxmox-backup)
// and would produce refusals on hosts that have nothing to do with us.
func pathLivesInProxsaveTree(token string) bool {
	path := strings.Trim(token, "\"'")
	if !strings.Contains(path, "/") {
		return false
	}
	for _, part := range strings.Split(strings.ToLower(filepath.Dir(path)), "/") {
		if part == "proxsave" {
			return true
		}
	}
	return false
}

// cronRunnerNamesProxsave reports whether the line's command is a known runner AND
// some word on the line names the proxsave binary. Splitting on shell metacharacters
// as well as whitespace is what makes the quoted form work: in
// "/bin/bash -c 'mountpoint -q /mnt/nas && /usr/local/bin/proxsave --backup'"
// strings.Fields would hand back the whole quoted script as a single word.
//
// The scan is deliberately dumb about shell semantics. Inside a shell invocation we
// cannot distinguish "runs proxsave" from "mentions proxsave", so a "bash -c 'cp
// /usr/local/bin/proxsave /backup'" line is reported here even though the same
// command would be correctly ignored when written directly. That asymmetry is on
// purpose: outside a runner the narrow matcher's guarantee applies, inside one we
// have no parser and the safe side is to say so.
func cronRunnerNamesProxsave(line, token string) bool {
	if _, isRunner := cronCommandRunners[strings.ToLower(filepath.Base(strings.Trim(token, "\"'")))]; !isRunner {
		return false
	}
	for _, word := range shellWords(line) {
		if commandTokenMatchesTarget(word) || basenameHasProxsaveComponent(word) {
			return true
		}
	}
	return false
}

// shellWords splits on whitespace AND the shell metacharacters that glue a path to
// its neighbours, so "&&/usr/local/bin/proxsave", "${BASE_DIR}/proxsave" and
// "--config=/etc/proxsave.env" all yield the path as a word. It is a tokenizer for
// RECOGNITION only; nothing here is ever executed.
func shellWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ';', '|', '&', '(', ')', '{', '}', '<', '>', '=', ',', '\'', '"', '`', '$':
			return true
		}
		return false
	})
}

// scriptReferencesProxsave reads the cron command as a text file and reports whether
// it names the proxsave binary BY PATH. It is the last resort for a wrapper whose
// name gives nothing away, and every gate on it is there to keep it from turning
// into a general file scan:
//
//   - absolute path only (a relative cron command resolves against the crontab
//     owner's home, which we are not going to guess);
//   - opened through safefs.OpenFileUnderRoot, so the operator-controlled path is
//     confined at the syscall level exactly like every other operator path this
//     command reads;
//   - regular file, at most maxCronWrapperProbeBytes, and no NUL byte, which drops
//     compiled binaries (every stock /usr/bin/* cron command) before the scan;
//   - only PATH-shaped words count. A wrapper invoked by cron must call the binary
//     by absolute path anyway, because cron's default PATH is /usr/bin:/bin and the
//     entry point lives in /usr/local/bin, so requiring a "/" costs no real
//     detection while keeping a prose mention of "proxsave" in a comment from
//     blocking an upgrade.
//
// Any failure (unreadable, too large, binary) returns false: see the false-negative
// note on indirectProxsaveCronRefs for why "cannot tell" must not mean "suspicious".
func scriptReferencesProxsave(token string) bool {
	references, _ := scriptProxsaveProbe(token)
	return references
}

// scriptProxsaveProbe is scriptReferencesProxsave with the third answer kept. It returns
// whether the script names the binary AND whether it could be read at all, because those
// are different facts and the name rule depends on telling them apart: a script we READ and
// found nothing in is evidence of absence, while one we could not open says nothing either
// way. Collapsing both into false is what made "proxsave-metrics-exporter" indistinguishable
// from a compiled wrapper.
func scriptProxsaveProbe(token string) (references bool, readable bool) {
	path := strings.Trim(token, "\"'")
	if !filepath.IsAbs(path) {
		return false, false
	}
	f, err := safefs.OpenFileUnderRoot(resolveSystemCronPath(path), os.O_RDONLY, 0)
	if err != nil {
		return false, false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCronWrapperProbeBytes {
		return false, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCronWrapperProbeBytes))
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return false, false
	}
	// Carriage returns are stripped so the line passes read the same either way. Not because such
	// a host behaves differently: a script with CRLF endings does not execute on Linux at all,
	// the interpreter becoming "/bin/sh\r", so it schedules nothing. It costs one line to read it
	// correctly rather than emit an advisory about a script that cannot run.
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	lines := scriptCodeLines(strings.Split(string(data), "\n"))
	vars := scriptProxsavePathVars(lines)
	for _, line := range lines {
		if scriptLineRunsProxsave(line, vars) {
			return true, true
		}
	}
	return false, true
}

// The three categories a word in command position can be. They exist because one category,
// "runner", used to do both jobs and did neither well: it opened the WHOLE rest of the segment
// to matching, which let "sudo cp /usr/local/bin/proxsave /backup/" match on an argument, while
// a word that was not a runner ended the segment, which hid the command behind "if", "while"
// and "!" - words that consume nothing at all.
var (
	// scriptCommandPrefixes take NO options at all, so the command position resumes on the very
	// next word. Membership means exactly that; anything that accepts a flag belongs below, or
	// the command position lands on the flag. "command" is here on purpose: as a prefix,
	// "command proxsave --backup" is a run and "command -v proxsave" is a query, which is the
	// right answer to both.
	scriptCommandPrefixes = map[string]struct{}{
		"if": {}, "while": {}, "until": {}, "elif": {}, "then": {}, "else": {}, "do": {}, "!": {},
		"{": {}, "}": {},
		"exec": {}, "command": {}, "builtin": {},
	}
	// scriptCommandLaunchers take options of their own and then the command. Skipping the
	// options, their values, and any leading assignments puts us back in command position.
	scriptCommandLaunchers = map[string]struct{}{
		"sudo": {}, "doas": {}, "env": {}, "xargs": {},
		"nice": {}, "ionice": {}, "setsid": {}, "stdbuf": {}, "nohup": {}, "eatmydata": {},
		"chronic": {}, "time": {},
	}
	// scriptOperandLaunchers take options AND one operand of their own before the command:
	// flock's lock file, timeout's duration.
	scriptOperandLaunchers = map[string]struct{}{
		"flock": {}, "timeout": {},
	}
	// launcherValueFlags names, per launcher, the SHORT and long options that consume the next
	// word as their value. Without this the value is mistaken for the command - "flock -w 600
	// /lock cmd" stopped on 600, took it for the lock file, and landed past the command - and the
	// wrapper behind it goes unseen, which is issue #298 with no warning at all.
	//
	// Bundled forms ("-c3", "-n19", "-oL") and "--flag=value" are single words and need no entry:
	// the option skip already drops them whole.
	launcherValueFlags = map[string]map[string]struct{}{
		"sudo": {"-u": {}, "-g": {}, "-p": {}, "-C": {}, "-T": {}, "-h": {}, "-r": {}, "-U": {}, "--user": {}, "--group": {}, "--prompt": {}, "--host": {}},
		"doas": {"-u": {}, "-C": {}},
		// No -S / --split-string here on purpose: their value IS the command, the way sh -c's is,
		// so skipping it threw away the very thing being looked for. Left out, the option reads
		// as boolean and the command string lands in command position, which is where it belongs.
		"env":     {"-u": {}, "-C": {}, "--unset": {}, "--chdir": {}},
		"xargs":   {"-n": {}, "-L": {}, "-P": {}, "-I": {}, "-d": {}, "-s": {}, "-E": {}, "-a": {}},
		"nice":    {"-n": {}, "--adjustment": {}},
		"ionice":  {"-c": {}, "-n": {}, "-p": {}, "-P": {}, "-u": {}, "--class": {}, "--classdata": {}, "--pid": {}},
		"stdbuf":  {"-i": {}, "-o": {}, "-e": {}, "--input": {}, "--output": {}, "--error": {}},
		"flock":   {"-w": {}, "-E": {}, "--timeout": {}, "--conflict-exit-code": {}},
		"timeout": {"-k": {}, "-s": {}, "--kill-after": {}, "--signal": {}},
	}
	// scriptShellLaunchers run a script passed as the argument to -c, which is a LINE and is
	// evaluated as one.
	scriptShellLaunchers = map[string]struct{}{
		"sh": {}, "bash": {}, "dash": {}, "ash": {}, "ksh": {}, "zsh": {}, "busybox": {},
	}
	// scriptOpaqueLaunchers put the command somewhere this parser does not model - a -c string
	// mixed with a user name, a -- separator, a unit description. They keep the old broad scan
	// over the rest of the segment, which can match an argument. That is stated rather than
	// pretended away: these four are rare in a backup wrapper, and the cost of a false positive
	// is a reported finding, never an action.
	scriptOpaqueLaunchers = map[string]struct{}{
		"su": {}, "runuser": {}, "systemd-run": {}, "systemd-cat": {},
	}
)

// scriptCodeLines drops the here-document BODIES from a script, so what is left is the lines the
// shell would execute.
//
// A here-doc body is data. A wrapper that prints usage text, writes a README or pipes a message
// to mail can name the backup command inside one, and reading those lines as code turns a script
// that DOCUMENTS ProxSave into a script that runs it - which makes an unattended --upgrade refuse
// to migrate a host that has nothing scheduled at all.
//
// The delimiter is taken from "<<WORD", "<<-WORD" or a quoted form, and the body ends at the
// first line that is exactly that word (trimmed, since "<<-" strips leading tabs). An
// unterminated body runs to the end of the file, which is what the shell does with it too.
func scriptCodeLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	delimiter := ""
	for _, line := range joinLineContinuations(lines) {
		if delimiter != "" {
			if strings.TrimSpace(line) == delimiter {
				delimiter = ""
			}
			continue
		}
		out = append(out, line)
		// A comment cannot open a here-document, and getting this wrong is not a lost line but
		// the rest of the FILE: everything up to a delimiter that never arrives is dropped, the
		// probe answers "read it, found nothing", and that answer suppresses the name fallback
		// too.
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if d := hereDocDelimiter(line); d != "" {
			delimiter = d
		}
	}
	return out
}

// quotesOpenAt reports whether an unclosed quote is still open at the end of the given prefix,
// i.e. whether whatever follows it is inside a string. Escaped quotes do not count, and a quote
// of one kind inside the other is ordinary text.
func quotesOpenAt(prefix string) bool {
	inSingle, inDouble, escaped := false, false, false
	for _, r := range prefix {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && !inSingle:
			escaped = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		}
	}
	return inSingle || inDouble
}

// joinLineContinuations glues a line ending in a backslash to the one after it, because that is
// one command to the shell and has to be one command here.
//
// Read as two lines, the continuation starts at a command position it does not have, so a path
// named as an ARGUMENT reads as a command: "rsync -a --delete \\" followed by "/opt/proxsave/
// /mnt/nas/" was flagged as running the binary, which is the exact false positive the
// command-position rule exists to prevent. It runs before the here-doc pass so a continued line
// cannot hide the "<<" that opens a body.
func joinLineContinuations(lines []string) []string {
	out := make([]string, 0, len(lines))
	pending := ""
	for _, line := range lines {
		joined := pending + line
		// A COMMENT does not continue: the shell ends it at the newline whatever the last
		// character is. Joining one swallowed the command on the next line, which is the same
		// blindness in the other direction.
		comment := pending == "" && strings.HasPrefix(strings.TrimSpace(line), "#")
		if !comment && strings.HasSuffix(joined, "\\") && !strings.HasSuffix(joined, "\\\\") {
			pending = strings.TrimSuffix(joined, "\\") + " "
			continue
		}
		out = append(out, joined)
		pending = ""
	}
	if pending != "" {
		out = append(out, pending)
	}
	return out
}

// hereDocDelimiter returns the word that ends a here-document started on this line, or "".
func hereDocDelimiter(line string) string {
	idx := strings.Index(line, "<<")
	if idx < 0 {
		return ""
	}
	// Only a REDIRECTION opens a body. The same two characters mean a left shift inside an
	// arithmetic expansion and nothing at all inside a string, and treating either as an opener
	// discards every following line of the script.
	before := line[:idx]
	if strings.Contains(before, "$((") {
		return ""
	}
	if quotesOpenAt(before) {
		return ""
	}
	// "<<<" is a here-STRING: it carries its data inline and starts no body.
	rest := line[idx+2:]
	if strings.HasPrefix(rest, "<") {
		return ""
	}
	rest = strings.TrimPrefix(rest, "-")
	rest = strings.TrimLeft(rest, " \t")
	// All three quoting forms mean the same thing - the body is literal - so all three have to be
	// recognised, or the body is read as code.
	word := strings.TrimPrefix(strings.Trim(strings.Fields(rest + " ")[0], "\"'"), "\\")
	if word == "" {
		return ""
	}
	for _, r := range word {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return ""
		}
	}
	return word
}

// scriptProxsavePathVars collects NAME -> value for the assignments in a script whose value is a
// proxsave path, so a later "$NAME" in command position can be resolved.
//
// Only assignments matter, and only where the variable is later USED as a command. That is the
// whole difference between a wrapper holding the binary path and a script holding a directory
// path it merely copies: "BIN=/usr/local/bin/proxsave" followed by "$BIN --backup" runs it,
// while "SRC=/opt/proxsave/" followed by "rsync -a $SRC /mnt/nas/" does not, and the earlier
// any-word probe could not tell them apart.
func scriptProxsavePathVars(lines []string) map[string]string {
	vars := map[string]string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, segment := range splitShellSegments(trimmed) {
			for _, word := range strings.Fields(segment) {
				idx := strings.Index(word, "=")
				if !looksLikeEnvAssignment(word) {
					continue
				}
				value := strings.Trim(word[idx+1:], "\"'")
				if scriptWordRunsProxsave(value) {
					vars[word[:idx]] = value
				}
			}
		}
	}
	return vars
}

// resolveScriptWord expands "$NAME", "${NAME}" and their quoted forms against the assignments
// collected above. Anything else is returned unchanged.
func resolveScriptWord(word string, vars map[string]string) string {
	word = strings.Trim(word, "\"'")
	if !strings.HasPrefix(word, "$") {
		return word
	}
	name := strings.TrimPrefix(word, "$")
	name = strings.TrimSuffix(strings.TrimPrefix(name, "{"), "}")
	if value, ok := vars[name]; ok {
		return value
	}
	return word
}

// scriptLineRunsProxsave reports whether one line of a script puts the proxsave binary in
// COMMAND position, i.e. actually runs it.
//
// The probe used to ask "does a proxsave path appear anywhere in this file", which made a
// nightly "rsync -a /opt/proxsave/ /mnt/nas/" and a "find /var/log/proxsave.log -delete" read
// as wrappers that call the binary. That verdict decides whether an unattended --upgrade
// refuses to migrate and what a revert reports about the host, so naming a path had to stop
// counting as running it.
//
// The parsing is deliberately shallow, because this is RECOGNITION and nothing here is ever
// executed. A line is cut into segments at the shell operators that start a new command, each
// segment's leading environment assignments are skipped, and what stands in command position is
// then walked through the four categories above until a real command is reached.
//
// What it does not model: a path assembled from more than one variable, a command reached
// through `source` or `eval`, and the four opaque launchers listed above. A wrapper missed here
// is REPORTED as missing, never acted on, so the cost of a blind spot is one advisory the
// operator does not get, not a decision taken on bad evidence.
func scriptLineRunsProxsave(line string, vars map[string]string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	for _, segment := range splitShellSegments(trimmed) {
		if segmentRunsProxsave(strings.Fields(segment), vars) {
			return true
		}
	}
	return false
}

// segmentRunsProxsave walks one segment from its command position. It is iterative rather than
// recursive on the launcher categories so a pathological line cannot nest; the ONE recursion is
// a shell's -c argument, which is a genuinely separate line.
func segmentRunsProxsave(words []string, vars map[string]string) bool {
	for len(words) > 0 {
		// Leading assignments belong to the command that follows, not to the command position.
		for len(words) > 0 && looksLikeEnvAssignment(strings.Trim(words[0], "\"'")) {
			words = words[1:]
		}
		if len(words) == 0 {
			return false
		}
		head := resolveScriptWord(words[0], vars)
		if scriptWordRunsProxsave(head) {
			return true
		}
		base := strings.ToLower(filepath.Base(head))
		switch {
		case isIn(base, scriptCommandPrefixes):
			words = words[1:]
		case isIn(base, scriptCommandLaunchers):
			words = skipLauncherOptions(base, words[1:])
		case isIn(base, scriptOperandLaunchers):
			words = skipLauncherOptions(base, words[1:])
			if len(words) > 0 {
				words = words[1:] // the lock file, the duration
			}
		case isIn(base, scriptShellLaunchers):
			return shellLauncherRunsProxsave(base, words[1:], vars)
		case isIn(base, scriptOpaqueLaunchers):
			for _, w := range words[1:] {
				if scriptWordRunsProxsave(resolveScriptWord(w, vars)) {
					return true
				}
			}
			return false
		default:
			// An ordinary command. Everything after it is an argument, and naming a path in an
			// argument is exactly what stopped counting.
			return false
		}
	}
	return false
}

// shellLauncherRunsProxsave handles "sh -c '<script>'": the argument after -c is a line of its
// own. Without a -c the shell is running a FILE, whose contents this probe cannot follow, so the
// remaining words are treated as a command position rather than scanned.
func shellLauncherRunsProxsave(launcher string, words []string, vars map[string]string) bool {
	for i, w := range words {
		if strings.Trim(w, "\"'") == "-c" {
			if i+1 >= len(words) {
				return false
			}
			return scriptLineRunsProxsave(strings.Trim(strings.Join(words[i+1:], " "), "\"'"), vars)
		}
	}
	return segmentRunsProxsave(skipLauncherOptions(launcher, words), vars)
}

// skipLauncherOptions drops the option-shaped words a launcher consumes before its command,
// INCLUDING the value word an option like "-u root" or "-w 600" takes with it.
//
// A bare "--" needs no case of its own: consumed as a value-less option, it leaves the command
// position exactly where an explicit end-of-options branch would have, and no realistic line
// distinguishes the two.
//
// An unknown flag is treated as boolean, and that is the UNSAFE direction, not the safe one: if
// it really takes a value, the command position lands on that value, nothing matches, and the
// wrapper goes unseen - a false NEGATIVE, which is what issue #298 is made of. The table is
// therefore not an optimisation. A launcher added to either launcher set without its
// value-taking flags is a hole, not a rough edge.
func skipLauncherOptions(launcher string, words []string) []string {
	valueFlags := launcherValueFlags[launcher]
	for len(words) > 0 {
		word := strings.Trim(words[0], "\"'")
		if !strings.HasPrefix(word, "-") || word == "-" {
			return words
		}
		words = words[1:]
		if strings.Contains(word, "=") {
			continue // --flag=value carries its value in the same word
		}
		if launcherFlagTakesNextWord(word, valueFlags) && len(words) > 0 {
			words = words[1:]
		}
	}
	return words
}

// launcherFlagTakesNextWord reports whether this option word consumes the following word as its
// value.
//
// Short options BUNDLE: "sudo -Hu backup" is "sudo -H -u backup", and getopt takes -u's value
// from the next word. A whole-word lookup misses that, treats the bundle as boolean, and lands
// the command position on the value - the wrapper behind it unseen, which is the direction that
// costs a duplicate schedule.
//
// The value is inline whenever the bundle does not END on a value-taking letter: "-c3", "-19" and
// "-oL" all carry it in the same word, and there the next word is already the command. Looking at
// the last letter alone is what tells the two apart.
func launcherFlagTakesNextWord(word string, valueFlags map[string]struct{}) bool {
	if _, ok := valueFlags[word]; ok {
		return true
	}
	if len(word) <= 2 || !strings.HasPrefix(word, "-") || strings.HasPrefix(word, "--") {
		return false
	}
	runes := []rune(word)
	_, ok := valueFlags["-"+string(runes[len(runes)-1])]
	return ok
}

func isIn(word string, set map[string]struct{}) bool {
	_, ok := set[word]
	return ok
}

// scriptWordRunsProxsave reports whether one word names our binary. basenameHasProxsaveComponent
// alone is not enough: it answers true for the bare word "proxsave", and a script is full of
// bare words. Requiring a slash keeps it to something that is a PATH.
func scriptWordRunsProxsave(word string) bool {
	if commandTokenMatchesTarget(word) {
		return true
	}
	return strings.Contains(word, "/") && basenameHasProxsaveComponent(word)
}

// splitShellSegments cuts a line at the operators that begin a new command, so each piece
// starts at a command position. Redirections and arguments are left inside their segment: they
// are exactly the places a path is NAMED rather than run.
//
// Braces are NOT separators, because "${BIN}" would then be cut into two words and the
// variable would be unresolvable. A grouping brace is handled where it actually stands, as a
// prefix that consumes one word (scriptCommandPrefixes); the ";" inside "{ cmd; }" already
// separates the commands.
func splitShellSegments(line string) []string {
	return strings.FieldsFunc(line, func(r rune) bool {
		switch r {
		case ';', '|', '&', '(', ')', '`':
			return true
		}
		return false
	})
}

// describeIndirectCronRefs renders one operator-facing item per finding: the cron line
// verbatim, where it lives, and the rule that fired.
//
// It returns the item WITHOUT the list prefix, because the caller owns that: this package
// prints an enumerated finding the way the rest of the CLI already does, a header ending in
// a colon and carrying a count, then one `  - ` item per entry (logConfigUpgradeWarnings in
// main_config_modes.go and logConfiguredZFSPools in restore_zfs.go are the two established
// examples). The bracketed location and the parenthesised reason follow the annotation style
// already used for a resolved path elsewhere in this package.
//
// The caller must print these with a "%s" format, never as the format itself - a crontab
// line may legitimately contain "%", which cron reads as its stdin separator.
func describeIndirectCronRefs(refs []indirectCronRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		where := "crontab"
		if ref.Source != "" {
			where = ref.Source
		}
		out = append(out, fmt.Sprintf("%s  [%s]  (%s)", ref.Line, where, ref.Reason))
	}
	return out
}

// cronRefEditHint names the tool that actually edits the findings at hand. "crontab -e"
// is right for the root crontab and wrong for /etc/crontab and /etc/cron.d, which are
// plain files an editor opens and which crontab(1) will not touch at all; sending an
// operator to the wrong tool over a duplicated backup is how a clear warning turns into
// a support ticket. Both habitats present together get both hints.
func cronRefEditHint(refs []indirectCronRef) string {
	user, system := false, false
	for _, ref := range refs {
		if ref.Source == "" {
			user = true
			continue
		}
		system = true
	}
	switch {
	case user && system:
		return "'crontab -e' for the crontab entries, an editor for the files named above"
	case system:
		return "edit the file named above"
	default:
		return "run 'crontab -e'"
	}
}

// systemCronScheduleAdvisory renders the --daemon-remove notice for a ProxSave schedule
// found under /etc: the findings verbatim, then who owns what. It is a pure renderer
// returning lines the caller prints with "%s", for the two reasons cronRemovalClause is
// one too - the wording is operator-facing and must be testable without a logging
// harness, and a crontab line may legitimately contain "%", which cron reads as its stdin
// separator and fmt would read as a verb.
//
// WHY THIS IS A NOTICE AND NOT A DECISION. applyCronMode does NOT suppress its canonical
// cron line on these findings, and that is the whole design, not an unfinished half of it.
// The three heuristic rules answer "is this named after proxsave", not "does this run a
// proxsave backup": /opt/proxsave/script/prune.sh, a nightly "nice rsync -a /opt/proxsave/
// ..." mirror, and a maintenance script carrying a COMMENTED-OUT proxsave call all match
// while scheduling no backup at all. Withholding the cron line on one of those would leave
// the host with nothing scheduled, silently, and ProxSave cannot edit /etc to repair it.
// F09-06 already ranks the two outcomes: a double schedule is a recoverable annoyance, an
// unscheduled host is silent data loss.
//
// WHAT IT MAY AND MAY NOT CLAIM. It states ONLY what is certain at this point, and the
// restraint is the fix for a defect this text used to carry. It used to say the /etc entry
// stood "alongside the cron line just written at SCHEDULER_TIME" and to instruct the
// operator to remove one of the two. Neither was knowable here. migrateLegacyCronEntries
// is void and has four early returns that write nothing, and one of them is reached
// DETERMINISTICALLY in the case that matters: it re-runs the same `crontab -l` that
// existingWrapperCronFallback just ran, so whenever that read fails no line is written at
// all. The notice then described a duplicate that did not exist and told the operator to
// delete the only schedule the host had left. So: no claim about ProxSave's own line, and
// no instruction to remove anything. The operator is told what was found, that ProxSave
// did not touch it and does not manage it, and where to look to decide - which is the most
// this function can say without asserting something it cannot check.
// systemCronOwnershipNote is the one line of the advisory that states what ProxSave DID
// about what it found, so it is the one line that is a WARNING when the block is logged.
// The findings above it are INFO: one finding printed at WARNING on every line is counted
// three times in the run's "WARNINGS/ERRORS DURING RUN (warnings=N)" recap and two findings
// four times, and that count is what an operator scans. Same shape as the other two #298
// blocks (warnIndirectProxsaveCronOnDaemonInstall, maybeAutoMigrateDaemon).
//
// It repeats the COUNT for the same reason those two do: DEBUG_LEVEL can be set to "warning"
// (internal/cli/args.go), which hides the findings above it. What is lost at that level is
// which lines they were; what survives is that some exist and that ProxSave left them alone.
//
// It is a named function rather than the last element of a slice because the caller has to
// pick it out to give it a different level; indexing the tail would put the whole block one
// off-by-one away from warning about the wrong sentence.
func systemCronOwnershipNote(count int) string {
	return fmt.Sprintf("ProxSave owns the root crontab only and never edits files it did not place, %d line(s) in /etc unchanged", count)
}

// systemCronScheduleFindings renders the findings alone: the header carrying the count, then
// one item per entry. These are the INFO half of the block; systemCronOwnershipNote is the
// other half.
func systemCronScheduleFindings(refs []indirectCronRef) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs)+1)
	out = append(out, fmt.Sprintf("Reverting to cron: %d possible ProxSave cron line(s) under /etc:", len(refs)))
	for _, line := range describeIndirectCronRefs(refs) {
		out = append(out, "  - "+line)
	}
	return out
}

func systemCronScheduleAdvisory(refs []indirectCronRef) []string {
	findings := systemCronScheduleFindings(refs)
	if len(findings) == 0 {
		return nil
	}
	return append(findings, systemCronOwnershipNote(len(refs)))
}

// detectIndirectProxsaveCron reads every habitat that can schedule ProxSave without
// this package owning the line, and returns what it found. It goes through the
// crontabReadLinesFn seam so a test can feed a synthetic crontab, and it returns the
// read error instead of swallowing it: a host with no crontab BINARY at all (nothing
// scheduled, nothing to collide with) must still be allowed to migrate, so "could not
// read" is a caller decision, not a refusal made here.
//
// The two halves are asymmetric, and the asymmetry is the point. From the ROOT CRONTAB
// it takes the indirect shapes only: a line whose command token IS the proxsave binary
// is canonical there, dropCanonicalCronLines owns it and removes it, so reporting it
// would refuse a migration over a line the migration is about to delete. From /etc it
// takes systemCronProxsaveRefs, which adds exactly that one shape, because under /etc
// nothing owns it. dropCanonicalCronLines never reads those files and by the read-only
// rule on systemCronPaths never will, so
//
//	0 2 * * * root /usr/local/bin/proxsave --backup     [/etc/cron.d/proxsave]
//
// is a live backup schedule that survives the migration untouched. Installing the daemon
// on top of it is issue #298 exactly, on the same unattended path, with the same outcome:
// two backups a night and the loser exiting ExitBackupSkipped.
//
// That shape was deliberately withheld from this predicate when it was introduced, on the
// grounds that widening what an unattended upgrade REFUSES on is a decision separate from
// widening what a revert prints. It is included now as a considered choice rather than by
// drift, and it is the safest possible widening: unlike the three heuristic rules, which
// answer "is this named after proxsave" and do fire on a prune script or an rsync mirror,
// this one matches a literal proxsave command token and nothing else. It cannot produce
// the false refusals the heuristics can.
func detectIndirectProxsaveCron(ctx context.Context) ([]indirectCronRef, error) {
	lines, err := crontabReadLinesFn(ctx)
	if err != nil {
		return nil, err
	}
	refs := indirectProxsaveCronRefs(lines, cronProbeReadScripts)
	return append(refs, systemCronProxsaveRefs()...), nil
}

// systemCronPaths are the SYSTEM crontab locations this detector reads. They are vars
// rather than consts purely so a test can point them at a temp tree; nothing writes
// through them.
//
// They are READ-ONLY here, and that asymmetry is deliberate rather than unfinished.
// ProxSave owns the root user's crontab: it writes it on install, rewrites it on
// --daemon-remove and deletes its own lines when enabling the daemon. It does not own
// anything under /etc: those files are hand-placed by an operator or shipped by a
// package, so this code may REPORT what it finds there and must never edit it.
// dropCanonicalCronLines and schedulerTimeFromCronLines are correspondingly untouched
// and still see the user crontab only.
var systemCronPaths = []string{"/etc/crontab", "/etc/cron.d"}

// indirectProxsaveSystemCronRefs applies the same three rules to /etc/crontab and
// /etc/cron.d, which use the SYSTEM crontab format and were invisible to every cron
// helper in this package: cronCommandToken reads field 6, which is the COMMAND in a
// user crontab but the USER in a system one, so a wrapper installed there parsed as a
// username and matched nothing. That is a second, equally silent habitat for exactly
// the schedule issue #298 reported.
//
// Every failure is silent and yields nothing: a missing /etc/cron.d, an unreadable
// file, a directory entry cron itself would ignore. See the false-negative note on
// indirectProxsaveCronRefs for why "cannot tell" must not become "suspicious".
//
// This is the INDIRECT-only view. Nothing in the daemon paths reads it any more -
// detectIndirectProxsaveCron and the revert advisory both go through
// systemCronProxsaveRefs, which adds the direct shape - and it is kept as the named
// boundary between "matched a heuristic" and "is literally the proxsave binary". Its one
// consumer is the test that pins that distinction, which is deliberate: the day someone
// widens the heuristics, the difference between the two views is what makes the blast
// radius visible.
func indirectProxsaveSystemCronRefs() []indirectCronRef {
	return systemCronRefs(scanIndirectOnly)
}

// systemCronProxsaveRefs is indirectProxsaveSystemCronRefs plus the ONE shape that
// detector deliberately drops: a system-cron line whose command token IS the proxsave
// binary.
//
// indirectProxsaveCronRefsWithToken skips those with the comment "canonical
// proxsave entry: not indirect, and not ours to report", and in the USER crontab that
// is exactly right, because dropCanonicalCronLines really does own them and really
// does remove them. Under /etc nothing owns them. dropCanonicalCronLines never reads
// those files and, by the read-only rule stated on systemCronPaths, never will. So
//
//	0 2 * * * root /usr/local/bin/proxsave --backup     [/etc/cron.d/proxsave]
//
// is a live ProxSave backup schedule that no code path in this package can see, remove
// or report - and it is the likeliest way a host ends up scheduled from /etc at all: an
// operator, or a configuration-management tool, moving the line the installer wrote into
// a file they manage. A revert advisory that named a neutrally-written wrapper while
// staying blind to a literal proxsave --backup line two directories away would be worse
// than silence, because the operator would trust it.
//
// This view now feeds the unattended --upgrade refusal too, through
// detectIndirectProxsaveCron. That was withheld at first, on the grounds that widening a
// refusal predicate is a decision separate from widening what a revert prints, and it was
// taken deliberately afterwards rather than by drift. It is the safest widening available:
// the three heuristic rules answer "is this named after proxsave" and can fire on a prune
// script or an rsync mirror, while this shape matches a literal proxsave command token and
// nothing else, so it adds no false refusals at all.
func systemCronProxsaveRefs() []indirectCronRef {
	return systemCronRefs(scanAll)
}

// systemCronRefs is the shared walk over systemCronPaths: /etc/crontab as a file,
// /etc/cron.d as a directory whose entries cron itself would load. It is factored out so
// the indirect-only and the indirect-plus-direct views can never walk different trees or
// apply different skip rules to the same host.
type systemCronScan int

const (
	// scanIndirectOnly is the --upgrade refusal / daemon-install warning view: heuristics
	// only, a canonical proxsave token excluded as "not indirect".
	scanIndirectOnly systemCronScan = iota
	// scanAll adds the canonical token back, because under /etc nothing owns or removes it.
	scanAll
	// scanDirectOnly drops the heuristics AND the script probe, for the SCHEDULER_TIME
	// adoption. See systemCronDirectProxsaveLines for why that view has to be the narrow one.
	scanDirectOnly
)

func systemCronRefs(mode systemCronScan) []indirectCronRef {
	var refs []indirectCronRef
	for _, path := range systemCronPaths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			refs = append(refs, systemCronFileRefs(path, mode)...)
			continue
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !cronDNameIsActive(entry.Name()) {
				continue
			}
			refs = append(refs, systemCronFileRefs(filepath.Join(path, entry.Name()), mode)...)
		}
	}
	return refs
}

// directProxsaveCronRefs reports the lines whose COMMAND TOKEN is the proxsave binary,
// by the same commandTokenMatchesTarget rule the removal uses, so it can never disagree
// with it about what a proxsave cron line is. It is the exact complement of
// indirectProxsaveCronRefsWithToken: that one skips these, this one reports only these,
// and nothing widens the shared matcher.
//
// It has no content probe and no heuristic, so it cannot produce the class of false
// positive the three indirect rules can: the command IS our binary or it is not. That is
// why it is the one rule strong enough to be worth reporting from a habitat nobody can
// clean up afterwards.
func directProxsaveCronRefs(lines []string, commandToken func(string) string) []indirectCronRef {
	var refs []indirectCronRef
	for _, line := range lines {
		token := strings.Trim(commandToken(line), "\"'")
		if token == "" || !commandTokenMatchesTarget(token) {
			continue
		}
		refs = append(refs, indirectCronRef{
			Line:    strings.TrimSpace(line),
			Command: token,
			Reason:  fmt.Sprintf("%q is the proxsave binary; /etc cron lines stay untouched", filepath.Base(token)),
		})
	}
	return refs
}

// systemCronDirectProxsaveLines returns the /etc cron lines whose command IS the proxsave
// binary. It is the SCHEDULER_TIME adoption's view of the system habitat, and nothing else
// reads it.
//
// Direct-only is the whole point, and it is a narrower rule than anything else in this file
// uses. Adopting a run time writes it into backup.env as the host's daily schedule, so it
// may only come from a line this code can read without guessing: "0 5 * * * root
// /usr/local/bin/proxsave --backup" says 05:00 and nothing else. A wrapper's schedule
// belongs to a script we did not write and cannot interpret - it may run the backup, or
// check a mount and give up - so it is reported by the advisories and never adopted here.
//
// Skipping the script probe is the second half of that: this view is reached from the
// install wizard and from --upgrade-config-json, neither of which should be opening
// operator scripts off disk to decide a default.
func systemCronDirectProxsaveLines() []indirectCronRef {
	return systemCronRefs(scanDirectOnly)
}

// cronDNameIsActive mirrors cron's own rule for which /etc/cron.d entries it will
// load: the name must be non-empty and consist only of letters, digits, underscore
// and hyphen. A file with a dot in it (foo.dpkg-dist, bar.bak, .hidden) is IGNORED by
// cron, so reporting it here would refuse a migration over a schedule that never runs.
func cronDNameIsActive(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// systemCronFileRefs reads one system-crontab file and returns its ProxSave references,
// tagged with the file they came from. The size cap is the same one the script probe
// uses: a crontab file is a few KiB, and a caller that pointed this at something enormous
// has misconfigured the host rather than scheduled a backup.
//
// includeDirect adds the lines whose command token IS the binary (see
// directProxsaveCronRefs). They are emitted FIRST, ahead of the heuristic findings,
// because that is the order an operator should read them in: the certain fact before the
// inferred ones. Within each group the crontab order of the file is preserved.
//
// The file is read ONCE for both rule sets. Two reads would be two chances to disagree
// about a file another process may be editing, and a revert that reported a line from one
// snapshot and a reason from another would be worse than either.
func systemCronFileRefs(path string, mode systemCronScan) []indirectCronRef {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCronWrapperProbeBytes {
		return nil
	}
	data, err := safefs.ReadFileUnderRoot(resolveSystemCronPath(path))
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return nil
	}
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.TrimSpace(normalized) == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(normalized, "\n"), "\n")
	var refs []indirectCronRef
	if mode != scanIndirectOnly {
		refs = append(refs, directProxsaveCronRefs(lines, systemCronCommandToken)...)
	}
	if mode != scanDirectOnly {
		refs = append(refs, indirectProxsaveCronRefsWithToken(
			lines,
			cronProbeReadScripts,
			systemCronCommandToken,
		)...)
	}
	for i := range refs {
		refs[i].Source = path
	}
	return refs
}

// resolveSystemCronPath returns the path systemCronFileRefs should actually open, which
// is not always the one it was handed: a symlinked /etc/cron.d entry has to be resolved
// first or it is silently invisible.
//
// safefs.OpenFileUnderRoot roots at the parent directory and REFUSES a final component
// that is an absolute symlink - a deliberate guarantee everywhere else in this codebase,
// and the right default. Here it produced a blind spot instead. os.Stat above follows the
// link, so the entry passes the regular-file and size gates; the read then fails, the
// error is swallowed by the same fail-quiet rule that covers a missing /etc/cron.d, and
// the file is never scanned. Cron loads that entry regardless, so the schedule is live and
// the detector is the only one that cannot see it - and a config-management tool is both
// the named cause of ProxSave ending up scheduled from /etc and the thing most likely to
// place its files there as symlinks into a package tree.
//
// Resolving the link and rooting at the TARGET's parent keeps the confinement rather than
// dropping it: the read still goes through os.Root, just anchored where the bytes really
// live. A path that is not a symlink, or whose chain cannot be resolved, is returned
// unchanged so the caller's existing guards decide - never silently widened.
func resolveSystemCronPath(path string) string {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return path
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	// The target must still be an ordinary, small text file: following a link is not a
	// reason to relax any gate the direct path had to pass.
	target, err := os.Stat(resolved)
	if err != nil || !target.Mode().IsRegular() || target.Size() > maxCronWrapperProbeBytes {
		return path
	}
	return resolved
}

// systemCronCommandToken is cronCommandToken for the SYSTEM crontab format, where a
// USER field sits between the schedule and the command:
//
//	MM HH DOM MON DOW USER COMMAND    ->  field 7 is the command
//	@daily        USER COMMAND        ->  field 3 is the command
//
// Comments and environment assignments are rejected exactly as in the user format, so
// a line like "MAILTO=root" is never mistaken for a command.
func systemCronCommandToken(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 || looksLikeEnvAssignment(fields[0]) {
		return ""
	}
	if strings.HasPrefix(fields[0], "@") {
		if len(fields) >= 3 {
			return fields[2]
		}
		return ""
	}
	if len(fields) <= 6 {
		return ""
	}
	return fields[6]
}

// warnIndirectProxsaveCronOnDaemonInstall reports, without touching anything, every
// entry that still schedules ProxSave indirectly after the canonical cron line has
// been removed and the daemon unit installed.
//
// It sits ONLY on the paths that INSTALL the daemon and that the OPERATOR asked for
// explicitly - applyDaemonMode (--daemon-setup, and the dashboard's install action)
// and the install/reinstall reconcile that picked daemon mode. On those, refusing
// would leave an operator who legitimately wants both no way to enable the daemon at
// all, so the policy is warn-and-proceed. The unattended --upgrade retrofit refuses
// instead; see maybeAutoMigrateDaemon.
//
// It deliberately does NOT live inside removeCanonicalCronEntry, even though that is
// where the crontab lines are already in hand and it would have cost no extra read.
// applyCronMode calls that same function on the --daemon-remove path, where no daemon
// is being installed and this text ("it will keep firing alongside the daemon") would
// be flatly untrue. A warning that is right on three paths and wrong on the fourth is
// worse than one extra `crontab -l`.
// It returns how many entries it found, so a caller whose only channel is a rendered screen
// can say the same thing the log line says (cronRemovalOutcome.UnmanagedSchedules).
func warnIndirectProxsaveCronOnDaemonInstall(ctx context.Context, bootstrap *logging.BootstrapLogger) int {
	refs, err := detectIndirectProxsaveCron(ctx)
	if err != nil || len(refs) == 0 {
		return 0
	}
	// One problem, one WARNING. The header and the findings are INFO: three warning lines for
	// a single fact read as three problems in the run's "WARNINGS/ERRORS DURING RUN
	// (warnings=N)" recap, and the count is what an operator scans. Telegram sets the same
	// shape - one line at the level that matters, the rest below it.
	//
	// The WARNING therefore repeats the count and stands on its own, because DEBUG_LEVEL can
	// be set to "warning" (internal/cli/args.go), which hides the two INFO lines. What is lost
	// at that level is which cron line it was; what survives is that the host now runs the
	// backup twice. Naming the file here instead would not survive a second finding.
	logBootstrapInfo(bootstrap, "%d unmanaged cron line(s) still schedule ProxSave:", len(refs))
	for _, line := range describeIndirectCronRefs(refs) {
		logBootstrapInfo(bootstrap, "  - %s", line)
	}
	logBootstrapWarning(bootstrap, "%d unmanaged cron line(s) NOT removed, so they keep firing alongside the daemon: this can cause problems, exit %d.", len(refs), types.ExitBackupSkipped.Int())
	return len(refs)
}
