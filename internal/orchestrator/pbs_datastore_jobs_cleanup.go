// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// pbsDatastoreJobFiles are the job configurations PBS removes together with a
// datastore (datastore remove, keep-job-configs false): every section whose
// "store" is that datastore. "remote-store" names a datastore of the remote, not
// one of this PBS.
var pbsDatastoreJobFiles = []string{
	"/etc/proxmox-backup/prune.cfg",
	"/etc/proxmox-backup/verification.cfg",
	"/etc/proxmox-backup/sync.cfg",
	"/etc/proxmox-backup/tape-job.cfg",
}

// pbsDatastoreCleanup is what was removed with one datastore that the file
// fallback took out of datastore.cfg.
type pbsDatastoreCleanup struct {
	// jobs are "<type>:<id>" with the file each was removed from.
	jobs []pbsRemovedJob
	// acl is the number of acl.cfg entries removed.
	acl int
	// err is the first file that could not be read or written while it held,
	// or could hold, something of this datastore; errFile is that file.
	err     error
	errFile string
}

type pbsRemovedJob struct {
	id   string
	file string
}

func (c *pbsDatastoreCleanup) fail(file string, err error) {
	if c.err == nil {
		c.err, c.errFile = err, file
	}
}

// removePBSDatastoreJobsAndACL removes from the job configurations and from
// acl.cfg what refers to the given datastores, the way PBS does when it removes a
// datastore, and reports it per datastore.
func removePBSDatastoreJobsAndACL(names []string) map[string]*pbsDatastoreCleanup {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]*pbsDatastoreCleanup, len(names))
	for _, name := range names {
		out[name] = &pbsDatastoreCleanup{}
	}
	for _, path := range pbsDatastoreJobFiles {
		removePBSDatastoreJobsFromFile(path, out)
	}
	// PBS removes the /datastore/<name> permission node, and everything below
	// it, with the datastore.
	removePBSDatastoreACL(pbsACLCfgPath, out)
	return out
}

// pbsCfgSection is one section of a PBS section config, kept as its raw lines.
type pbsCfgSection struct {
	header string
	lines  []string
	store  string
}

// splitPBSCfgSections splits a section config into the lines before the first
// section and the sections, each with its raw lines (the blank and comment lines
// after it included), so that what is kept is written back unchanged.
func splitPBSCfgSections(content string) (preamble []string, sections []pbsCfgSection) {
	var current *pbsCfgSection
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		indented := line != "" && (line[0] == ' ' || line[0] == '\t')
		if !indented && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			if typ, id, ok := parseSectionHeader(trimmed); ok {
				sections = append(sections, pbsCfgSection{header: typ + ":" + id})
				current = &sections[len(sections)-1]
				current.lines = append(current.lines, line)
				continue
			}
		}
		if current == nil {
			preamble = append(preamble, line)
			continue
		}
		current.lines = append(current.lines, line)
		if fields := strings.Fields(trimmed); indented && len(fields) == 2 && fields[0] == "store" {
			current.store = fields[1]
		}
	}
	return preamble, sections
}

func removePBSDatastoreJobsFromFile(path string, out map[string]*pbsDatastoreCleanup) {
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		for _, c := range out {
			c.fail(path, err)
		}
		return
	}
	preamble, sections := splitPBSCfgSections(string(data))
	kept := preamble
	var dropped []pbsCfgSection
	for _, s := range sections {
		if _, gone := out[s.store]; gone {
			dropped = append(dropped, s)
			continue
		}
		kept = append(kept, s.lines...)
	}
	if len(dropped) == 0 {
		return
	}
	if err := writePBSConfigKeepingMode(path, kept); err != nil {
		for _, s := range dropped {
			out[s.store].fail(path, err)
		}
		return
	}
	for _, s := range dropped {
		out[s.store].jobs = append(out[s.store].jobs, pbsRemovedJob{id: s.header, file: path})
	}
}

// removePBSDatastoreACL removes the acl.cfg entries on /datastore/<name> and on
// the paths below it. An entry is "acl:<propagate>:<path>:<users>:<roles>".
func removePBSDatastoreACL(path string, out map[string]*pbsDatastoreCleanup) {
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		for _, c := range out {
			c.fail(path, err)
		}
		return
	}
	var kept []string
	removed := map[string]int{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if name := pbsACLDatastore(line, out); name != "" {
			removed[name]++
			continue
		}
		kept = append(kept, line)
	}
	if len(removed) == 0 {
		return
	}
	if err := writePBSConfigKeepingMode(path, kept); err != nil {
		for name := range removed {
			out[name].fail(path, err)
		}
		return
	}
	for name, n := range removed {
		out[name].acl = n
	}
}

// pbsACLDatastore returns the datastore of out that an acl.cfg line grants
// permissions on, or "".
func pbsACLDatastore(line string, out map[string]*pbsDatastoreCleanup) string {
	parts := strings.SplitN(strings.TrimSpace(line), ":", 5)
	if len(parts) != 5 || parts[0] != "acl" {
		return ""
	}
	rest, ok := strings.CutPrefix(parts[2], "/datastore/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	if _, gone := out[name]; gone {
		return name
	}
	return ""
}

// writePBSConfigKeepingMode writes a PBS config file back with the mode it has;
// writeFileAtomic keeps its owner.
func writePBSConfigKeepingMode(path string, lines []string) error {
	perm := os.FileMode(0o640)
	if info, err := restoreFS.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	content := strings.TrimRight(strings.Join(lines, "\n"), "\n \t")
	if content != "" {
		content += "\n"
	}
	if err := writeFileAtomic(path, []byte(content), perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
