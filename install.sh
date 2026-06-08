#!/usr/bin/env bash

set -euo pipefail

###############################################
# Optional arguments: --new-install / --cli
###############################################
INSTALL_FLAG="--install"   # default mode
CLI_FLAG=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --new-install)
      INSTALL_FLAG="--new-install"
      ;;
    --install)
      INSTALL_FLAG="--install"
      ;;
    --cli)
      CLI_FLAG="--cli"
      ;;
    *)
      echo "❌ Unknown argument: $1"
      exit 1
      ;;
  esac
  shift
done

###############################################
# 1) Ensure running as root
###############################################
if [ "$EUID" -ne 0 ]; then
  echo "❌ Please run as root"
  exit 1
fi

###############################################
# 2) Config
###############################################
REPO="tis24dev/proxsave"
TARGET_DIR="/opt/proxsave"
BUILD_DIR="${TARGET_DIR}/build"
TARGET_BIN="${BUILD_DIR}/proxsave"

# Pinned release-signing public key (ECDSA P-256). The matching private key lives
# only in the project's GitHub Actions secret, so an archive whose SHA256SUMS does
# not verify against this key is rejected. Fingerprint (sha256 of DER):
# fdbbba66cdb770b85a728c8aee0b920b4cd244c84f4fc5a0065188fbe9a5eddb
PUBKEY_PEM='-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAElks05mPtm1vm0YtHlSGX1HlgdXjn
liDJEnB+RgiWOQR+6xLWeX7PyauuMxUh/HNnvBQAokK91fLWes4r9Xlwzw==
-----END PUBLIC KEY-----'

###############################################
# 3) OS/ARCH detection
###############################################
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH_RAW="$(uname -m)"

if [ "${OS}" != "linux" ]; then
  echo "❌ Only Linux systems are supported"
  exit 1
fi

case "$ARCH_RAW" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "❌ Unsupported architecture: ${ARCH_RAW}"
    exit 1
    ;;
esac

echo "--------------------------------------------"
echo " ProxSave Installer"
echo " Mode: ${INSTALL_FLAG}"
echo " OS:   ${OS}"
echo " Arch: ${ARCH}"
echo " Dir:  ${TARGET_DIR}"
echo "--------------------------------------------"

###############################################
# 4) HTTP helper (curl/wget)
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
# 5) Fetch latest release tag
###############################################
LATEST_JSON="$(fetch "https://api.github.com/repos/${REPO}/releases/latest")"

LATEST_TAG=""
if command -v jq >/dev/null 2>&1; then
  LATEST_TAG="$(jq -r '.tag_name // empty' <<<"${LATEST_JSON}" 2>/dev/null || true)"
fi

if [ -z "${LATEST_TAG}" ] && [[ ${LATEST_JSON} =~ \"tag_name\"[[:space:]]*:[[:space:]]*\"([^\"]+)\" ]]; then
  LATEST_TAG="${BASH_REMATCH[1]}"
fi

if [ -z "${LATEST_TAG}" ]; then
  echo "❌ Could not detect latest release tag"
  exit 1
fi

echo "📦 Latest tag: ${LATEST_TAG}"

VERSION="${LATEST_TAG#v}"

###############################################
# 6) Build correct filename (ARCHIVE)
###############################################
FILENAME="proxsave_${VERSION}_${OS}_${ARCH}.tar.gz"

BINARY_URL="https://github.com/${REPO}/releases/download/${LATEST_TAG}/${FILENAME}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${LATEST_TAG}/SHA256SUMS"
SIG_URL="https://github.com/${REPO}/releases/download/${LATEST_TAG}/SHA256SUMS.sig"

echo "➡ Archive URL:   ${BINARY_URL}"
echo "➡ Checksum URL:  ${CHECKSUM_URL}"
echo "➡ Signature URL: ${SIG_URL}"

###############################################
# 7) Prepare directories
###############################################
mkdir -p "${BUILD_DIR}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
cd "${TMP_DIR}"

###############################################
# 8) Download archive, checksum and signature
###############################################
echo "[+] Downloading archive..."
download "${BINARY_URL}" "${FILENAME}"

echo "[+] Downloading SHA256SUMS..."
download "${CHECKSUM_URL}" "SHA256SUMS"

echo "[+] Downloading SHA256SUMS.sig..."
if ! download "${SIG_URL}" "SHA256SUMS.sig"; then
  echo "❌ Could not download SHA256SUMS.sig for ${LATEST_TAG}"
  echo "   Refusing to install without a verifiable release signature."
  exit 1
fi

###############################################
# 9) Verify SHA256SUMS signature (authenticity)
###############################################
echo "[+] Verifying SHA256SUMS signature..."
if ! command -v openssl >/dev/null 2>&1; then
  echo "❌ openssl is required to verify the release signature"
  exit 1
fi

printf '%s\n' "${PUBKEY_PEM}" > pubkey.pem
if ! openssl dgst -sha256 -verify pubkey.pem -signature SHA256SUMS.sig SHA256SUMS >/dev/null 2>&1; then
  echo "❌ SHA256SUMS signature verification FAILED — refusing to install"
  exit 1
fi
echo "✔ Signature OK (release authenticity verified)"

###############################################
# 10) Verify checksum (integrity)
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
# 11) Extract ONLY the binary
###############################################
echo "[+] Extracting binary from tar.gz..."
tar -xzf "${FILENAME}" proxsave

if [ ! -f proxsave ]; then
  echo "❌ Binary 'proxsave' not found inside archive"
  exit 1
fi

###############################################
# 12) Install binary
###############################################
echo "[+] Installing binary -> ${TARGET_BIN}"
mv proxsave "${TARGET_BIN}"
chmod +x "${TARGET_BIN}"

###############################################
# 13) Run internal installer (--install or --new-install)
###############################################
cd "${TARGET_DIR}"

BINARY_ARGS=("${INSTALL_FLAG}")
if [[ -n "${CLI_FLAG}" ]]; then
  BINARY_ARGS+=("${CLI_FLAG}")
fi

echo "[+] Running: ${TARGET_BIN} ${BINARY_ARGS[*]}"
"${TARGET_BIN}" "${BINARY_ARGS[@]}"

echo "--------------------------------------------"
echo "✔ Installation completed successfully!"
echo " Directory: ${TARGET_DIR}"
echo " Binary:    ${TARGET_BIN}"
echo " Mode:      ${INSTALL_FLAG}"
echo "--------------------------------------------"
