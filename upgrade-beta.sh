#!/usr/bin/env bash

set -euo pipefail

###############################################
# ProxSave BETA upgrader
#
# Installs a PRERELEASE (beta/rc) build onto an existing ProxSave install and
# finalizes it exactly like the normal upgrade, WITHOUT downloading again: it
# swaps in the beta binary, then runs `proxsave --upgrade --localfile` so the
# freshly installed binary upgrades backup.env, refreshes docs/symlinks,
# installs/restarts the daemon and fixes permissions.
#
# Why a separate script: the built-in `--upgrade` (and install.sh) only ever see
# `/releases/latest`, which GitHub defines as the latest NON-prerelease release,
# so a beta is invisible to them.
#
# What it does: it looks at the newest release overall (stable or beta).
#   - newest is a BETA   -> shows installed vs available, asks to confirm, installs it.
#   - newest is STABLE   -> tells you to use the standard upgrade and stops (so a
#                           month-old beta is never installed over a newer stable).
#
# Usage:
#   sh upgrade-beta.sh                 # newest release; installs it only if it is a beta (jq optional)
#   sh upgrade-beta.sh -y              # same, no confirmation prompt
#   sh upgrade-beta.sh v5.0.0-beta1    # force a specific tag (escape hatch, skips the release lookup)
###############################################

###############################################
# 1) Parse arguments (-y/--yes, optional explicit tag)
###############################################
ASSUME_YES=0
TAG_ARG=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -y|--yes)
      ASSUME_YES=1
      ;;
    -*)
      echo "❌ Unknown option: $1"
      exit 1
      ;;
    *)
      if [ -z "${TAG_ARG}" ]; then
        TAG_ARG="$1"
      else
        echo "❌ Unexpected argument: $1"
        exit 1
      fi
      ;;
  esac
  shift
done

###############################################
# 2) Ensure running as root
###############################################
if [ "$EUID" -ne 0 ]; then
  echo "❌ Please run as root"
  exit 1
fi

###############################################
# 3) Config
###############################################
REPO="tis24dev/proxsave"
TARGET_DIR="/opt/proxsave"
BUILD_DIR="${TARGET_DIR}/build"
TARGET_BIN="${BUILD_DIR}/proxsave"

# Pinned release-signing public key (ECDSA P-256), IDENTICAL to install.sh and the
# in-binary verifier. Prereleases are signed with the same key (GoReleaser signs
# every release), so an archive whose SHA256SUMS does not verify against this key
# is rejected. Fingerprint (sha256 of DER):
# fdbbba66cdb770b85a728c8aee0b920b4cd244c84f4fc5a0065188fbe9a5eddb
PUBKEY_PEM='-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAElks05mPtm1vm0YtHlSGX1HlgdXjn
liDJEnB+RgiWOQR+6xLWeX7PyauuMxUh/HNnvBQAokK91fLWes4r9Xlwzw==
-----END PUBLIC KEY-----'

###############################################
# 4) Require an existing install (this UPGRADES, it does not install)
###############################################
if [ ! -x "${TARGET_BIN}" ]; then
  echo "❌ No existing ProxSave install found at ${TARGET_BIN}"
  echo "   This script upgrades an existing install to a beta. Install first:"
  echo "   sh install.sh"
  exit 1
fi

###############################################
# 5) OS/ARCH detection
###############################################
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH_RAW="$(uname -m)"

if [ "${OS}" != "linux" ]; then
  echo "❌ Only Linux systems are supported"
  exit 1
fi

case "$ARCH_RAW" in
  x86_64) ARCH="amd64" ;;
  *)
    echo "❌ Unsupported architecture: ${ARCH_RAW} (only linux/amd64 is published; build from source for other architectures)"
    exit 1
    ;;
esac

###############################################
# 6) HTTP helpers (curl/wget)
###############################################
fetch() {
  local url="$1"

  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "${url}"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O - "${url}"
  else
    echo "❌ Neither curl nor wget is installed" >&2
    exit 1
  fi
}

