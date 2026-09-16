package environment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/safeexec"
	"github.com/tis24dev/proxsave/internal/types"
)

const (
	defaultPVEVersionFile = "/etc/pve-manager/version"
	defaultPVELegacyFile  = "/etc/pve/pve.version"
	defaultPBSVersionFile = "/etc/proxmox-backup/version"
)

var (
	pveVersionFile = defaultPVEVersionFile
	pveLegacyFile  = defaultPVELegacyFile
	pbsVersionFile = defaultPBSVersionFile

	// dpkgStatusFile is the Debian package database. Under a mounted host prefix it
	// is the persistent, authoritative record of what is installed AND its version,
	// unlike the pmxcfs-backed version files which vanish when the /etc/pve bind is
	// not mounted. It is the reliable offline version source for both PVE and PBS.
	dpkgStatusFile = "/var/lib/dpkg/status"

	// pveClusterDB is the pmxcfs SQLite backing store. It exists on every PVE host
	// (clustered or standalone), lives on the persistent root filesystem, and
	// survives with no pmxcfs FUSE bind, so it identifies a mounted PVE host even
	// when /etc/pve is an empty mountpoint. Nothing but PVE creates it.
	pveClusterDB = "/var/lib/pve-cluster/config.db"

	// pveBinaryCandidates and pbsBinaryCandidates are product-specific binaries
	// installed under /usr, present whenever the mount carries /usr even if /etc and
	// /var are excluded. They are only stat-probed, never executed (executing a host
	// binary from inside the backup appliance would answer for the appliance).
	pveBinaryCandidates = []string{"/usr/bin/pmxcfs", "/usr/bin/pveversion", "/usr/bin/pvesh", "/usr/sbin/qm", "/usr/sbin/pct"}
	// PBS server binaries only (proxmox-backup-proxy/manager); the client
	// (proxmox-backup-client) ships on PVE hosts too, so it is excluded to avoid a
	// false PBS positive on a PVE-only host.
	pbsBinaryCandidates = []string{"/usr/sbin/proxmox-backup-proxy", "/usr/bin/proxmox-backup-manager", "/usr/sbin/proxmox-backup-manager"}

	// commandBinaryCandidates are the paths the two binaries the command probes run can
	// live at. The marker table reports these with their executable bit, which the offline
	// candidates above are never asked for, so the list is its own - but it has to stay a
	// SUPERSET of what those lists hold for the same two binaries, or the table reports a
	// binary as absent while the offline section finds it. It did: the list this replaces
	// named /usr/bin/proxmox-backup-manager only, and PBS 4.2.5 installs it in /usr/sbin,
	// so a real PBS host read "exists: NO" for a binary it has.
	// TestCommandBinaryCandidatesCoverTheOfflineOnes fails if the two ever diverge again.
	commandBinaryCandidates = []string{
		"/usr/bin/pveversion",
		"/usr/sbin/pveversion",
		"/usr/bin/proxmox-backup-manager",
		"/usr/sbin/proxmox-backup-manager",
	}

	// Package data directories, on-disk and product-specific.
	pveShareDir = "/usr/share/pve-manager"
	pbsShareDir = "/usr/share/proxmox-backup"

	additionalPaths = []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin"}

	pveDirCandidates = []string{
		"/etc/pve",
		"/var/lib/pve-cluster",
	}

	pbsDirCandidates = []string{
		"/etc/proxmox-backup",
		"/var/lib/proxmox-backup",
	}

	pveSourceFiles = []string{
		"/etc/apt/sources.list.d/proxmox.list",
	}

	pbsSourceFiles = []string{
		"/etc/apt/sources.list.d/pbs.list",
		"/etc/apt/sources.list.d/proxmox.list",
	}

	// Tokens a repository file must contain to count as that product's source.
	// Named here so the marker snapshot reports the same test detection runs.
	pveSourceTokens = []string{"pve", "pve-enterprise"}
	pbsSourceTokens = []string{"pbs", "proxmox-backup"}

	lookPathFunc = exec.LookPath

	readFileFunc  = os.ReadFile
	statFunc      = os.Stat
	mkdirAllFunc  = os.MkdirAll
	writeFileFunc = os.WriteFile
	getwdFunc     = os.Getwd

	userCurrentFunc = user.Current
	timeNowFunc     = time.Now

	commandTimeout = 5 * time.Second
	debugBaseDir   = "/tmp"

	runCommandFunc = runCommand

	// rootPrefix re-anchors the hardcoded detection paths under a mounted host
	// filesystem (SYSTEM_ROOT_PREFIX). Empty means detect against the real root,
	// the historical behavior. It is a package-level seam, like statFunc and
	// readFileFunc above.
	//
	// EXACTLY TWO functions set it, each for the duration of one call and each
	// restoring the previous value in a defer: DetectWith and MarkerSnapshot. Both
	// are called from the process bootstrap, which is sequential and runs before any
	// goroutine of this program exists, so the two never overlap. That is the whole
	// safety argument - there is no lock - and it holds only while the call sites stay
	// where they are.
	//
	// A third setter, or either of these two reached from a goroutine, breaks it: one
	// call would inspect paths under another call's prefix and return a snapshot or a
	// verdict for the wrong machine. This is not hypothetical bookkeeping. MarkerSnapshot
	// became the second setter without anyone noticing that this comment named only the
	// first, which is why TestRootPrefixHasExactlyTwoSetters now reads the package source
	// and fails when a third assignment appears.
	rootPrefix string
)

