package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
	"github.com/tis24dev/proxsave/internal/ui/components"
)

type guestApplyPrecondition string

const (
	guestMustBeAbsent  guestApplyPrecondition = "absent"
	guestMustBeStopped guestApplyPrecondition = "stopped"
	pvePerlPath                               = "/usr/bin/perl"
)

type pveGuestLockedWriterFunc func(
	ctx context.Context,
	logger *logging.Logger,
	node string,
	vm vmEntry,
	precondition guestApplyPrecondition,
	data []byte,
) error

var pveGuestLockedWriter pveGuestLockedWriterFunc = writeGuestConfigWithPVELock

var runPVEGuestLockHelper = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd, err := safeexec.TrustedCommandContext(ctx, pvePerlPath, args...)
	if err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

// pveGuestLockedApplyPerl deliberately uses PVE's own QEMU and LXC lock
// implementations. Both locks are acquired in one fixed order so a VMID cannot
// race through the other guest kind while cluster ownership is revalidated.
// All dynamic values arrive through @ARGV; none is interpolated into the code.
const pveGuestLockedApplyPerl = `
use strict;
use warnings;

use Digest::SHA qw(sha256_hex);
use PVE::Cluster ();
use PVE::INotify ();
use PVE::LXC ();
use PVE::LXC::Config ();
use PVE::QemuConfig ();
use PVE::QemuServer ();
use PVE::Tools ();

my ($expected, $kind, $vmid, $node, $source, $digest) = @ARGV;

die "invalid guest apply precondition\n"
    if !defined($expected) || ($expected ne 'absent' && $expected ne 'stopped');
die "invalid guest kind\n"
    if !defined($kind) || ($kind ne 'qemu' && $kind ne 'lxc');
die "invalid guest VMID\n"
    if !defined($vmid) || $vmid !~ m/\A[1-9][0-9]*\z/;
die "invalid PVE node\n"
    if !defined($node) || $node ne PVE::INotify::nodename();
die "invalid staged configuration path\n"
    if !defined($source) || $source !~ m!\A/!;
die "invalid staged configuration digest\n"
    if !defined($digest) || $digest !~ m/\A[0-9a-f]{64}\z/;

my $class = $kind eq 'qemu' ? 'PVE::QemuConfig' : 'PVE::LXC::Config';
my $is_running = $kind eq 'qemu'
    ? sub { return PVE::QemuServer::check_running($vmid) ? 1 : 0; }
    : sub { return PVE::LXC::check_running($vmid) ? 1 : 0; };

my $apply = sub {
    PVE::Cluster::cfs_update();
    PVE::Cluster::check_cfs_quorum();

    open(my $src, '<:raw', $source)
        or die "open staged guest configuration failed: $!\n";
    local $/;
    my $data = <$src>;
    $data = '' if !defined($data);
    close($src) or die "close staged guest configuration failed: $!\n";
    die "staged guest configuration changed before locked apply\n"
        if sha256_hex($data) ne $digest;

    my $vmlist = PVE::Cluster::get_vmlist();
    my $record = $vmlist->{ids}->{$vmid};
    my $target = $class->config_file($vmid, $node);
    my $registered = 0;

    if ($expected eq 'absent') {
        die "guest $vmid appeared before locked apply\n" if defined($record);
        PVE::Cluster::check_vmid_unused($vmid);

        # Register a create-locked placeholder through PVE's configuration
        # writer first. This gives pmxcfs/PVE the same atomic VMID claim used by
        # normal guest creation before the exact staged bytes replace it.
        $class->write_config($vmid, { lock => 'create' });
        $registered = 1;
    } else {
        die "guest $vmid disappeared before locked apply\n" if !defined($record);
        die "guest $vmid belongs to node '$record->{node}', not '$node'\n"
            if !defined($record->{node}) || $record->{node} ne $node;
        die "guest $vmid is '$record->{type}', not '$kind'\n"
            if !defined($record->{type}) || $record->{type} ne $kind;

        # Loading the canonical same-kind config inside the lock also refuses a
        # concurrent move or deletion before the runtime-state decision.
        my $conf = $class->load_config($vmid, $node);
        die "guest $vmid is running; refusing locked file apply\n" if $is_running->();

        # PVE marks a guest busy with a config-level lock while a backup, migration,
        # clone or snapshot runs, and a STOPPED guest carries it just the same. The
        # apply goes ahead regardless: that marker is frequently left behind by an
        # operation that died, and refusing would block the restore in the very case
        # it exists for. Report it instead, so the override reaches the operator.
        my $lock = $conf->{lock};
        print "proxsave-lock: $lock\n" if defined($lock) && $lock =~ m/\A[a-z-]+\z/;
    }

    eval { PVE::Tools::file_set_contents($target, $data, 0640); };
    my $write_error = $@;
    if ($write_error) {
        if ($registered) {
            eval { $class->destroy_config($vmid); };
            my $cleanup_error = $@;
            $write_error .= "cleanup of create placeholder failed: $cleanup_error"
                if $cleanup_error;
        }
        die $write_error;
    }
};

# QEMU and LXC use different local lock files for the same numeric VMID. Take
# both in this invariant order, then make the cluster decision and write once.
PVE::QemuConfig->lock_config($vmid, sub {
    PVE::LXC::Config->lock_config($vmid, $apply);
});
`