download() {
  local url="$1"
  local out="$2"

  fetch "${url}" > "${out}"
}

###############################################
# 7) Detect the installed version (best-effort)
###############################################
# The `|| CURRENT_VERSION=""` keeps a corrupt installed binary (whose --version
# exits non-zero) from aborting the whole script under `set -euo pipefail` before
# the "unknown" fallback below can run.
CURRENT_VERSION="$("${TARGET_BIN}" --version 2>/dev/null | awk -F': ' '/^Version:/ {print $2; exit}')" || CURRENT_VERSION=""
if [ -z "${CURRENT_VERSION}" ]; then
  CURRENT_VERSION="unknown"
fi

###############################################
# 8) Resolve the target tag and decide whether to proceed
#
# Explicit tag  -> force it (escape hatch), no stable guard.
# Auto-detect   -> newest release overall; proceed only if it is a prerelease,
#                  otherwise point the user at the standard upgrade and stop.
###############################################
if [ -n "${TAG_ARG}" ]; then
  BETA_TAG="${TAG_ARG}"
  echo "--------------------------------------------"
  echo " ProxSave BETA upgrader (forced tag)"
  echo " Installed version:  ${CURRENT_VERSION}"
  echo " Requested tag:      ${BETA_TAG}"
  echo "--------------------------------------------"