// DetectProxmoxType detects whether the system is running Proxmox VE or Proxmox Backup Server
func DetectProxmoxType() types.ProxmoxType {
	info, _ := detectEnvironmentInfo()
	return info.Type
}

// GetVersion returns the version string of the detected Proxmox system
func GetVersion(pType types.ProxmoxType) (string, error) {
	extendPath()

	switch pType {
	case types.ProxmoxVE:
		if version, ok, _ := detectPVE(nil); ok && version != "" && version != "unknown" {
			return version, nil
		}
		return "", fmt.Errorf("unable to determine Proxmox VE version")
	case types.ProxmoxBS:
		if version, ok, _ := detectPBS(nil); ok && version != "" && version != "unknown" {
			return version, nil
		}
		return "", fmt.Errorf("unable to determine Proxmox Backup Server version")
	case types.ProxmoxDual:
		info, err := detectEnvironmentInfo()
		if err != nil {
			return "", err
		}
		if info.Type != types.ProxmoxDual || info.Version == "" || info.Version == "unknown" {
			return "", fmt.Errorf("unable to determine dual Proxmox versions")
		}
		return info.Version, nil
	default:
		return "", fmt.Errorf("unknown proxmox type: %s", pType)
	}
}

// EnvironmentInfo holds information about the current Proxmox environment
type EnvironmentInfo struct {
	Type       types.ProxmoxType
	Version    string
	PVEVersion string
	PBSVersion string

	// PVESource and PBSSource name the marker that ended each product ladder, and
	// Steps is the whole ladder that led there. They are provenance: the verdict is
	// decided by the ladder, not by reading these back.
	PVESource string
	PBSSource string
	Steps     []DetectionStep

	// PVEResidual and PBSResidual name a marker that was found but does NOT prove the
	// product is installed: a directory the package never owned, or an empty version
	// file. They are empty when the product is installed, and empty when nothing at all
	// was found. A non-empty residual with an absent product is the whole explanation
	// for a verdict an operator did not expect (issue #315), so it is reported rather
	// than discarded.
	PVEResidual string
	PBSResidual string
}

// Product labels used by the detection trace.
const (
	productPVE = "PVE"
	productPBS = "PBS"
)

// DetectionStep is one rung of a product's detection ladder: the marker consulted,
// the exact target it looked at (already re-anchored under any host prefix), and the
// answer it gave. A recorded run reads as "these markers missed, this one decided the
// type".
type DetectionStep struct {
	Product  string // productPVE or productPBS
	Marker   string // command, version-file, dpkg, cluster-db, binary, share-dir, directory
	Target   string // path(s) or command consulted
	Hit      bool
	Skipped  bool // probe not run at all (command probes under a host prefix)
	Residual bool // marker found, but it does not prove the product is installed
	Version  string
	Note     string // why a probe was skipped, or what the residue means
}

// String renders the step as the single line a debug log carries.
func (s DetectionStep) String() string {
	outcome := "miss"
	switch {
	case s.Skipped:
		outcome = "skipped"
	case s.Hit && s.Version != "" && !strings.EqualFold(s.Version, "unknown"):
		outcome = "HIT, version " + s.Version
	case s.Hit:
		outcome = "HIT, no version"
	case s.Residual:
		outcome = "residue, does not prove an install"
	}
	line := fmt.Sprintf("%s %s (%s): %s", s.Product, s.Marker, s.Target, outcome)
	if s.Note != "" {
		line += " - " + s.Note
	}
	return line
}

