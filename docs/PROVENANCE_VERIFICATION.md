# Verify release authenticity

<!-- site-region: verify-release:start -->

## Verify a ProxSave release

ProxSave's installer and upgrade workflow verify a signed checksum file using a public
key pinned in the tool. A missing or invalid signature stops the operation; the selected
archive must also match its authenticated checksum. Build attestations provide an
additional, optional check of artifact identity and recorded provenance.

### Install or upgrade through the verified workflow

For a first installation, use the script in [Install ProxSave](INSTALL.md#fast-install).
It performs signature and checksum verification before installing the release. Keep a
trusted existing copy or recovery route available if you are upgrading a working host.

For an existing installation:

1. Run `proxsave` without arguments on an interactive terminal.
2. Choose **Maintenance** > **Upgrade** > **Check upgrade**. This opens the release
   check and displays the available version and release notes; it does not install it.
3. Inspect the result. When an update is available, choose **Run upgrade** to download,
   verify and install it. A failed signature or checksum is a reason to stop.
4. Read the final result, including configuration merging and daemon restart status.
   Choose **Daemon** > **Status** to check the running binary when using the resident
   daemon. A deferred or failed restart can leave the old process running.

The adjacent **Check config** action reviews missing configuration variables and offers
an explicit apply step. It is separate from checking for a new binary. See
[configuration upgrades](CONFIGURATION.md#editing-the-configuration-from-the-dashboard) for details.

The installed binary drives an in-place upgrade, including its verification. Only the
post-install finalize phase can be delegated to the new binary when both releases
support it. A fix to upgrade code in the new release cannot change the old binary that
is currently downloading it. If release instructions require the externally fetched
installer, follow [Which binary runs the upgrade](#which-binary-runs-the-upgrade).

### Verify a downloaded artifact with GitHub CLI

This optional check is useful before manually installing an artifact. Install `gh`
using the [prerequisites](#prerequisites), obtain the intended release asset, then set
the exact tag and filename:

```bash
TAG=vX.Y.Z
VER=${TAG#v}
ASSET="proxsave_${VER}_linux_amd64"
gh attestation verify "$ASSET" \
  --repo tis24dev/proxsave \
  --signer-workflow tis24dev/proxsave/.github/workflows/release.yml \
  --source-ref "refs/tags/${TAG}" \
  --deny-self-hosted-runners
```

Require a successful verifier exit status. This policy checks the artifact digest,
repository, release workflow, source reference and runner type. To require an independently
trusted commit, add the source-digest policy described in [What gets verified](#what-gets-verified).
The [manual signature procedure](#verify-a-download-manually) contains the pinned key and
signed-checksum commands. [Verification methods](#verification-methods) also covers archives,
JSON results and offline verification with previously obtained trust material.

Successful verification connects the artifact to the selected identity and recorded build.
It does not establish that the source, dependencies or workflow are safe or uncompromised.
The signed checksum and attestation serve different verification policies; keep those
limits in mind when evaluating a release.

### Respond to a failed check

Do not run the unverified binary or disable verification to finish the upgrade. Confirm
the tag, asset name and download completeness, then follow the matching
[verification error](#troubleshooting). Retry a clean download when appropriate. If the
failure remains, preserve the error and request support without replacing the working
installation. Verification of a release is separate from checking that a backup or
restore succeeds on your host.

<!-- site-region: verify-release:end -->

## Introduction

The release workflow creates signed build-provenance attestations for its binary,
archive and SBOM assets. Verification checks the artifact digest and signing identity
against the policy you request. The recorded provenance identifies the source and
build workflow; it does not prove that their contents are safe or uncompromised.

Public-repository attestations use Sigstore and a public transparency log. See
[GitHub's explanation of artifact attestations](https://docs.github.com/en/actions/concepts/security/artifact-attestations)
for their guarantees and limits.

Proxsave publishes a single build target, `linux/amd64`. The release assets for a tag `vX.Y.Z` are:

```text
proxsave_X.Y.Z_linux_amd64                     # uncompressed binary
proxsave_X.Y.Z_linux_amd64.tar.gz              # binary + LICENSE + README
proxsave_X.Y.Z_linux_amd64.tar.gz.sbom.cdx.json
SHA256SUMS
SHA256SUMS.sig
```

GoReleaser drops the leading `v` from the version in filenames, so tag `v0.29.0` produces `proxsave_0.29.0_linux_amd64`. There are no macOS or Windows binaries.

## Release signature (SHA256SUMS.sig)

In addition to the SLSA attestations described below, every release ships a detached signature of its `SHA256SUMS` file: `SHA256SUMS.sig`. It is an **ECDSA P-256 / SHA-256** signature produced in CI with a private key that exists only as a GitHub Actions secret.

**This is what the tooling enforces automatically.** `install.sh` and `proxsave --upgrade` download `SHA256SUMS.sig` and verify it against a public key **pinned in the tool itself** before trusting `SHA256SUMS` (and then the archive checksum). A missing or invalid signature aborts the install or upgrade; there is no fallback to checksum-only. Because the public key is pinned in the client, an attacker cannot substitute their own key: only the project's private key can produce a signature that verifies. The release workflow also validates the signing key against the same pinned public key before publishing.

Pinned public key (sha256/DER fingerprint `fdbbba66cdb770b85a728c8aee0b920b4cd244c84f4fc5a0065188fbe9a5eddb`):

```text
-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAElks05mPtm1vm0YtHlSGX1HlgdXjn
liDJEnB+RgiWOQR+6xLWeX7PyauuMxUh/HNnvBQAokK91fLWes4r9Xlwzw==
-----END PUBLIC KEY-----
```

### Verify a download manually

```bash
TAG=vX.Y.Z          # the release you downloaded, e.g. v0.29.0
base="https://github.com/tis24dev/proxsave/releases/download/${TAG}"
curl -fsSLO "${base}/SHA256SUMS"
curl -fsSLO "${base}/SHA256SUMS.sig"

# Save the pinned public key above to proxsave_pub.pem, then verify authenticity:
openssl dgst -sha256 -verify proxsave_pub.pem -signature SHA256SUMS.sig SHA256SUMS
#   -> "Verified OK"

# Then check the archive you downloaded against the now-authenticated checksums:
sha256sum --ignore-missing -c SHA256SUMS
```

> The release signature (above) and the SLSA attestations (below) are complementary: the signature is the lightweight check the installer enforces with no extra tooling, while attestations add an independently verifiable, transparency-logged build provenance via the GitHub CLI.

### How an operator gets a verified release

Follow [Verify a ProxSave release](#verify-a-proxsave-release) for the dashboard
procedure. **Check upgrade** checks availability; **Run upgrade** installs the release.
**Check config** is the separate action for reviewing and merging configuration keys.
The signature and checksum verification uses the same implementation as direct upgrade.
Command equivalents are in [CLI_REFERENCE.md](CLI_REFERENCE.md).

### Which binary runs the upgrade

An in-place `proxsave --upgrade`, dashboard row included, is executed by the binary already on the host. That is deliberate for the verification itself: a freshly downloaded binary cannot be the party that verifies itself, so the release check, the download, the signature and checksum check, and the install all stay with the release you are upgrading FROM. Only the post-install finalize phase is handed to the newly installed binary, and only when both ends are new enough to do it. The practical consequence is that a fix to the upgrade flow shipped in a new release cannot help a host upgrading from an older one.

When a release note says the upgrade path itself changed, take the externally fetched route instead. The script downloads the release and verifies `SHA256SUMS.sig` itself, swaps the binary in, and only then runs `--upgrade --localfile` on it, so the finalize is the new release's code:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" -- --upgrade
```

See [SECURITY.md](SECURITY.md#threat-model) for the same split stated against the trust boundary.

## Why attestations matter

A successful verification establishes that the artifact matches the attested digest
and satisfies the selected identity policy. Provenance also records source and build
information that can be checked against an independently trusted release.

It does not establish that only maintainers can publish releases, or rule out malicious
source, dependencies or a compromised build workflow. Inspect the selected policy in
[What gets verified](#what-gets-verified); repository identity alone is less restrictive
than also requiring the expected workflow, source reference and commit.

## Prerequisites

To verify attestations you need the GitHub CLI (`gh`). It runs on any OS even though the Proxsave binary is `linux/amd64` only.

```bash
# Debian/Ubuntu
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null
sudo apt update
sudo apt install gh

# Fedora/RHEL/CentOS
sudo dnf install gh

# Arch Linux
sudo pacman -S github-cli
```

See the [GitHub CLI install docs](https://github.com/cli/cli#installation) for other platforms.

## Verification methods

The examples below use a small setup block so you can paste any release tag:

```bash
TAG=vX.Y.Z                       # the release you are verifying, e.g. v0.29.0
VER=${TAG#v}                     # version without the leading v (used in asset names)
ASSET="proxsave_${VER}_linux_amd64"
```

### Method 1: quick verification (single binary)

```bash
# Download the binary
wget "https://github.com/tis24dev/proxsave/releases/download/${TAG}/${ASSET}"

# Verify the attestation
gh attestation verify "${ASSET}" --repo tis24dev/proxsave
```

**Expected output:**
```text
Loaded digest sha256:abc123... for file://proxsave_0.29.0_linux_amd64
Loaded 1 attestation from GitHub API
Verification succeeded!

sha256:abc123... was attested by:
REPO               PREDICATE_TYPE                  WORKFLOW
tis24dev/proxsave  https://slsa.dev/provenance/v1  .github/workflows/release.yml@refs/tags/v0.29.0
```

### Method 2: verify both artifacts

Both the uncompressed binary and the `.tar.gz` are attested (the attestation subject-path `build/proxsave_*` also covers the SBOM document).

```bash
cd ~/downloads
wget "https://github.com/tis24dev/proxsave/releases/download/${TAG}/${ASSET}"
wget "https://github.com/tis24dev/proxsave/releases/download/${TAG}/${ASSET}.tar.gz"

gh attestation verify "${ASSET}" --repo tis24dev/proxsave &&
  gh attestation verify "${ASSET}.tar.gz" --repo tis24dev/proxsave
```

### Method 3: verification with JSON output

For integrating verification into scripts or for detailed analysis.

```bash
gh attestation verify "${ASSET}" \
  --repo tis24dev/proxsave \
  --format json | jq
```

Successful `--format json` output is an array, with one entry per verified
attestation. Each entry contains `attestation` and `verificationResult`. For example,
extract the attested subjects with:

```bash
set -o pipefail
gh attestation verify "${ASSET}" --repo tis24dev/proxsave --format json \
  | jq 'map(.verificationResult.statement.subject)'
```

Keep the verifier's exit status when automating this check; formatted JSON alone
is not a substitute for a successful verification.

### Method 4: offline verification (with downloaded bundle)

Prepare the artifact, attestation and trust material on a connected Linux host:

```bash
# The artifact must already be present
gh attestation download "${ASSET}" --repo tis24dev/proxsave
gh attestation trusted-root > trusted_root.jsonl

# Linux download filenames are derived from the artifact digest
DIGEST=$(sha256sum "${ASSET}" | cut -d' ' -f1)
BUNDLE="sha256:${DIGEST}.jsonl"
test -s "$BUNDLE" && test -s trusted_root.jsonl
```

Transfer those three files through your trusted channel. On the offline host, set
`ASSET` to the transferred artifact, recompute `DIGEST` and `BUNDLE` as above, then run:

```bash
gh attestation verify "${ASSET}" \
  --bundle "$BUNDLE" \
  --custom-trusted-root trusted_root.jsonl \
  --repo tis24dev/proxsave
```

The trust-root file must come from the trusted preparation step, not from an
untrusted source alongside a suspect binary. The commands and filename convention
are documented by [GitHub CLI download](https://cli.github.com/manual/gh_attestation_download)
and [trusted-root](https://cli.github.com/manual/gh_attestation_trusted-root).

## A complete verify-and-install example (Linux)

```bash
#!/bin/bash
set -euo pipefail

TAG=vX.Y.Z                       # the release you want, e.g. v0.29.0
VER=${TAG#v}
ASSET="proxsave_${VER}_linux_amd64"

echo "Downloading ${ASSET}..."
wget -q "https://github.com/tis24dev/proxsave/releases/download/${TAG}/${ASSET}"

echo "Verifying attestation..."
if gh attestation verify "${ASSET}" --repo tis24dev/proxsave; then
    echo "Attestation verified."
else
    echo "Attestation verification failed."
    rm -f "${ASSET}"
    exit 1
fi

chmod +x "${ASSET}"
sudo mv "${ASSET}" /usr/local/bin/proxsave
proxsave --version
```

The recipe above is a connected, manual installation with repository-scoped attestation verification. Offline verification needs the files prepared in Method 4. For a first install the recommended path is the install script, which performs the `SHA256SUMS.sig` signature check automatically:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
```

On a host that already runs ProxSave, use **Maintenance** > **Upgrade** > **Check upgrade**, inspect the result and select **Run upgrade** when available; see [How an operator gets a verified release](#how-an-operator-gets-a-verified-release).

## What gets verified

The examples using only `--repo` check artifact integrity and repository identity.
They do not pin a particular release workflow, expected commit or runner type.

| Check | Policy in the examples above |
|-------|------------------------------|
| Artifact digest | Must match the attested subject |
| Repository identity | Must match `tis24dev/proxsave` |
| Signature and trust | Validated by the verifier using its trusted roots and signed attestation material |
| Predicate type | Defaults to SLSA provenance v1; this is not a general SLSA certification |
| Commit, workflow and runner | Reported provenance must be distinguished from an explicitly required policy |

To require the release workflow, the chosen source tag and a GitHub-hosted runner:

```bash
gh attestation verify "${ASSET}" \
  --repo tis24dev/proxsave \
  --signer-workflow tis24dev/proxsave/.github/workflows/release.yml \
  --source-ref "refs/tags/${TAG}" \
  --deny-self-hosted-runners
```

To pin an independently trusted commit, also pass `--source-digest` with that commit
SHA. For offline use, add Method 4's bundle and trust-root arguments. See the
[GitHub CLI verification policy](https://cli.github.com/manual/gh_attestation_verify).

## Troubleshooting

The variables `TAG`, `VER`, and `ASSET` below are the ones defined in [Verification methods](#verification-methods).

### Error: "no attestations found"

**Cause**: no matching attestation was found for this artifact and policy. Check the selected asset and release; older releases may predate attestations.

**Solution**:
```bash
gh release view "${TAG}" --repo tis24dev/proxsave
# Older releases may not include attestation/provenance data.
```

### Error: "gh: command not found"

**Cause**: GitHub CLI is not installed or not in PATH.

**Solution**: install `gh` following the Prerequisites section above.

### Error: "failed to verify signature"

**Cause**: the file may have been tampered with or corrupted.

**Solution**:
```bash
rm -f "${ASSET}"
wget "https://github.com/tis24dev/proxsave/releases/download/${TAG}/${ASSET}"
gh attestation verify "${ASSET}" --repo tis24dev/proxsave
```

If the problem persists, **do not use the binary** and report the issue on GitHub.

### Error: "attestation verification failed: subject digest mismatch"

**Cause**: the SHA256 hash of the file does not match the attested one (partial download, modification, or corruption).

**Solution**:
```bash
ls -lh "${ASSET}"
rm -f "${ASSET}"
wget "https://github.com/tis24dev/proxsave/releases/download/${TAG}/${ASSET}"

# Optional: authenticate the checksum file before checking the artifact
# Save the pinned public key from this guide as proxsave_pub.pem first
wget "https://github.com/tis24dev/proxsave/releases/download/${TAG}/SHA256SUMS"
wget "https://github.com/tis24dev/proxsave/releases/download/${TAG}/SHA256SUMS.sig"
openssl dgst -sha256 -verify proxsave_pub.pem -signature SHA256SUMS.sig SHA256SUMS &&
  sha256sum --ignore-missing -c SHA256SUMS
```

### Error: "HTTP 401: Bad credentials"

**Cause**: GitHub CLI is not authenticated or the token expired.

**Solution**:
```bash
gh auth login
# or
export GITHUB_TOKEN=your_personal_access_token
```

### Slow verification or timeout

**Cause**: connection issues to the transparency log (Rekor).

**Solution**: use offline verification if you already downloaded the attestation bundle (Method 4).

## Security considerations

### Trust model

Attestations are based on:
1. **GitHub Actions OIDC**: GitHub signs attestations using short-lived OIDC tokens
2. **Sigstore/Rekor**: a public transparency log that records all attestations
3. **SLSA framework**: an industry standard for provenance metadata

The verification depends on its trusted identity and certificate roots, as well as trust in the selected source and build workflow. The pinned-key `SHA256SUMS.sig` signature uses a separate signing key. Both checks still depend on the release workflow and protection of its credentials; neither proves the program is free of vulnerabilities.

### Transparency log (Rekor)

Every attestation is publicly recorded and searchable at https://search.sigstore.dev/ (filter by `tis24dev/proxsave`).

Benefits:
- Immutable, publicly auditable registry
- Verifiable timestamps
- No secrets to manage (keyless signing)

### Best practices

1. **Always verify before use**: do not run unverified binaries.
2. **Use HTTPS**: always download from `https://github.com`.
3. **Verify the repository**: ensure it is `tis24dev/proxsave`.
4. **Keep `gh` updated**: newer versions carry the latest verification logic.
5. **Automate**: integrate verification into your deployment scripts.
6. **Archive attestations**: save attestation bundles for future audits.

## References

- [GitHub Docs - Artifact Attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations)
- [GitHub CLI - attestation verify](https://cli.github.com/manual/gh_attestation_verify)
- [SLSA Framework](https://slsa.dev/)
- [Sigstore Project](https://www.sigstore.dev/)
- [Rekor Transparency Log](https://docs.sigstore.dev/logging/overview/)
- [In-Toto Attestations](https://in-toto.io/)

## Support

If you have problems with attestation verification:
1. Check this documentation for troubleshooting.
2. Make sure you have the latest `gh` CLI.
3. Open an issue on https://github.com/tis24dev/proxsave/issues.

**Security note**: if you suspect an attestation has been forged or compromised, **do not use the binary** and report it by opening a private security advisory on GitHub.
