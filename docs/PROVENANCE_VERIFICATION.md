# Provenance Attestation Verification

## Introduction

Every Proxsave release binary includes cryptographically signed provenance attestations that prove:
- The binary was built from this repository
- The binary was built using GitHub Actions
- The binary has not been tampered with after the build
- The build process is traceable and verifiable

Attestations use the SLSA (Supply-chain Levels for Software Artifacts) standard and are registered in an immutable public transparency log (Sigstore).

## Why Attestations Matter

Provenance attestations protect against:
- **Supply chain attacks**: Verifies that the binary comes from the official repository
- **Tampering**: Guarantees that no one has modified the binary after the build
- **Compromises**: Proves that the binary was built in a trusted environment (GitHub Actions)
- **Unauthorized builds**: Confirms that only maintainers can create releases

## Prerequisites

To verify attestations, you need to install GitHub CLI (`gh`).

### Linux

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

### macOS

```bash
brew install gh
```

### Windows

```powershell
# With winget
winget install --id GitHub.cli

# With Chocolatey
choco install gh

# With Scoop
scoop install gh
```

## Verification Methods

### Method 1: Quick Verification (Single Binary)

This is the simplest method to verify a single downloaded binary.

```bash
# Download the binary for your platform
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxsave-linux-amd64

# Verify the attestation
gh attestation verify proxsave-linux-amd64 --repo tis24dev/proxsave
```

**Expected output:**
```
Loaded digest sha256:abc123... for file://proxsave-linux-amd64
Loaded 1 attestation from GitHub API
✓ Verification succeeded!

sha256:abc123... was attested by:
REPO                        PREDICATE_TYPE                  WORKFLOW
tis24dev/proxsave     https://slsa.dev/provenance/v1  .github/workflows/release.yml@refs/tags/v0.9.0
```

### Method 2: Verify All Artifacts

Verify all binaries downloaded in a directory.

```bash
# Download all the binaries you need
cd ~/downloads
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxsave-linux-amd64
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxsave-darwin-amd64

# Verify all together
gh attestation verify proxsave-* --repo tis24dev/proxsave
```

### Method 3: Verification with JSON Output

For integrating verification into scripts or for detailed analysis.

```bash
gh attestation verify proxsave-linux-amd64 \
  --repo tis24dev/proxsave \
  --format json | jq
```

**JSON output** (example):
```json
{
  "verificationResult": {
    "verifiedTimestamps": [
      {
        "timestamp": "2024-11-28T10:30:45Z",
        "source": "Rekor"
      }
    ],
    "statement": {
      "_type": "https://in-toto.io/Statement/v1",
      "subject": [
        {
          "name": "proxsave-linux-amd64",
          "digest": {
            "sha256": "abc123..."
          }
        }
      ],
      "predicateType": "https://slsa.dev/provenance/v1",
      "predicate": {
        "buildDefinition": {
          "buildType": "https://slsa.dev/provenance/github/actions/v1",
          "externalParameters": {
            "workflow": {
              "ref": "refs/tags/v0.9.0",
              "repository": "https://github.com/tis24dev/proxsave"
            }
          }
        }
      }
    }
  }
}
```

### Method 4: Offline Verification (with downloaded bundle)

Useful for air-gapped environments or for archiving attestations.

```bash
# Download the attestation as a bundle
gh attestation download proxsave-linux-amd64 \
  --repo tis24dev/proxsave \
  --output attestation.jsonl

# Verify offline using the bundle
gh attestation verify proxsave-linux-amd64 \
  --bundle attestation.jsonl \
  --repo tis24dev/proxsave
```

## Complete Practical Examples

### Example 1: Linux - Verification and Installation

```bash
#!/bin/bash
set -e

# 1. Download the binary
echo "Downloading binary..."
wget -q https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxsave-linux-amd64

# 2. Verify the attestation
echo "Verifying attestation..."
if gh attestation verify proxsave-linux-amd64 --repo tis24dev/proxsave; then
    echo "✓ Attestation verified successfully!"
else
    echo "✗ Attestation verification failed!"
    rm proxsave-linux-amd64
    exit 1
fi

# 3. Make executable
chmod +x proxsave-linux-amd64

# 4. Move to /usr/local/bin
sudo mv proxsave-linux-amd64 /usr/local/bin/proxsave

echo "Installation complete!"
proxsave --version
```