else
  # /releases (the LIST endpoint) includes prereleases and is newest-first, so the
  # first entry is the most recent release overall (stable or beta). jq is used
  # when present; otherwise fall back to a first-match parse of the raw list JSON
  # so a stock system without jq still works (mirrors install.sh's tag detection).
  # The anonymous API never returns drafts, so the first entry is the newest
  # published release either way.
  RELEASES_JSON="$(fetch "https://api.github.com/repos/${REPO}/releases")"
  LATEST_TAG=""
  LATEST_PRE=""
  if command -v jq >/dev/null 2>&1; then
    LATEST_TAG="$(jq -r 'map(select(.draft == false)) | .[0].tag_name // empty' <<<"${RELEASES_JSON}" 2>/dev/null || true)"
    LATEST_PRE="$(jq -r 'map(select(.draft == false)) | (.[0].prerelease // false) | tostring' <<<"${RELEASES_JSON}" 2>/dev/null || true)"
  fi
  # Fallback (no jq, or jq failed): the list is newest-first, so the first
  # tag_name / prerelease occurrence in the raw JSON belongs to the newest release.
  if [ -z "${LATEST_TAG}" ] && [[ ${RELEASES_JSON} =~ \"tag_name\"[[:space:]]*:[[:space:]]*\"([^\"]+)\" ]]; then
    LATEST_TAG="${BASH_REMATCH[1]}"
  fi
  if [ -z "${LATEST_PRE}" ] && [[ ${RELEASES_JSON} =~ \"prerelease\"[[:space:]]*:[[:space:]]*(true|false) ]]; then
    LATEST_PRE="${BASH_REMATCH[1]}"
  fi

  if [ -z "${LATEST_TAG}" ]; then
    echo "❌ No release found for ${REPO} (the API may be rate-limited)."
    echo "   You can pass a tag explicitly: sh upgrade-beta.sh vX.Y.Z-beta1"
    exit 1
  fi

  if [ "${LATEST_PRE}" = "true" ]; then
    LATEST_TYPE="beta / prerelease"
  else
    LATEST_TYPE="stable"
  fi

  echo "--------------------------------------------"
  echo " ProxSave BETA upgrader"
  echo " Installed version:  ${CURRENT_VERSION}"
  echo " Latest available:   ${LATEST_TAG} (${LATEST_TYPE})"
  echo "--------------------------------------------"

  if [ "${LATEST_PRE}" != "true" ]; then
    echo "ℹ The newest release is STABLE, not a beta."
    echo "  This script only installs betas. Use the standard upgrade instead:"
    echo ""
    echo "    proxsave --upgrade"
    echo ""
    echo "  (or run it from the dashboard). Nothing was changed."
    exit 0
  fi

  BETA_TAG="${LATEST_TAG}"

  # Guard against a publish-order downgrade: /releases is ordered by publish date,
  # not semver, so a back-ported beta published AFTER a newer stable would show up
  # as .[0]. Refuse to auto-install a beta that is not newer than the installed
  # version (version-sort compares the numeric cores). Escape hatch: pass the tag
  # explicitly to force it.
  BETA_VERSION="${BETA_TAG#v}"
  if [ "${CURRENT_VERSION}" != "unknown" ] && [ "${CURRENT_VERSION}" != "${BETA_VERSION}" ]; then
    NEWEST="$(printf '%s\n%s\n' "${CURRENT_VERSION}" "${BETA_VERSION}" | sort -V | tail -n1)"
    if [ "${NEWEST}" = "${CURRENT_VERSION}" ]; then
      echo "ℹ The newest beta (${BETA_TAG}) is OLDER than your installed version (${CURRENT_VERSION})."
      echo "  Installing it would be a downgrade. Refusing."
      echo "  To force this specific beta anyway:"
      echo "    sh upgrade-beta.sh ${BETA_TAG}"
      exit 0
    fi
  fi
fi

VERSION="${BETA_TAG#v}"

###############################################
# 9) Confirm (skipped with -y)
###############################################
if [ "${ASSUME_YES}" -ne 1 ]; then
  if [ -r /dev/tty ]; then
    printf 'Install BETA %s over %s? [y/N]: ' "${BETA_TAG}" "${CURRENT_VERSION}" > /dev/tty
    read -r REPLY < /dev/tty || REPLY=""
    case "${REPLY}" in
      y|Y|yes|YES) : ;;
      *)
        echo "Aborted; nothing was changed."
        exit 0
        ;;
    esac
  else
    echo "❌ Not attached to a terminal and -y was not given; refusing to install a beta unattended."
    echo "   Re-run interactively, or pass -y to confirm: sh upgrade-beta.sh -y"
    exit 1
  fi
fi

###############################################
# 10) Build archive/checksum/signature URLs
###############################################
FILENAME="proxsave_${VERSION}_${OS}_${ARCH}.tar.gz"

BINARY_URL="https://github.com/${REPO}/releases/download/${BETA_TAG}/${FILENAME}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${BETA_TAG}/SHA256SUMS"
SIG_URL="https://github.com/${REPO}/releases/download/${BETA_TAG}/SHA256SUMS.sig"

echo "➡ Archive URL:   ${BINARY_URL}"
echo "➡ Checksum URL:  ${CHECKSUM_URL}"
echo "➡ Signature URL: ${SIG_URL}"

###############################################
# 11) Download archive, checksum and signature
###############################################
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
cd "${TMP_DIR}"

echo "[+] Downloading archive..."
download "${BINARY_URL}" "${FILENAME}"

echo "[+] Downloading SHA256SUMS..."
download "${CHECKSUM_URL}" "SHA256SUMS"

echo "[+] Downloading SHA256SUMS.sig..."
if ! download "${SIG_URL}" "SHA256SUMS.sig"; then
  echo "❌ Could not download SHA256SUMS.sig for ${BETA_TAG}"
  echo "   Refusing to install without a verifiable release signature."
  exit 1
fi

###############################################
# 12) Verify SHA256SUMS signature (authenticity)
###############################################
echo "[+] Verifying SHA256SUMS signature..."
if ! command -v openssl >/dev/null 2>&1; then
  echo "❌ openssl is required to verify the release signature"
  exit 1
fi

printf '%s\n' "${PUBKEY_PEM}" > pubkey.pem
if ! openssl dgst -sha256 -verify pubkey.pem -signature SHA256SUMS.sig SHA256SUMS >/dev/null 2>&1; then
  echo "❌ SHA256SUMS signature verification FAILED, refusing to install"
  exit 1
fi
echo "✔ Signature OK (release authenticity verified)"

###############################################
# 13) Verify checksum (integrity)
###############################################
echo "[+] Verifying checksum..."
# Match the filename exactly, normalizing sha256sum's optional binary-mode '*'
# marker on the name (mirrors the Go verifyChecksum). The original line is printed
# unchanged so "sha256sum -c" still understands the marker.
awk -v f="${FILENAME}" '{n=$2; sub(/^\*/,"",n)} n==f' SHA256SUMS > CHECK
if [ ! -s CHECK ]; then
  echo "❌ Checksum entry not found for ${FILENAME}"
  exit 1
fi

sha256sum -c CHECK
echo "✔ Checksum OK"

###############################################
# 14) Extract ONLY the binary
###############################################
echo "[+] Extracting binary from tar.gz..."
tar -xzf "${FILENAME}" proxsave

if [ ! -f proxsave ]; then
  echo "❌ Binary 'proxsave' not found inside archive"
  exit 1
fi
chmod 755 proxsave

###############################################
# 15) Swap in the beta binary (keep a rollback copy)
###############################################
echo "[+] Backing up current binary -> ${TARGET_BIN}.prev"
cp -f "${TARGET_BIN}" "${TARGET_BIN}.prev"

echo "[+] Installing beta binary -> ${TARGET_BIN}"
# Conventional executable mode 0755 (owner rwx, group/other r-x). The binary runs
# as root and only root can replace it; the security check verifies it is
# root-owned and not group/other-writable, not an exact mode.
if ! mv proxsave "${TARGET_BIN}"; then
  echo "[!] Failed to install the beta binary (e.g. cross-device copy interrupted)."
  echo "   Rolling back to the previous binary."
  mv -f "${TARGET_BIN}.prev" "${TARGET_BIN}"
  chmod 755 "${TARGET_BIN}"
  exit 1
fi
chmod 755 "${TARGET_BIN}"

# Sanity-check the swapped binary actually runs before finalizing. A corrupt or
# incompatible binary is rolled back to the previous one immediately.
if ! "${TARGET_BIN}" --version >/dev/null 2>&1; then
  echo "❌ The installed beta binary failed to run; rolling back to the previous binary."
  mv -f "${TARGET_BIN}.prev" "${TARGET_BIN}"
  chmod 755 "${TARGET_BIN}"
  exit 1
fi

###############################################
# 16) Finalize locally (no re-download): --upgrade --localfile
###############################################
cd "${TARGET_DIR}"

echo "[+] Finalizing: ${TARGET_BIN} --upgrade --localfile"
# The beta binary is already swapped in and verified to run. If the local finalize
# fails (daemon migrate/restart, backup.env merge, permission fixes -- the parts
# that touch the live system), surface the rollback path explicitly: set -e would
# otherwise abort here and never print the footer that documents .prev.
if ! "${TARGET_BIN}" --upgrade --localfile; then
  echo "--------------------------------------------"
  echo "❌ Finalize failed. The beta binary IS installed but the local upgrade did"
  echo "   not complete (backup.env / daemon / permissions may be half-applied)."
  echo ""
  echo "   Re-run the finalize:   ${TARGET_BIN} --upgrade --localfile"
  echo "   Or roll back:          mv -f ${TARGET_BIN}.prev ${TARGET_BIN}"
  echo "--------------------------------------------"
  exit 1
fi

echo "--------------------------------------------"
echo "✔ Beta upgrade completed!"
echo " Version:  ${VERSION}"
echo " Binary:   ${TARGET_BIN}"
echo " Rollback: mv -f ${TARGET_BIN}.prev ${TARGET_BIN}"
echo ""
echo " You are now on a PRERELEASE. When a stable release of the same or newer"
echo " version ships, 'proxsave --upgrade' will move you back onto stable."
echo "--------------------------------------------"
