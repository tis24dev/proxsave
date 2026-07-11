# Provenance Attestation Verification

## Introduction

Every Proxsave release binary includes cryptographically signed provenance attestations that prove:
- The binary was built from this repository
- The binary was built using GitHub Actions
- The binary has not been tampered with after the build
- The build process is traceable and verifiable

Attestations use the SLSA (Supply-chain Levels for Software Artifacts) standard and are registered in an immutable public transparency log (Sigstore).

Proxsave publishes a single build target, `linux/amd64`. The release assets for a tag `vX.Y.Z` are:

```
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

## Why attestations matter

Provenance attestations protect against:
- **Supply chain attacks**: verifies that the binary comes from the official repository
- **Tampering**: guarantees that no one has modified the binary after the build
- **Compromises**: proves that the binary was built in a trusted environment (GitHub Actions)
- **Unauthorized builds**: confirms that only maintainers can create releases

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
```
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

gh attestation verify proxsave_${VER}_linux_amd64* --repo tis24dev/proxsave
```

### Method 3: verification with JSON output

For integrating verification into scripts or for detailed analysis.

```bash
gh attestation verify "${ASSET}" \
  --repo tis24dev/proxsave \
  --format json | jq
```

**JSON output** (example):
```json
{
  "verificationResult": {
    "verifiedTimestamps": [
      { "timestamp": "2025-06-20T10:30:45Z", "source": "Rekor" }
    ],
    "statement": {
      "_type": "https://in-toto.io/Statement/v1",
      "subject": [
        {
          "name": "proxsave_0.29.0_linux_amd64",
          "digest": { "sha256": "abc123..." }
        }
      ],
      "predicateType": "https://slsa.dev/provenance/v1",
      "predicate": {
        "buildDefinition": {
          "buildType": "https://slsa.dev/provenance/github/actions/v1",
          "externalParameters": {
            "workflow": {
              "ref": "refs/tags/v0.29.0",
              "repository": "https://github.com/tis24dev/proxsave"
            }
          }
        }
      }
    }
  }
}
```

### Method 4: offline verification (with downloaded bundle)

Useful for air-gapped environments or for archiving attestations.

```bash
# Download the attestation as a bundle
gh attestation download "${ASSET}" \
  --repo tis24dev/proxsave \
  --output attestation.jsonl

# Verify offline using the bundle
gh attestation verify "${ASSET}" \
  --bundle attestation.jsonl \
  --repo tis24dev/proxsave
```

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

For most users the recommended path is still the install script, which performs the `SHA256SUMS.sig` signature check automatically:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
```

## What gets verified

When you run `gh attestation verify`, the following checks are performed:

| Verification | Description |
|--------------|-------------|
| Source repository | Confirms the build comes from `github.com/tis24dev/proxsave` |
| Commit SHA | Verifies the exact commit used for the build |
| Workflow | Checks that `.github/workflows/release.yml` was used |
| Build environment | Confirms the build occurred on a GitHub-hosted runner |
| SHA256 integrity | Recomputes the file hash and compares it with the attested one |
| Cryptographic signature | Verifies the OIDC/Sigstore signature on the attestation |
| Rekor timestamp | Checks the immutable record in the transparency log |
| SLSA compliance | Verifies the attestation complies with the SLSA v1 standard |

## Troubleshooting

The variables `TAG`, `VER`, and `ASSET` below are the ones defined in [Verification methods](#verification-methods).

### Error: "no attestations found"

**Cause**: the release does not include attestations (a release created before this feature).

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

# Optional: cross-check against the signed checksums
wget "https://github.com/tis24dev/proxsave/releases/download/${TAG}/SHA256SUMS"
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

**What this means**: you must trust GitHub as the root of trust for the attestations. If GitHub is compromised, attestations could be forged. The pinned-key `SHA256SUMS.sig` signature is an independent check that does not rely on GitHub as a signer.

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
