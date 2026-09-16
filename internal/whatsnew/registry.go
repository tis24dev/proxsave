package whatsnew

import (
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Fixed Screen 0 section headers. The template is fixed: these headers are always the same
// across every release; a Note never carries its own title, only the content under them.
const (
	headerChanges = "What changed in this version:"
	headerActions = "What you need to do now:"
)

// Note is one release's what's-new entry: a semver key, its highlight lines, and an optional
// list of actions the user must still take. All copy is plain terse English (no em-dash,
// en-dash, or emoji); the Pager sanitizes and wraps it, so it is never hand-styled here and
// carries no glyphs. The two section HEADERS are fixed (see the consts above), never
// per-release.
type Note struct {
	Version string
	Lines   []string // highlights, one plain "- " bullet each (under headerChanges)
	Actions []string // optional user TODOs, one "- " bullet each (under headerActions)
}

// HOW TO ADD A RELEASE ENTRY (Screen 0 population guide)
//
// Screen 0 has a FIXED template with FIXED section headers (see headerChanges/headerActions
// and RenderBody). Never invent a per-release title; only fill the two content slices:
//
//	Lines   -> shown under "What changed in this version:"
//	Actions -> shown under "What you need to do now:" (the whole section is omitted when empty)
//
// Append one Note per FINAL release only (no beta/rc: finalize() maps every build of a line
// onto its final key, so betas inherit the final's notes). Keep this slice append-only and
// strictly ascending by semver key; the release CI (release-notes-guard) BLOCKS any release
// whose line has no well-formed entry here, PRERELEASES INCLUDED.
//
// Add the entry before the FIRST beta of a line, not in the final's release PR. A beta that
// ships without it renders the empty state and still writes last_seen on continue, and that
// flag finalizes to the same key as the final, so IsUnseen is false when the final lands and
// everyone who came through a beta misses the notes for good.
//
// Lines: ONLY genuinely new user features, or concrete changes to how the user uses the
// tool. State the capability from the user's point of view, not the technical detail (e.g.
// "backup monitoring is new", not "monitoring decoupled from Telegram"). One change per line,
// 1 to 8 terse bullets. NOT: this what's-new screen itself or other meta, internal refactors
// or security hardening, UX polish, or anything that already existed in an earlier release.
//
// Actions: only a REAL step the user must take. "On by default" does NOT mean nothing to do:
// if a default-on feature still needs configuring, that configuration goes here. Always point
// to the DASHBOARD path, never a CLI command when a dashboard path exists, and VERIFY every
// menu label and navigation step against the current code before writing it (labels drift; a
// wrong or nonexistent label is a bug). Omit Actions entirely when there is genuinely nothing
// the user must do.
//
// Style (enforced by TestRegistryWellFormed, blocking): pure ASCII (no em-dash, en-dash, or
// emoji), no "placeholder", each line at most 120 chars, non-blank, English, terse.
var notes = []Note{
	{
		Version: "0.30.0",
		Lines: []string{
			"New interactive dashboard to run backups, restores, checks, and setup",
			"Scheduled backups now run from a resident daemon (replaces cron, reversible), on by default",
			"Backup monitoring (healthchecks) is on by default and alerts you if a backup stops running",
			"Host backup mode: back up a Proxmox host from an LXC or HA-LXC appliance",
			"PBS datastore file scanning is off by default, including on configs written before the setting existed",
		},
		Actions: []string{
			"In the dashboard, open Healthchecks for your monitoring portal address and sign-in details",
			"If your backups included PBS datastore file scans, set PXAR_SCAN_ENABLE=true in backup.env to keep them",
		},
	},
	{
		Version: "0.31.0",
		Lines: []string{
			"Retention recognises this host's own archives after its name changes, so archives it stopped pruning are pruned again",
			"Backup counts in notifications report the archives this host manages, not every archive in a shared location",
		},
	},
	{
		Version: "0.32.0",
		Lines: []string{
			"Switching to the daemon detects a backup already scheduled by your own cron wrapper, and does not add a second one",
			"That detection also reads /etc/crontab, /etc/cron.d and the /etc/cron.hourly, .daily, .weekly and .monthly directories",
			"Turning the daemon off always restores a working cron entry, and reports any entry it could not remove",
			"SCHEDULER_MODE alone decides the engine; an upgrade leaves a recorded engine as it stands and DAEMON_OPT_OUT is gone",
			"install.sh accepts --upgrade, so the new binary finalizes the upgrade instead of the one being replaced",
		},
		Actions: []string{
			"If an upgrade reports a cron wrapper of yours, remove the duplicate schedule from your crontab and upgrade again",
		},
	},
	{
		Version: "0.33.0",
		Lines: []string{
			"Run your own scripts around each backup: PERSONAL_SCRIPT_PRE_RUN and PERSONAL_SCRIPT_POST_RUN in backup.env",
			"The notification issue list names the failed operation (upload, retention, listing), one entry per fault",
			"Secondary storage reports unreadable archives and a location that stopped answering instead of skipping them silently",
			"A backup that hits those secondary faults ends with exit 1 instead of reporting success",
			"Log messages no longer repeat the level word the level column already prints",
			"A cloud delete killed mid-transfer reports the kill, not rclone's last progress line",
			"Personal scripts must live on a root-owned, non-writable path chain; unsafe paths are refused at daemon start",
			"A daemon shutdown no longer waits for a running personal script; the script keeps its own 10-minute budget",
		},
		Actions: []string{
			"If runs start ending with exit 1 naming unreadable secondary archives, repair or remove those files",
			"Update any log-parsing script of yours that matches WARNING: or ERROR: inside the message text",
			"If the daemon warns a personal script was disabled, move it to a root-owned directory and chmod it 0755 or stricter",
		},
	},
	{
		Version: "0.34.0",
		Lines: []string{
			"Staged PVE restore really applies datacenter.cfg, vzdump.cron, storage definitions and guest configs",
			"A guest missing from the node is registered from its restored config file; rejected keys no longer fail it",
			"Copy failures of critical files and interrupted collections now warn in the log instead of hiding at debug",
			"Text cleanup in backups only touches CRLF line endings in plain text; UTF-16 and binary files stay untouched",
			"The PVE cluster database is captured as a consistent snapshot, including changes not yet checkpointed",
			"Cloud retention no longer stalls the run when rclone hangs; a missing cloud log no longer skips log cleanup",
			"A failed notification promotes an otherwise clean non-dry-run run to exit 1; --log-level only quiets the console",
			"Starting --daemon twice is refused, so the running daemon stays discoverable by backup handoffs",
		},
		Actions: []string{
			"After a staged PVE restore, check the restore log: applied items say pmxcfs or pvesh, failures stay warnings",
			"If a monitoring script gates on exit codes, account for failed notifications promoting clean non-dry runs to exit 1",
		},
	},
	{
		Version: "0.35.0",
		Lines: []string{
			"PVE cluster database snapshots no longer fail before sqlite3 can run",
			"Personal pre- and post-backup scripts can run below user-owned homes with an administrator trust warning",
			"Daemon status compares loaded and current personal scripts, with paths, ownership, modes, refusal reasons, and drift",
		},
	},
	{
		Version: "0.36.0",
		Lines: []string{
			"A restore aborted mid-apply stops instead of writing more PVE configuration cluster-wide",
			"Restores and notification emails no longer break when your ssh session forwards a locale the node does not have",
			"A guest config the API rejects for a bad value is no longer written into the cluster anyway",
			"A running guest keeps every setting the API accepts instead of losing its whole configuration",
			"Storage definitions apply where they used to fail: already matching, or carrying mostly create-only keys",
			"Daemon status says the configuration could not be read instead of blaming the daemon and asking for a restart",
			"The personal-script warning names the kernel setting that trust decision depends on",
			"--upgrade finalizes with the newly installed binary, and no longer waits on the notes screen when unattended",
		},
		Actions: []string{
			"If you relied on --upgrade y showing the release notes, run proxsave --show-whatsnew after the upgrade",
			"The upgrade TO 0.36.0 still finalizes from the old binary; the new path starts with the upgrade after it",
			"If a personal script lives below a user-owned home, check the fs.protected_hardlinks value now shown at startup",
			"Remove --dry-run from any script that runs proxsave --upgrade: the combination is now refused instead of upgrading",
		},
	},
	{
		Version: "0.37.0",
		Lines: []string{
			"Every run checks backup.env and reports a variable that is set twice, absent, or not read by ProxSave",
			"A value you wrote is no longer discarded in silence when the same variable is set again further down the file",
			"Daemon status says why a personal script reads as not configured: absent from backup.env, empty, or overwritten",
			"The configuration check names a legacy variable and says which of the old and the new name is the one in use",
			"A variable ProxSave reads is no longer called unknown and ignored, so nothing tells you to delete a working setting",
			"A restore counts a storage definition as applied only when it can show it: otherwise it says failed or unknown",
			"A restore says when it wrote a guest configuration while Proxmox had that guest marked busy",
			"An aborted restore names the VMID it may have left reserved and locked on the cluster",
		},
		Actions: []string{
			"If a run reports a variable set twice, the warning names the line that wins: delete the other one",
			"If a run reports absent variables, merge them from the dashboard: Maintenance > Upgrade > Check config",
			"If a run reports a legacy variable set alongside its current name, delete the one the line says has no effect",
			"If an earlier version said a variable is not known and is ignored, check it again before deleting that line",
		},
	},
	{
		Version: "0.38.0",
		Lines: []string{
			"A personal script the daemon refuses to start now says so, instead of the run looking as if it ran",
			"A debug run names the marker that decided PVE, PBS or dual, and every marker it checked before that one",
			"PBS ACME accounts are backed up and restored as a directory, and BACKUP_PBS_ACME_ACCOUNTS=false really excludes them",
			"The healthchecks screen accepts HEALTHCHECK_ALIVE_ID: an id alone no longer reads as not configured",
			"A misspelled variable in backup.env is called out as a probable typo, with the name it was meant to be",
			"proxsave --upgrade-config reports a variable set twice instead of answering that the file is up to date",
			"A variable the upgrade adds back is written inside its own section, not above the header that documents it",
			"SKIP_PERMISSION_CHECK now takes effect: until now the variable was read and then ignored by the backup",
		},
		Actions: []string{
			"If the daemon says a personal script was not started for a run, the reason names what changed about the file",
			"To clear a personal-script trust warning without moving the script, see Clearing a READY WITH WARNING in DAEMON.md",
			"A PBS restore mirrors the ACME accounts: an account on the node that the backup does not carry is removed",
			"Archives taken before this fix hold no accounts directory, and restoring one leaves the node accounts untouched",
			"A PBS archive whose acme/accounts holds anything but plain account files is refused, and no live account is touched",
			"If a run names a possible typo in backup.env, fix that line: nothing written on it is applied today",
			"A variable set twice still has to be cleaned by hand, and --upgrade-config names the lines to delete",
			"Several variables missing from the same section are each written back under their own comment, not stacked",
			"A debug run under SYSTEM_ROOT_PREFIX reports the mounted host only: command probes are listed as skipped",
			"The detection marker table now stats proxmox-backup-manager in /usr/sbin too, where PBS 4 installs it",
			"A self-mode host set up with an alive check id needs no full URL: the screen and the run now agree on it",
			"If SKIP_PERMISSION_CHECK=true is left over from a test, clear it: the next run really skips that check",
		},
	},
	{
		Version: "0.39.0",
		Lines: []string{
			"A PVE host with leftover proxmox-backup files is no longer taken for a PBS server, and its backup runs again",
			"A run says when a product left files behind without being installed, and names the file it found",
			"Restore reads the host type from the same check the backup uses, instead of keeping a weaker one of its own",
			"On a PVE plus PBS host, one role failing no longer discards the other role and the shared system payload",
			"A slow pveversion no longer costs the run its version number: the version is read from the package instead",
		},
		Actions: []string{
			"If a run used to fail with failed to get PBS version on a host without PBS, it now completes as a PVE backup",
			"The leftover directories are still reported: remove them, or install PBS, only if you want the notice to stop",
			"An archive taken before this fix is labelled dual, so restoring it on the corrected host reports partial match",
			"A PVE host with leftover PBS files no longer offers PBS restore categories and no longer stops PBS services",
			"A dual backup that lost one role exits with the warning code and names the missing role in its manifest",
		},
	},
}

// LookupNotes returns the notes for versions in the half-open range (from, to], ascending by
// parsed semver. Both bounds are FINALIZED (prerelease/metadata stripped) before filtering,
// so a prerelease build sees its final line's notes: with to=0.30.0-beta6 the upper bound
// becomes 0.30.0 and the 0.30.0 entry is IN range (notes are keyed to FINAL releases, so every
// build of a line maps to that line's final notes). The lower bound is exclusive (an equal,
// finalized, from never re-shows a version the user already saw on any build of that line) and
// the upper bound is inclusive. An unparseable bound, an inverted range, or simply no matching
// entry yields an empty slice, never an error and never the whole registry (fail toward silence).
func LookupNotes(from, to string) []Note {
	lo, errLo := semver.NewVersion(from)
	hi, errHi := semver.NewVersion(to)
	if errLo != nil || errHi != nil {
		return nil
	}
	lo, hi = finalize(lo), finalize(hi)
	var out []Note
	for _, n := range notes {
		v, err := semver.NewVersion(n.Version)
		if err != nil {
			continue // a malformed registry key is skipped, never surfaced as show-all
		}
		if v.GreaterThan(lo) && !v.GreaterThan(hi) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := semver.NewVersion(out[i].Version)
		b, _ := semver.NewVersion(out[j].Version)
		return a.LessThan(b)
	})
	return out
}