// detectionTrace accumulates the ladder as it runs. A nil trace is a no-op, so a
// caller that only wants the verdict passes nil and pays nothing.
type detectionTrace struct {
	steps []DetectionStep
}

func (t *detectionTrace) add(step DetectionStep) {
	if t == nil {
		return
	}
	t.steps = append(t.steps, step)
}

func (t *detectionTrace) hit(product, marker, target, version string) {
	t.add(DetectionStep{Product: product, Marker: marker, Target: target, Hit: true, Version: version})
}

func (t *detectionTrace) miss(product, marker, target string) {
	t.add(DetectionStep{Product: product, Marker: marker, Target: target})
}

// residue records a marker that was found and deliberately does not decide. It is
// not a miss: a miss says the host has nothing there, and this says the host has
// something there that does not answer the question. Both readings matter to whoever
// is holding an unexpected verdict, so they are recorded apart.
func (t *detectionTrace) residue(product, marker, target, note string) {
	t.add(DetectionStep{Product: product, Marker: marker, Target: target, Residual: true, Note: note})
}

func (t *detectionTrace) skip(product, marker, target, note string) {
	t.add(DetectionStep{Product: product, Marker: marker, Target: target, Skipped: true, Note: note})
}

// decidedBy names the marker that ended the ladder for product; empty when none did.
func (t *detectionTrace) decidedBy(product string) string {
	if t == nil {
		return ""
	}
	for _, step := range t.steps {
		if step.Product == product && step.Hit {
			return fmt.Sprintf("%s (%s)", step.Marker, step.Target)
		}
	}
	return ""
}

// targetList renders a candidate list for a trace line, each entry re-anchored under
// the active prefix so the line names the path actually consulted.
func targetList(paths ...string) string {
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		resolved = append(resolved, resolveUnderPrefix(path))
	}
	return strings.Join(resolved, ", ")
}

// Detect detects the Proxmox environment and returns detailed information
func Detect() (*EnvironmentInfo, error) {
	return DetectWith(DetectOptions{})
}

// DetectOptions tunes detection. RootPrefix, when set, points detection at a
// Proxmox host filesystem mounted read-only under that prefix (SYSTEM_ROOT_PREFIX),
// so ProxSave running inside an HA-LXC backup appliance detects the host rather
// than the container it runs in (issue #255).
type DetectOptions struct {
	RootPrefix string
}

// DetectWith detects the Proxmox environment under the given options. With an
// empty RootPrefix it is identical to the historical Detect(): detection reads the
// real root and probes host commands. With a RootPrefix it re-anchors the
// detection paths under the prefix and skips the host command probes, because
// pveversion/proxmox-backup-manager executed inside the container answer for the
// container, not for the mounted host.
func DetectWith(opts DetectOptions) (*EnvironmentInfo, error) {
	prev := rootPrefix
	rootPrefix = strings.TrimSpace(opts.RootPrefix)
	defer func() { rootPrefix = prev }()

	info, err := detectEnvironmentInfo()
	if info.Type == types.ProxmoxUnknown {
		if err != nil {
			return info, err
		}
		return info, fmt.Errorf("unable to detect Proxmox environment")
	}
	return info, err
}

// DetectHostUnderPrefix detects a Proxmox host whose filesystem is mounted under
// prefix (SYSTEM_ROOT_PREFIX, issue #255). It uses ONLY the re-anchored filesystem
// markers that bare-metal PVE/PBS detection uses; it never runs the host command
// probes, which answer for the appliance container rather than the mounted host. It
// NEVER returns nil and NEVER returns a live-container type: when no host markers
// are present under the prefix it returns a ProxmoxUnknown EnvironmentInfo (fail
// closed), so a caller cannot inherit the container type and label a hollow archive
// as a valid PVE/PBS backup. The caller is responsible for screening an empty or
// root prefix before calling.
func DetectHostUnderPrefix(prefix string) *EnvironmentInfo {
	info, _ := DetectWith(DetectOptions{RootPrefix: prefix})
	if info == nil { // defensive; detectEnvironmentInfo never yields nil today
		return &EnvironmentInfo{Type: types.ProxmoxUnknown, Version: "unknown"}
	}
	return info
}

// hostRooted reports whether detection is re-anchored under a host prefix.
func hostRooted() bool {
	return rootPrefix != "" && rootPrefix != string(filepath.Separator)
}

