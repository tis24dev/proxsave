#!/usr/bin/env bash

# Shared helpers for the release pipeline (release-intake / release-guard /
# post-merge-release). This project derives its version purely from the git tag
# at build time via GoReleaser ldflags (internal/version.Version), so there is NO
# in-repo version file to bump and NO manifest assertions here.

# Keep this regex identical to the SemVer check in release.yml so the two never
# disagree (allows vX.Y.Z and prereleases like vX.Y.Z-rc1 / vX.Y.Z-beta1).
RELEASE_TAG_REGEX='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
# Unprotected trigger tag that starts a release. It deliberately does NOT match
# the v* glob (release.yml / a tag-immutability ruleset), so it can be created and
# deleted freely; the real vX.Y.Z tag is CREATED ONCE on the squash commit by
# post-merge-release (a tag creation, which a tag-immutability ruleset allows).
PR_TAG_REGEX='^pr-v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

die() {
  echo "::error::$*" >&2
  exit 1
}

notice() {
  echo "::notice::$*"
}

is_release_tag() {
  [[ "${1:-}" =~ ${RELEASE_TAG_REGEX} ]]
}

validate_release_tag() {
  local tag="${1:-}"

  if ! is_release_tag "${tag}"; then
    die "Invalid release tag '${tag}'. Allowed formats are vX.Y.Z and vX.Y.Z-rc1/-beta1."
  fi
}

is_pr_tag() {
  [[ "${1:-}" =~ ${PR_TAG_REGEX} ]]
}

# pr-vX.Y.Z -> vX.Y.Z (the release tag that will be CREATED at merge).
release_tag_from_pr_tag() {
  local pr_tag="${1:-}"

  if ! is_pr_tag "${pr_tag}"; then
    die "Invalid trigger tag '${pr_tag}'. Expected pr-vX.Y.Z (or pr-vX.Y.Z-rc1/-beta1)."
  fi
  printf '%s\n' "${pr_tag#pr-}"
}

# Informational only: GoReleaser's `release.prerelease: auto` already marks
# prereleases from the -suffix, so nothing branches on this.
is_prerelease_tag() {
  [[ "${1:-}" == *-* ]]
}

# Success if the given commit is contained in (ancestor of, or equal to)
# origin/main. The caller MUST `git fetch origin main` first.
tag_commit_on_main() {
  git merge-base --is-ancestor "${1:-}" origin/main
}

extract_pr_marker() {
  local marker="${1:-}"

  python3 - "${marker}" <<'PY'
import os
import re
import sys

marker = sys.argv[1]
body = os.environ.get("PR_BODY", "")
pattern = rf"^<!-- {re.escape(marker)}: ([^<\n]+) -->$"
match = re.search(pattern, body, re.MULTILINE)
if not match:
    sys.exit(1)
print(match.group(1).strip())
PY
}

delete_remote_tag() {
  local tag="${1:-}"

  git push origin ":refs/tags/${tag}" || true
}

# Existence probes that DISTINGUISH absent from error. A transient auth/network
# failure must never be silently read as "does not exist" (which would defeat the
# preflight / immutability gates). Echo: present|absent|error.

remote_tag_state() {
  local tag="${1:-}"
  local rc=0
  # git ls-remote --exit-code: 0 = ref found, 2 = no matching ref, other = failure.
  git ls-remote --exit-code --tags origin "refs/tags/${tag}" >/dev/null 2>&1 || rc=$?
  case "${rc}" in
    0) echo "present" ;;
    2) echo "absent" ;;
    *) echo "error" ;;
  esac
}

remote_release_state() {
  local tag="${1:-}"
  local out
  local rc=0
  out="$(gh api "repos/${GITHUB_REPOSITORY}/releases/tags/${tag}" 2>&1)" || rc=$?
  if [[ "${rc}" -eq 0 ]]; then
    echo "present"
  elif printf '%s' "${out}" | grep -qi 'HTTP 404\|Not Found'; then
    echo "absent"
  else
    echo "error"
  fi
}

# Hard gates: die on "present" AND on "error" (fail closed). Used where the only
# acceptable state to proceed is a confirmed "absent".
assert_release_tag_absent() {
  local tag="${1:-}"
  case "$(remote_tag_state "${tag}")" in
    present) die "Tag ${tag} already exists and is immutable." ;;
    error)   die "Could not determine whether tag ${tag} exists (git ls-remote failed); aborting." ;;
  esac
}

assert_release_absent() {
  local tag="${1:-}"
  case "$(remote_release_state "${tag}")" in
    present) die "Release ${tag} already exists." ;;
    error)   die "Could not determine whether release ${tag} exists (gh api failed); aborting." ;;
  esac
}
