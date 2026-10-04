#!/bin/sh
# Fake rclone for the cloud tests: the subcommands the cloud backend runs, over plain
# local paths (CLOUD_REMOTE as a directory), in rclone's output formats. With
# FAKE_RCLONE_NOTICE set it first writes, on stderr, the line rclone writes on every
# call when its config file is missing (measured 2026-09-05).
if [ -n "${FAKE_RCLONE_NOTICE:-}" ]; then
	echo '2026/09/05 19:53:22 NOTICE: Config file "/root/.config/rclone/rclone.conf" not found - using defaults' >&2
fi
cmd=$1
shift
files_only=""
p1=""
p2=""
while [ $# -gt 0 ]; do
	case "$1" in
	--max-depth | --bwlimit | --transfers) shift 2 ;;
	--files-only) files_only=1; shift ;;
	--*) shift ;;
	*)
		if [ -z "$p1" ]; then p1=$1; else p2=$1; fi
		shift
		;;
	esac
done
fail() {
	echo "$(date '+%Y/%m/%d %H:%M:%S') ERROR : $1" >&2
	exit 1
}
lsl_line() {
	printf '%9d %s %s\n' "$(stat -c %s "$1")" "$(date -r "$1" '+%Y-%m-%d %H:%M:%S.%N')" "$2"
}
case "$cmd" in
lsf)
	[ -d "$p1" ] || fail "error listing: directory not found"
	for f in "$p1"/* "$p1"/.[!.]*; do
		[ -e "$f" ] || continue
		if [ -d "$f" ]; then
			[ -n "$files_only" ] || echo "$(basename "$f")/"
		else
			basename "$f"
		fi
	done
	;;
lsl)
	if [ -f "$p1" ]; then
		lsl_line "$p1" "$(basename "$p1")"
	else
		[ -d "$p1" ] || fail "error listing: directory not found"
		for f in "$p1"/* "$p1"/.[!.]*; do
			if [ -f "$f" ]; then lsl_line "$f" "$(basename "$f")"; fi
		done
	fi
	;;
ls)
	[ -d "$p1" ] || fail "error listing: directory not found"
	for f in "$p1"/*; do
		if [ -f "$f" ]; then printf '%9d %s\n' "$(stat -c %s "$f")" "$(basename "$f")"; fi
	done
	;;
copyto)
	if ! mkdir -p "$(dirname "$p2")" || ! cp -p "$p1" "$p2"; then
		fail "Failed to copy: $p1"
	fi
	;;
hashsum)
	[ -f "$p2" ] || fail "$(basename "$p2"): object not found"
	printf '%s  %s\n' "$(sha256sum <"$p2" | cut -d' ' -f1)" "$(basename "$p2")"
	;;
cat)
	[ -f "$p1" ] || fail "$(basename "$p1"): object not found"
	cat "$p1"
	;;
deletefile)
	[ -f "$p1" ] || fail "$(basename "$p1"): Failed to deletefile: object not found"
	rm -f "$p1"
	;;
mkdir) mkdir -p "$p1" ;;
touch) touch "$p1" ;;
*) fail "unknown command $cmd" ;;
esac