// resolveUnderPrefix re-anchors an absolute detection path under rootPrefix. The
// detection paths are fixed literals (never attacker-controlled), so a plain join
// mirroring collector.systemPath is sufficient and matches how the rest of
// collection resolves system paths under the prefix.
func resolveUnderPrefix(path string) string {
	if !hostRooted() {
		return path
	}
	return filepath.Join(rootPrefix, strings.TrimPrefix(path, string(filepath.Separator)))
}

func detectEnvironmentInfo() (*EnvironmentInfo, error) {
	extendPath()

	trace := &detectionTrace{}
	pveVersion, hasPVE, pveResidue := detectPVE(trace)
	pbsVersion, hasPBS, pbsResidue := detectPBS(trace)

	info := &EnvironmentInfo{
		Type:        resolveType(hasPVE, hasPBS),
		PVEVersion:  normalizedDetectedVersion(pveVersion),
		PBSVersion:  normalizedDetectedVersion(pbsVersion),
		PVESource:   trace.decidedBy(productPVE),
		PBSSource:   trace.decidedBy(productPBS),
		Steps:       trace.steps,
		PVEResidual: pveResidue,
		PBSResidual: pbsResidue,
	}
	info.Version = combineVersions(info.PVEVersion, info.PBSVersion)

	if info.Type != types.ProxmoxUnknown {
		return info, nil
	}

	// A host with residue and no install is a different failure from a host with
	// nothing on it, and the difference is actionable: under SYSTEM_ROOT_PREFIX it
	// usually means the mount carries /etc but not the /usr and /var that hold the
	// package evidence. Saying only "unable to detect" would send the operator
	// looking for the wrong thing.
	if residue := firstNonEmpty(info.PVEResidual, info.PBSResidual); residue != "" {
		return info, fmt.Errorf("unable to detect Proxmox environment: %s was found but no installed product was%s",
			residue, debugSuffix(writeDetectionDebug()))
	}
	return info, fmt.Errorf("unable to detect Proxmox environment%s", debugSuffix(writeDetectionDebug()))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func debugSuffix(path string) string {
	if path == "" {
		return ""
	}
	return " (debug saved to " + path + ")"
}

func resolveType(hasPVE, hasPBS bool) types.ProxmoxType {
	switch {
	case hasPVE && hasPBS:
		return types.ProxmoxDual
	case hasPVE:
		return types.ProxmoxVE
	case hasPBS:
		return types.ProxmoxBS
	default:
		return types.ProxmoxUnknown
	}
}

func normalizedDetectedVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || strings.EqualFold(version, "unknown") {
		return ""
	}
	return version
}

func combineVersions(pveVersion, pbsVersion string) string {
	switch {
	case pveVersion != "" && pbsVersion != "":
		return fmt.Sprintf("pve=%s,pbs=%s", pveVersion, pbsVersion)
	case pveVersion != "":
		return pveVersion
	case pbsVersion != "":
		return pbsVersion
	default:
		return "unknown"
	}
}

// detectPVE walks the PVE marker ladder and returns at the first marker that PROVES
// the product is installed. Every rung it reaches is recorded in trace (nil records
// nothing), so a log can say which marker produced the verdict and which ones it had
// already ruled out. The third return is the first residue seen: see detectPBS, which
// is where that distinction is load-bearing.
func detectPVE(trace *detectionTrace) (string, bool, string) {
	residue := ""
	noteResidue := func(marker, target, note string) {
		trace.residue(productPVE, marker, target, note)
		if residue == "" {
			residue = fmt.Sprintf("%s (%s)", marker, target)
		}
	}

	if hostRooted() {
		trace.skip(productPVE, "command", "pveversion", "a command run here answers for the appliance, not for the mounted host")
	} else if version, ok := detectPVEViaCommand(); ok {
		trace.hit(productPVE, "command", "pveversion", version)
		return version, true, ""
	} else {
		trace.miss(productPVE, "command", "pveversion")
	}

	versionFiles := targetList(pveVersionFile, pveLegacyFile)
	switch version, outcome := detectPVEViaVersionFiles(); outcome {
	case markerInstalled:
		trace.hit(productPVE, "version-file", versionFiles, version)
		return version, true, ""
	case markerResidual:
		noteResidue("version-file", versionFiles, "the file is there but carries no version")
	default:
		trace.miss(productPVE, "version-file", versionFiles)
	}

	// dpkg is version-bearing and reliable offline, so it precedes the version-less
	// markers below and recovers the real version even when the pmxcfs version files
	// are absent.
	if version, ok := dpkgPackageInstalled("pve-manager"); ok {
		trace.hit(productPVE, "dpkg pve-manager", resolveUnderPrefix(dpkgStatusFile), version)
		return version, true, ""
	}
	trace.miss(productPVE, "dpkg pve-manager", resolveUnderPrefix(dpkgStatusFile))

	if fileExists(pveClusterDB) {
		trace.hit(productPVE, "cluster-db", resolveUnderPrefix(pveClusterDB), "")
		return "unknown", true, ""
	}
	trace.miss(productPVE, "cluster-db", resolveUnderPrefix(pveClusterDB))

	if path := firstExistingFile(pveBinaryCandidates); path != "" {
		trace.hit(productPVE, "binary", path, "")
		return "unknown", true, ""
	}
	trace.miss(productPVE, "binary", targetList(pveBinaryCandidates...))

	if dirExists(pveShareDir) {
		trace.hit(productPVE, "share-dir", resolveUnderPrefix(pveShareDir), "")
		return "unknown", true, ""
	}
	trace.miss(productPVE, "share-dir", resolveUnderPrefix(pveShareDir))

	if path := firstMatchingSource(pveSourceFiles, pveSourceTokens); path != "" {
		noteResidue("apt-source", path, "a configured repository is not an installed package")
	} else {
		trace.miss(productPVE, "apt-source", targetList(pveSourceFiles...))
	}

	if path := firstExistingDir(pveDirCandidates); path != "" {
		noteResidue("directory", path, "no package owns this directory and none removes it")
	} else {
		trace.miss(productPVE, "directory", targetList(pveDirCandidates...))
	}

	return "", false, residue
}