### Example 2: macOS - Verification with Homebrew Alternative

```bash
#!/bin/bash
set -e

VERSION="v0.9.0"
BINARY="proxsave-darwin-$(uname -m)"
URL="https://github.com/tis24dev/proxsave/releases/download/${VERSION}/${BINARY}"

# Download
echo "Downloading ${BINARY}..."
curl -L -o proxsave "${URL}"

# Verify
echo "Verifying provenance..."
gh attestation verify proxsave --repo tis24dev/proxsave || {
    echo "Verification failed!"
    rm proxsave
    exit 1
}

# Install
chmod +x proxsave
sudo mv proxsave /usr/local/bin/
echo "Installed successfully!"
```

### Example 3: Windows PowerShell - Verification and Installation

```powershell
# Download binary
$version = "v0.9.0"
$binary = "proxsave-windows-amd64.exe"
$url = "https://github.com/tis24dev/proxsave/releases/download/$version/$binary"

Write-Host "Downloading $binary..." -ForegroundColor Cyan
Invoke-WebRequest -Uri $url -OutFile "proxsave.exe"

# Verify attestation
Write-Host "Verifying attestation..." -ForegroundColor Cyan
$result = gh attestation verify proxsave.exe --repo tis24dev/proxsave

if ($LASTEXITCODE -eq 0) {
    Write-Host "✓ Attestation verified successfully!" -ForegroundColor Green

    # Move to Program Files
    $destPath = "$env:ProgramFiles\ProxmoxBackup"
    New-Item -ItemType Directory -Force -Path $destPath | Out-Null
    Move-Item -Path "proxsave.exe" -Destination "$destPath\proxsave.exe" -Force

    # Add to PATH if not already there
    $currentPath = [Environment]::GetEnvironmentVariable("Path", "Machine")
    if ($currentPath -notlike "*$destPath*") {
        [Environment]::SetEnvironmentVariable("Path", "$currentPath;$destPath", "Machine")
        Write-Host "Added to system PATH" -ForegroundColor Green
    }

    Write-Host "Installation complete!" -ForegroundColor Green
} else {
    Write-Host "✗ Attestation verification failed!" -ForegroundColor Red
    Remove-Item "proxsave.exe"
    exit 1
}
```

## What Gets Verified

When you run `gh attestation verify`, the following checks are performed:

| Verification | Description |
|--------------|-------------|
| ✓ **Source repository** | Confirms that the build comes from `github.com/tis24dev/proxsave` |
| ✓ **Commit SHA** | Verifies the exact commit used for the build |
| ✓ **Workflow** | Checks that `.github/workflows/release.yml` was used |
| ✓ **Build environment** | Confirms that the build occurred on GitHub-hosted runner |
| ✓ **SHA256 integrity** | Calculates the file hash and compares it with the attested one |
| ✓ **Cryptographic signature** | Verifies the OIDC/Sigstore signature on the attestation |
| ✓ **Rekor timestamp** | Checks the immutable record in the transparency log |
| ✓ **SLSA compliance** | Verifies that the attestation complies with the SLSA v1 standard |

## Troubleshooting

### Error: "no attestations found"

**Cause**: The release does not include attestations (release prior to this feature).

**Solution**:
```bash
# Check the release version
gh release view v0.9.0 --repo tis24dev/proxsave

# Note: older releases may not include attestations/provenance data.
```

### Error: "gh: command not found"

**Cause**: GitHub CLI is not installed or not in PATH.

**Solution**: Install `gh` following the instructions in the Prerequisites section above.

### Error: "failed to verify signature"

**Cause**: The file may have been tampered with or corrupted.

**Solution**:
```bash
# Re-download the binary
rm proxsave-linux-amd64
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxsave-linux-amd64

# Retry verification
gh attestation verify proxsave-linux-amd64 --repo tis24dev/proxsave
```

If the problem persists, **DO NOT use the binary** and report the issue by opening an issue on GitHub.

### Error: "attestation verification failed: subject digest mismatch"

**Cause**: The SHA256 hash of the file does not match the one in the attestation.

**Possible causes**:
- File downloaded partially (interrupted download)
- File modified after download
- File corrupted during transfer

**Solution**:
```bash
# Check file size
ls -lh proxsave-linux-amd64

# Re-download completely
rm proxsave-linux-amd64
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxsave-linux-amd64

# Manual checksum verification (optional)
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/SHA256SUMS
sha256sum -c SHA256SUMS
```