func writeGuestConfigWithPVELock(
	ctx context.Context,
	logger *logging.Logger,
	node string,
	vm vmEntry,
	precondition guestApplyPrecondition,
	data []byte,
) error {
	if precondition != guestMustBeAbsent && precondition != guestMustBeStopped {
		return fmt.Errorf("invalid guest apply precondition %q", precondition)
	}
	if vm.Kind != "qemu" && vm.Kind != "lxc" {
		return fmt.Errorf("invalid guest kind %q", vm.Kind)
	}
	vmid, err := strconv.Atoi(vm.VMID)
	if err != nil || vmid <= 0 || strconv.Itoa(vmid) != vm.VMID {
		return fmt.Errorf("invalid guest VMID %q", vm.VMID)
	}
	if strings.TrimSpace(node) == "" {
		return fmt.Errorf("invalid empty PVE node")
	}
	if !filepath.IsAbs(vm.Path) {
		return fmt.Errorf("staged guest configuration path must be absolute: %s", vm.Path)
	}

	digest := sha256.Sum256(data)
	out, runErr := runPVEGuestLockHelper(
		ctx,
		"-e", pveGuestLockedApplyPerl, "--",
		string(precondition), vm.Kind, vm.VMID, node, vm.Path, hex.EncodeToString(digest[:]),
	)
	if runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		detail := compactPVEGuestHelperOutput(out)
		if detail != "" {
			return fmt.Errorf("PVE locked guest apply failed: %w (output: %s)", runErr, detail)
		}
		return fmt.Errorf("PVE locked guest apply failed: %w", runErr)
	}
	if lock := parseOverriddenGuestLock(out); lock != "" {
		logging.DebugStep(logger, "pve guest configs apply",
			"vmid=%s lock=%s action=override-lock", vm.VMID, lock)
		logger.Warning("Applied VM/CT config %s over a %q lock - PVE had the guest marked busy and that marker is now gone",
			guestDisplay(vm), lock)
	}
	if detail := compactPVEGuestHelperOutput(out); detail != "" {
		logger.Debug("PVE locked guest apply vmid=%s output: %s", vm.VMID, detail)
	}
	return nil
}

// pveGuestLockMarker is how the helper reports a config-level lock it wrote over. It
// is its own line rather than part of an error because the apply SUCCEEDS: overriding
// is the decided behaviour, and this is the record of it.
const pveGuestLockMarker = "proxsave-lock:"

// parseOverriddenGuestLock reads the lock name out of the helper output. The value
// comes from a guest config file, so it is accepted only as a bare lowercase word -
// PVE's own set (backup, migrate, clone, snapshot, rollback) fits and anything else
// is dropped rather than put in a log line.
func parseOverriddenGuestLock(out []byte) string {
	for line := range strings.SplitSeq(string(out), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), pveGuestLockMarker)
		if !ok {
			continue
		}
		lock := strings.TrimSpace(rest)
		if lock == "" || len(lock) > 32 {
			return ""
		}
		for _, r := range lock {
			if (r < 'a' || r > 'z') && r != '-' {
				return ""
			}
		}
		return lock
	}
	return ""
}

// guestDisplay is how a guest is named in every operator-facing line of the apply,
// the VMID alone when the export carries no name. It lives in one place so the
// applied line and the override line above it cannot drift apart.
func guestDisplay(vm vmEntry) string {
	if strings.TrimSpace(vm.Name) == "" {
		return vm.VMID
	}
	return fmt.Sprintf("%s (%s)", vm.VMID, vm.Name)
}

func compactPVEGuestHelperOutput(out []byte) string {
	const maxRunes = 512
	detail := components.SanitizeLine(strings.TrimSpace(string(out)))
	runes := []rune(detail)
	if len(runes) > maxRunes {
		detail = string(runes[:maxRunes]) + "…"
	}
	return detail
}