// detectPBS is the PBS half of the same ladder, traced the same way, and it is the
// half where the split between an install and its leftovers was bought with a broken
// backup (issue #315).
//
// Everything proxmox-backup-server SHIPS goes away when the package does: the manager
// binary, /usr/share/proxmox-backup (dpkg -S names the package as its owner) and the
// dpkg stanza itself. Everything PBS CREATES stays: /etc/proxmox-backup and
// /var/lib/proxmox-backup belong to no package, are made at runtime, and the server
// postrm does not remove them even on purge. Measured on PBS 3.4.9 and 4.2.0.
//
// So the two kinds of marker answer different questions. The shipped ones answer "is
// PBS installed"; the created ones answer "was PBS ever here", which is not the
// question the backup recipe needs. Only the first kind ends this ladder.
func detectPBS(trace *detectionTrace) (string, bool, string) {
	residue := ""
	noteResidue := func(marker, target, note string) {
		trace.residue(productPBS, marker, target, note)
		if residue == "" {
			residue = fmt.Sprintf("%s (%s)", marker, target)
		}
	}

	if hostRooted() {
		trace.skip(productPBS, "command", "proxmox-backup-manager", "a command run here answers for the appliance, not for the mounted host")
	} else if version, ok := detectPBSViaCommand(); ok {
		trace.hit(productPBS, "command", "proxmox-backup-manager", version)
		return version, true, ""
	} else {
		trace.miss(productPBS, "command", "proxmox-backup-manager")
	}

	switch version, outcome := detectPBSViaVersionFile(); outcome {
	case markerInstalled:
		trace.hit(productPBS, "version-file", resolveUnderPrefix(pbsVersionFile), version)
		return version, true, ""
	case markerResidual:
		noteResidue("version-file", resolveUnderPrefix(pbsVersionFile), "the file is there but carries no version")
	default:
		trace.miss(productPBS, "version-file", resolveUnderPrefix(pbsVersionFile))
	}

	if version, ok := dpkgPackageInstalled("proxmox-backup-server"); ok {
		trace.hit(productPBS, "dpkg proxmox-backup-server", resolveUnderPrefix(dpkgStatusFile), version)
		return version, true, ""
	}
	trace.miss(productPBS, "dpkg proxmox-backup-server", resolveUnderPrefix(dpkgStatusFile))

	if path := firstExistingFile(pbsBinaryCandidates); path != "" {
		trace.hit(productPBS, "binary", path, "")
		return "unknown", true, ""
	}
	trace.miss(productPBS, "binary", targetList(pbsBinaryCandidates...))

	if dirExists(pbsShareDir) {
		trace.hit(productPBS, "share-dir", resolveUnderPrefix(pbsShareDir), "")
		return "unknown", true, ""
	}
	trace.miss(productPBS, "share-dir", resolveUnderPrefix(pbsShareDir))

	// The apt candidates carry the token "pbs", and the repository Proxmox tells you to
	// add on a non-PBS host to keep proxmox-backup-client current is called pbs-client.
	// A configured repository never proved an install; on this rung it used to.
	if path := firstMatchingSource(pbsSourceFiles, pbsSourceTokens); path != "" {
		noteResidue("apt-source", path, "a configured repository is not an installed package")
	} else {
		trace.miss(productPBS, "apt-source", targetList(pbsSourceFiles...))
	}

	if path := firstExistingDir(pbsDirCandidates); path != "" {
		noteResidue("directory", path, "no package owns this directory and none removes it")
	} else {
		trace.miss(productPBS, "directory", targetList(pbsDirCandidates...))
	}

	return "", false, residue
}