### Error: "HTTP 401: Bad credentials"

**Cause**: GitHub CLI is not authenticated or the token has expired.

**Solution**:
```bash
# Authenticate with GitHub
gh auth login

# Or use a token
export GITHUB_TOKEN=your_personal_access_token
```

### Slow verification or timeout

**Cause**: Connection issues to the transparency log (Rekor).

**Solution**:
```bash
# Use offline verification if you've already downloaded the attestation
gh attestation download proxsave-linux-amd64 --repo tis24dev/proxsave -o attestation.jsonl
gh attestation verify proxsave-linux-amd64 --bundle attestation.jsonl --repo tis24dev/proxsave
```

## Security Considerations

### Trust Model

Attestations are based on:
1. **GitHub Actions OIDC**: GitHub signs attestations using short-lived OIDC tokens
2. **Sigstore/Rekor**: Public transparency log that records all attestations
3. **SLSA Framework**: Industry standard for provenance metadata

**What this means**: You must trust GitHub as the root of trust. If GitHub is compromised, attestations could be forged.

### Transparency Log (Rekor)

Every attestation is publicly recorded on https://search.sigstore.dev/

**Benefits**:
- Immutable and publicly auditable registry
- Verifiable timestamps
- No secrets to manage (keyless signing)

**What you can check**:
```bash
# Search for attestations for this repository
open "https://search.sigstore.dev/?logIndex=&email=&hash=&logEntry=&uuid="
# Filter by: tis24dev/proxsave
```

### Comparison with GPG Signing

| Aspect | Attestations (new) | GPG Signing (old) |
|--------|-------------------|-------------------|
| **Key management** | None (keyless) | Requires private key management |
| **Key rotation** | Automatic | Manual and complex |
| **Revocation** | Timestamp-based | Requires key revocation |
| **Transparency** | Public registry | Only if published on keyserver |
| **Build metadata** | Complete SLSA provenance | Only checksum signature |
| **Verification** | `gh attestation verify` | `gpg --verify` |
| **Trust model** | GitHub OIDC + Sigstore | Web of trust / keyserver |

### Best Practices

1. **Always verify before use**: Do not run unverified binaries
2. **Use HTTPS**: Always download from `https://github.com`
3. **Verify the repository**: Ensure it's `tis24dev/proxsave`
4. **Update gh CLI**: Keep GitHub CLI updated for the latest security features
5. **Automation**: Integrate verification into your deployment scripts
6. **Archive attestations**: Save attestations for future audits

## Migration from GPG

### For Users Who Used Old GPG Signing

**What changed**:
- ✗ We no longer generate `SHA256SUMS.asc` (GPG signature)
- ✓ We generate SLSA provenance attestations
- ✓ We continue to generate `SHA256SUMS` (plain checksums)

**Previous releases** (v0.9.0 and earlier):
- Use GPG signing (key: CD28A21CC11B270E)
- Verify with: `gpg --verify SHA256SUMS.asc SHA256SUMS`

**New releases** (v0.9.1+):
- Use GitHub attestations
- Verify with: `gh attestation verify`

### Migration Script

If you have scripts that use GPG, here's how to migrate them:

**Before (GPG)**:
```bash
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxsave-linux-amd64
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/SHA256SUMS
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/SHA256SUMS.asc
gpg --verify SHA256SUMS.asc SHA256SUMS
sha256sum -c SHA256SUMS
```

**After (Attestations)**:
```bash
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.1/proxsave-linux-amd64
gh attestation verify proxsave-linux-amd64 --repo tis24dev/proxsave
```

Much simpler! 🎉

## References

- [GitHub Docs - Artifact Attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations)
- [GitHub CLI - attestation verify](https://cli.github.com/manual/gh_attestation_verify)
- [SLSA Framework](https://slsa.dev/)
- [Sigstore Project](https://www.sigstore.dev/)
- [Rekor Transparency Log](https://docs.sigstore.dev/logging/overview/)
- [In-Toto Attestations](https://in-toto.io/)

## Support

If you have problems with attestation verification:
1. Check this documentation for troubleshooting
2. Verify you have the latest version of `gh` CLI
3. Open an issue on https://github.com/tis24dev/proxsave/issues

**Security note**: If you suspect that an attestation has been forged or compromised, **DO NOT use the binary** and immediately report it by opening a private security advisory on GitHub.
