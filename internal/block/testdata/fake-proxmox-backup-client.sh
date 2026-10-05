#!/bin/sh
# Fake proxmox-backup-client for the tests of package block (installFakeClient). It keeps
# its state in the directory it is installed in.
#
# Every call appends a record to calls.log: "call", one "arg <value>" per argument, one
# "env <NAME>" per PBS_* variable received (names only, never values), "match <NAME>
# yes|no" for every expect.<NAME> file (the value compared, not recorded), "stdin <target
# of fd 0>", "end". Then it answers with <key>.stdout, <key>.stderr and <key>.rc (rc 0
# and no output when absent), where <key> is the subcommand, two words joined by "-" for
# snapshot, key and namespace ("snapshot-list"). <key>.sleep makes it hang that many
# seconds after writing its output. <key>.<n>.rc (with its .stdout and .stderr) answers
# the n-th call of <key> only, counted from 1 in <key>.count.
dir=$(dirname "$0")
key=$1
case "$1" in
snapshot | key | namespace) key="$1-$2" ;;
esac
{
	echo "call"
	for arg in "$@"; do
		printf 'arg %s\n' "$arg"
	done
	env | sed -n 's/^\(PBS_[A-Za-z0-9_]*\)=.*/env \1/p'
	for expect in "$dir"/expect.*; do
		[ -f "$expect" ] || continue
		name=${expect##*/expect.}
		eval "value=\${$name-}"
		if [ "$value" = "$(cat "$expect")" ]; then
			echo "match $name yes"
		else
			echo "match $name no"
		fi
	done
	printf 'stdin %s\n' "$(readlink /proc/self/fd/0)"
	echo "end"
} >>"$dir/calls.log"
n=1
[ -f "$dir/$key.count" ] && n=$(($(cat "$dir/$key.count") + 1))
echo "$n" >"$dir/$key.count"
[ -f "$dir/$key.$n.rc" ] && key="$key.$n"
[ -f "$dir/$key.stdout" ] && cat "$dir/$key.stdout"
[ -f "$dir/$key.stderr" ] && cat "$dir/$key.stderr" >&2
if [ -f "$dir/$key.sleep" ]; then
	exec sleep "$(cat "$dir/$key.sleep")"
fi
rc=0
[ -f "$dir/$key.rc" ] && rc=$(cat "$dir/$key.rc")
exit "$rc"