// firstExistingFile returns the first candidate that resolves to a regular file
// under the active prefix, as the resolved path, so a trace line names the file that
// actually answered instead of the whole candidate list. Empty when none does.
func firstExistingFile(paths []string) string {
	for _, path := range paths {
		if fileExists(path) {
			return resolveUnderPrefix(path)
		}
	}
	return ""
}

// firstExistingDir is firstExistingFile for directory candidates.
func firstExistingDir(paths []string) string {
	for _, path := range paths {
		if dirExists(path) {
			return resolveUnderPrefix(path)
		}
	}
	return ""
}

// firstMatchingSource returns the first repository file whose content carries one of
// the tokens, as the resolved path. Empty when none does.
func firstMatchingSource(paths []string, tokens []string) string {
	for _, path := range paths {
		if containsAny(path, tokens) {
			return resolveUnderPrefix(path)
		}
	}
	return ""
}

// dpkgPackageInstalled reports whether the dpkg status database under the active
// prefix records pkg as installed, returning its Version. It matches an anchored
// "Package:" field plus a "Status: ... installed" line so a Depends: mention in
// another stanza, or a residual "deinstall ok config-files" entry, never counts as
// installed. Stanzas are blank-line separated.
func dpkgPackageInstalled(pkg string) (string, bool) {
	data, err := readFileFunc(resolveUnderPrefix(dpkgStatusFile))
	if err != nil {
		return "", false
	}
	for _, stanza := range strings.Split(string(data), "\n\n") {
		if dpkgStanzaField(stanza, "Package") != pkg {
			continue
		}
		if !strings.HasSuffix(dpkgStanzaField(stanza, "Status"), " installed") {
			return "", false
		}
		version := dpkgStanzaField(stanza, "Version")
		if version == "" {
			version = "unknown"
		}
		return version, true
	}
	return "", false
}