// RenderBody builds the plain, \n-separated Pager body. It is NOT hand-styled or colored
// (the Pager sanitizes and wraps it): the version header "ProxSave <current>", a blank line,
// the fixed changes header, then either the UI-SPEC empty-state line (so continue is always
// reachable and the flag can clear) or, per note, each highlight as a plain ASCII "- " bullet
// followed, when the note has actions, by a blank line, the fixed actions header, and its
// "- " bullets. Section headers are the fixed consts, never per-release. No theme import, no
// glyph constant; the pure state tier stays stdlib-plus-semver only.
func RenderBody(current string, notes []Note) string {
	var b strings.Builder
	b.WriteString("ProxSave " + current + "\n\n")
	b.WriteString(headerChanges + "\n")
	if len(notes) == 0 {
		b.WriteString("This version has updates. See the changelog for details.\n")
		return b.String()
	}
	for i, n := range notes {
		if i > 0 {
			b.WriteString("\n") // blank line between consecutive notes in a multi-version catch-up
		}
		for _, line := range n.Lines {
			b.WriteString("- " + line + "\n")
		}
		if len(n.Actions) > 0 {
			b.WriteString("\n" + headerActions + "\n")
			for _, line := range n.Actions {
				b.WriteString("- " + line + "\n")
			}
		}
	}
	return b.String()
}