// dpkgStanzaField returns the trimmed value of the first "Key: value" line in a
// stanza, anchored at column 0 so an indented continuation line never matches.
func dpkgStanzaField(stanza, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(stanza, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func detectPVEViaCommand() (string, bool) {
	cmdPath, err := lookPathFunc("pveversion")
	if err != nil {
		return "", false
	}

	output, err := runCommandFunc(cmdPath)
	if err != nil {
		return "unknown", true
	}

	version := extractPVEVersion(output)
	if version == "" {
		return "unknown", true
	}
	return version, true
}

func detectPBSViaCommand() (string, bool) {
	cmdPath, err := lookPathFunc("proxmox-backup-manager")
	if err != nil {
		return "", false
	}

	output, err := runCommandFunc(cmdPath, "version")
	if err != nil {
		return "unknown", true
	}

	version := extractPBSVersion(output)
	if version == "" {
		return "unknown", true
	}
	return version, true
}

// markerOutcome is what a version-file probe found. The middle state is the point:
// a file that exists but says nothing is neither "the product is here" nor "there is
// nothing here", and collapsing it onto either one loses the only detail that
// explains the verdict.
type markerOutcome int

const (
	markerAbsent markerOutcome = iota
	markerResidual
	markerInstalled
)

func detectPVEViaVersionFiles() (string, markerOutcome) {
	outcome := markerAbsent

	if fileExists(pveVersionFile) {
		if version := readAndTrim(pveVersionFile); version != "" {
			return version, markerInstalled
		}
		outcome = markerResidual
	}

	if fileExists(pveLegacyFile) {
		data := readAndTrim(pveLegacyFile)
		if version := extractPVEVersion(data); version != "" {
			return version, markerInstalled
		}
		outcome = markerResidual
	}

	return "", outcome
}

func detectPBSViaVersionFile() (string, markerOutcome) {
	if !fileExists(pbsVersionFile) {
		return "", markerAbsent
	}
	if version := readAndTrim(pbsVersionFile); version != "" {
		return version, markerInstalled
	}
	return "", markerResidual
}

func detectPVEViaSources() bool {
	return firstMatchingSource(pveSourceFiles, pveSourceTokens) != ""
}

func detectPBSViaSources() bool {
	return firstMatchingSource(pbsSourceFiles, pbsSourceTokens) != ""
}

func detectViaDirectories(paths []string) bool {
	return firstExistingDir(paths) != ""
}

func extendPath() {
	currentPath := os.Getenv("PATH")
	pathSet := make(map[string]struct{})
	for _, part := range strings.Split(currentPath, string(os.PathListSeparator)) {
		pathSet[part] = struct{}{}
	}

	updated := currentPath
	for _, add := range additionalPaths {
		if _, ok := pathSet[add]; !ok {
			if updated == "" {
				updated = add
			} else {
				updated = updated + string(os.PathListSeparator) + add
			}
		}
	}

	if updated != currentPath {
		_ = os.Setenv("PATH", updated)
	}
}

func runCommand(command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd, cmdErr := safeexec.TrustedCommandContext(ctx, command, args...)
	if cmdErr != nil {
		return "", cmdErr
	}
	output, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command %s timed out", command)
	}
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func extractPVEVersion(output string) string {
	re := regexp.MustCompile(`pve-manager/([0-9]+\.[0-9]+(?:[.-][0-9]+)*)`)
	match := re.FindStringSubmatch(output)
	if len(match) >= 2 {
		return match[1]
	}
	return ""
}

func extractPBSVersion(output string) string {
	re := regexp.MustCompile(`version:\s*([0-9]+\.[0-9]+(?:[.-][0-9]+)*)`)
	match := re.FindStringSubmatch(output)
	if len(match) >= 2 {
		return match[1]
	}
	return ""
}

func containsAny(path string, tokens []string) bool {
	data, err := readFileFunc(resolveUnderPrefix(path))
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	for _, token := range tokens {
		if strings.Contains(lower, strings.ToLower(token)) {
			return true
		}
	}
	return false
}

func readAndTrim(path string) string {
	data, err := readFileFunc(resolveUnderPrefix(path))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	info, err := statFunc(resolveUnderPrefix(path))
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func dirExists(path string) bool {
	info, err := statFunc(resolveUnderPrefix(path))
	if err != nil {
		return false
	}
	return info.IsDir()
}

func writeDetectionDebug() string {
	debugDir := filepath.Join(debugBaseDir, "proxsave")
	if err := mkdirAllFunc(debugDir, 0o755); err != nil {
		return ""
	}
	now := timeNowFunc()
	path := filepath.Join(debugDir, fmt.Sprintf("proxmox_detection_debug_%d.log", now.Unix()))

	var builder strings.Builder
	fmt.Fprintf(&builder, "=== Proxmox Detection Failure Debug - %s ===\n", now.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&builder, "Current PATH: %s\n", os.Getenv("PATH"))

	if u, err := userCurrentFunc(); err == nil {
		fmt.Fprintf(&builder, "Current USER: %s\n", u.Username)
	} else {
		builder.WriteString("Current USER: unknown\n")
	}

	if cwd, err := getwdFunc(); err == nil {
		fmt.Fprintf(&builder, "Current PWD: %s\n", cwd)
	}
	fmt.Fprintf(&builder, "Shell: %s\n\n", os.Getenv("SHELL"))

	for _, line := range markerLines() {
		builder.WriteString(line + "\n")
	}

	if err := writeFileFunc(path, []byte(builder.String()), 0640); err != nil {
		return ""
	}
	return path
}

// MarkerSnapshot returns the whole marker table detection can consult - every
// command, file, directory, package and repository file, each with the answer it
// gives right now - under the given options. The ladder trace in EnvironmentInfo.Steps
// stops at the marker that decided the type and so cannot say what ELSE is on the
// host; this can, which is what tells a leftover PBS marker on a PVE-only host apart
// from a real PBS install (issue #315). It restats every marker, so callers gate it
// on a debug run.
func MarkerSnapshot(opts DetectOptions) []string {
	prev := rootPrefix
	rootPrefix = strings.TrimSpace(opts.RootPrefix)
	defer func() { rootPrefix = prev }()
	return markerLines()
}

// markerLines builds the marker table under whatever prefix is active. Shared by the
// detection-failure debug file and MarkerSnapshot so both always report the same
// checks in the same order. Every path is printed re-anchored under the active
// prefix, so a line names the file that was really stat-ed rather than the bare
// literal, which under a host prefix points at the appliance instead of the host.
func markerLines() []string {
	var lines []string
	add := func(format string, args ...interface{}) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	// The ladder refuses to run these under a prefix, and the snapshot has to refuse them
	// for the same reason: lookPath searches the appliance's PATH and would answer for the
	// appliance. The reason travels on each line rather than once above them, because every
	// line leaves here on its own as a "Detection marker:" log entry.
	add("=== Command availability check ===")
	for _, cmd := range []string{"pveversion", "proxmox-backup-manager"} {
		if hostRooted() {
			add("command -v %s: skipped (answers for the appliance, not the mounted host)", cmd)
			continue
		}
		add("command -v %s: %s", cmd, lookPathOrNotFound(cmd))
	}
	add("")

	add("=== File existence check ===")
	for _, bin := range commandBinaryCandidates {
		add("%s exists: %s", resolveUnderPrefix(bin), boolToYes(fileExists(bin)))
		add("%s executable: %s", resolveUnderPrefix(bin), boolToYes(isExecutable(bin)))
	}
	add("")

	add("=== Directory existence check ===")
	for _, dir := range append(append([]string{}, pveDirCandidates...), pbsDirCandidates...) {
		add("%s exists: %s", resolveUnderPrefix(dir), boolToYes(dirExists(dir)))
	}
	add("")

	add("=== Version file check ===")
	for _, file := range []string{pveLegacyFile, pveVersionFile, pbsVersionFile} {
		add("%s exists: %s", resolveUnderPrefix(file), boolToYes(fileExists(file)))
		if content := readAndTrim(file); content != "" {
			add("%s content: %s", resolveUnderPrefix(file), content)
		}
	}
	add("")

	add("=== APT source files check ===")
	// PVE and PBS share a candidate, so the union is deduplicated: the same file
	// listed twice reads as two separate findings.
	for _, source := range dedupePaths(append(append([]string{}, pveSourceFiles...), pbsSourceFiles...)) {
		add("%s exists: %s", resolveUnderPrefix(source), boolToYes(fileExists(source)))
	}
	// Existence alone never decided anything: the file counts as a PVE or PBS marker
	// only when its content carries the product token, so report the match too.
	add("PVE apt-source match: %s", pathOrNone(firstMatchingSource(pveSourceFiles, pveSourceTokens)))
	add("PBS apt-source match: %s", pathOrNone(firstMatchingSource(pbsSourceFiles, pbsSourceTokens)))
	add("")

	add("=== Offline host markers check ===")
	add("%s exists: %s", resolveUnderPrefix(pveClusterDB), boolToYes(fileExists(pveClusterDB)))
	for _, bin := range append(append([]string{}, pveBinaryCandidates...), pbsBinaryCandidates...) {
		add("%s exists: %s", resolveUnderPrefix(bin), boolToYes(fileExists(bin)))
	}
	add("%s exists: %s", resolveUnderPrefix(pveShareDir), boolToYes(dirExists(pveShareDir)))
	add("%s exists: %s", resolveUnderPrefix(pbsShareDir), boolToYes(dirExists(pbsShareDir)))
	add("%s exists: %s", resolveUnderPrefix(dpkgStatusFile), boolToYes(fileExists(dpkgStatusFile)))
	if _, ok := dpkgPackageInstalled("pve-manager"); ok {
		add("dpkg pve-manager: installed")
	}
	if _, ok := dpkgPackageInstalled("proxmox-backup-server"); ok {
		add("dpkg proxmox-backup-server: installed")
	}
	add("")

	return lines
}

// dedupePaths drops repeated candidates while keeping the order they are probed in.
func dedupePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

// pathOrNone renders an empty marker path as an explicit "none" so a snapshot line
// never reads as a truncated path.
func pathOrNone(path string) string {
	if path == "" {
		return "none"
	}
	return path
}

func lookPathOrNotFound(binary string) string {
	if path, err := lookPathFunc(binary); err == nil {
		return path
	}
	return "NOT FOUND"
}

func boolToYes(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

// isExecutable re-anchors like fileExists and dirExists do. Stating the bare literal is
// what let the marker table print a host path and answer for the appliance's own copy of
// it: under a prefix the line read "executable: YES" for a file the mounted host did not
// have, and "executable: NO" for one it had and could run.
func isExecutable(path string) bool {
	info, err := statFunc(resolveUnderPrefix(path))
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Mode()&0111 != 0
}
