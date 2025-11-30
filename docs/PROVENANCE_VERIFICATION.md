# Verifica delle Attestazioni di Provenienza

## Introduzione

Ogni binario di release di Proxmox Backup Go include attestazioni di provenienza firmate crittograficamente che provano:
- Il binario è stato costruito da questo repository
- Il binario è stato costruito usando GitHub Actions
- Il binario non è stato manomesso dopo la build
- Il processo di build è tracciabile e verificabile

Le attestazioni utilizzano lo standard SLSA (Supply-chain Levels for Software Artifacts) e sono registrate in un transparency log pubblico immutabile (Sigstore).

## Perché le Attestazioni sono Importanti

Le attestazioni di provenienza proteggono da:
- **Supply chain attacks**: Verifica che il binario proviene dal repository ufficiale
- **Manomissioni**: Garantisce che nessuno ha modificato il binario dopo il build
- **Compromissioni**: Prova che il binario è stato costruito in un ambiente fidato (GitHub Actions)
- **Build non autorizzate**: Conferma che solo i maintainer possono creare release

## Prerequisiti

Per verificare le attestazioni è necessario installare GitHub CLI (`gh`).

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
# Con winget
winget install --id GitHub.cli

# Con Chocolatey
choco install gh

# Con Scoop
scoop install gh
```

## Metodi di Verifica

### Metodo 1: Verifica Rapida (Singolo Binario)

Questo è il metodo più semplice per verificare un singolo binario scaricato.

```bash
# Scarica il binario per la tua piattaforma
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxmox-backup-linux-amd64

# Verifica l'attestazione
gh attestation verify proxmox-backup-linux-amd64 --repo tis24dev/proxmox-backup
```

**Output atteso:**
```
Loaded digest sha256:abc123... for file://proxmox-backup-linux-amd64
Loaded 1 attestation from GitHub API
✓ Verification succeeded!

sha256:abc123... was attested by:
REPO                        PREDICATE_TYPE                  WORKFLOW
tis24dev/proxmox-backup     https://slsa.dev/provenance/v1  .github/workflows/release.yml@refs/tags/v0.9.0
```

### Metodo 2: Verifica di Tutti gli Artifact

Verifica tutti i binari scaricati in una directory.

```bash
# Scarica tutti i binari che ti servono
cd ~/downloads
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxmox-backup-linux-amd64
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxmox-backup-darwin-amd64

# Verifica tutti insieme
gh attestation verify proxmox-backup-* --repo tis24dev/proxmox-backup
```

### Metodo 3: Verifica con Output JSON

Per integrare la verifica in script o per analisi dettagliate.

```bash
gh attestation verify proxmox-backup-linux-amd64 \
  --repo tis24dev/proxmox-backup \
  --format json | jq
```

**Output JSON** (esempio):
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
          "name": "proxmox-backup-linux-amd64",
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

### Metodo 4: Verifica Offline (con bundle scaricato)

Utile per ambienti air-gapped o per archiviare le attestazioni.

```bash
# Scarica l'attestazione come bundle
gh attestation download proxmox-backup-linux-amd64 \
  --repo tis24dev/proxmox-backup \
  --output attestation.jsonl

# Verifica offline usando il bundle
gh attestation verify proxmox-backup-linux-amd64 \
  --bundle attestation.jsonl \
  --repo tis24dev/proxmox-backup
```

## Esempi Pratici Completi

### Esempio 1: Linux - Verifica e Installazione

```bash
#!/bin/bash
set -e

# 1. Scarica il binario
echo "Downloading binary..."
wget -q https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxmox-backup-linux-amd64

# 2. Verifica l'attestazione
echo "Verifying attestation..."
if gh attestation verify proxmox-backup-linux-amd64 --repo tis24dev/proxmox-backup; then
    echo "✓ Attestation verified successfully!"
else
    echo "✗ Attestation verification failed!"
    rm proxmox-backup-linux-amd64
    exit 1
fi

# 3. Rendi eseguibile
chmod +x proxmox-backup-linux-amd64

# 4. Sposta in /usr/local/bin
sudo mv proxmox-backup-linux-amd64 /usr/local/bin/proxmox-backup

echo "Installation complete!"
proxmox-backup --version
```

### Esempio 2: macOS - Verifica con Homebrew Alternativa

```bash
#!/bin/bash
set -e

VERSION="v0.9.0"
BINARY="proxmox-backup-darwin-$(uname -m)"
URL="https://github.com/tis24dev/proxsave/releases/download/${VERSION}/${BINARY}"

# Scarica
echo "Downloading ${BINARY}..."
curl -L -o proxmox-backup "${URL}"

# Verifica
echo "Verifying provenance..."
gh attestation verify proxmox-backup --repo tis24dev/proxmox-backup || {
    echo "Verification failed!"
    rm proxmox-backup
    exit 1
}

# Installa
chmod +x proxmox-backup
sudo mv proxmox-backup /usr/local/bin/
echo "Installed successfully!"
```

### Esempio 3: Windows PowerShell - Verifica e Installazione

```powershell
# Download binary
$version = "v0.9.0"
$binary = "proxmox-backup-windows-amd64.exe"
$url = "https://github.com/tis24dev/proxsave/releases/download/$version/$binary"

Write-Host "Downloading $binary..." -ForegroundColor Cyan
Invoke-WebRequest -Uri $url -OutFile "proxmox-backup.exe"

# Verify attestation
Write-Host "Verifying attestation..." -ForegroundColor Cyan
$result = gh attestation verify proxmox-backup.exe --repo tis24dev/proxmox-backup

if ($LASTEXITCODE -eq 0) {
    Write-Host "✓ Attestation verified successfully!" -ForegroundColor Green

    # Move to Program Files
    $destPath = "$env:ProgramFiles\ProxmoxBackup"
    New-Item -ItemType Directory -Force -Path $destPath | Out-Null
    Move-Item -Path "proxmox-backup.exe" -Destination "$destPath\proxmox-backup.exe" -Force

    # Add to PATH if not already there
    $currentPath = [Environment]::GetEnvironmentVariable("Path", "Machine")
    if ($currentPath -notlike "*$destPath*") {
        [Environment]::SetEnvironmentVariable("Path", "$currentPath;$destPath", "Machine")
        Write-Host "Added to system PATH" -ForegroundColor Green
    }

    Write-Host "Installation complete!" -ForegroundColor Green
} else {
    Write-Host "✗ Attestation verification failed!" -ForegroundColor Red
    Remove-Item "proxmox-backup.exe"
    exit 1
}
```

## Cosa Viene Verificato

Quando esegui `gh attestation verify`, vengono effettuati i seguenti controlli:

| Verifica | Descrizione |
|----------|-------------|
| ✓ **Repository sorgente** | Conferma che il build proviene da `github.com/tis24dev/proxsave` |
| ✓ **Commit SHA** | Verifica il commit esatto usato per il build |
| ✓ **Workflow** | Controlla che sia stato usato `.github/workflows/release.yml` |
| ✓ **Ambiente di build** | Conferma che il build è avvenuto su GitHub-hosted runner |
| ✓ **Integrità SHA256** | Calcola l'hash del file e lo confronta con quello attestato |
| ✓ **Firma crittografica** | Verifica la firma OIDC/Sigstore sull'attestazione |
| ✓ **Timestamp Rekor** | Controlla il record immutabile nel transparency log |
| ✓ **Conformità SLSA** | Verifica che l'attestazione rispetti lo standard SLSA v1 |

## Troubleshooting

### Errore: "no attestations found"

**Causa**: La release non include attestazioni (release precedente a questa feature).

**Soluzione**:
```bash
# Controlla la versione della release
gh release view v0.9.0 --repo tis24dev/proxmox-backup

# Le attestazioni sono disponibili solo da v0.9.1 in poi
```

### Errore: "gh: command not found"

**Causa**: GitHub CLI non è installato o non è nel PATH.

**Soluzione**: Installare `gh` seguendo le istruzioni nella sezione Prerequisiti sopra.

### Errore: "failed to verify signature"

**Causa**: Il file potrebbe essere stato manomesso o corrotto.

**Soluzione**:
```bash
# Riscaricare il binario
rm proxmox-backup-linux-amd64
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxmox-backup-linux-amd64

# Ritentare la verifica
gh attestation verify proxmox-backup-linux-amd64 --repo tis24dev/proxmox-backup
```

Se il problema persiste, **NON usare il binario** e segnala il problema aprendo una issue su GitHub.

### Errore: "attestation verification failed: subject digest mismatch"

**Causa**: L'hash SHA256 del file non corrisponde a quello nell'attestazione.

**Possibili cause**:
- File scaricato parzialmente (download interrotto)
- File modificato dopo il download
- File corrotto durante il trasferimento

**Soluzione**:
```bash
# Verifica dimensione del file
ls -lh proxmox-backup-linux-amd64

# Riscaricare completamente
rm proxmox-backup-linux-amd64
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxmox-backup-linux-amd64

# Verifica checksum manuale (opzionale)
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/SHA256SUMS
sha256sum -c SHA256SUMS
```

### Errore: "HTTP 401: Bad credentials"

**Causa**: GitHub CLI non è autenticato o il token è scaduto.

**Soluzione**:
```bash
# Autenticarsi con GitHub
gh auth login

# Oppure usare un token
export GITHUB_TOKEN=your_personal_access_token
```

### Verifica lenta o timeout

**Causa**: Problemi di connessione al transparency log (Rekor).

**Soluzione**:
```bash
# Usare la verifica offline se hai già scaricato l'attestazione
gh attestation download proxmox-backup-linux-amd64 --repo tis24dev/proxmox-backup -o attestation.jsonl
gh attestation verify proxmox-backup-linux-amd64 --bundle attestation.jsonl --repo tis24dev/proxmox-backup
```

## Considerazioni di Sicurezza

### Trust Model

Le attestazioni si basano su:
1. **GitHub Actions OIDC**: GitHub firma le attestazioni usando token OIDC a breve termine
2. **Sigstore/Rekor**: Transparency log pubblico che registra tutte le attestazioni
3. **SLSA Framework**: Standard industry per provenance metadata

**Cosa significa**: Devi fidarti di GitHub come root of trust. Se GitHub è compromesso, le attestazioni potrebbero essere falsificate.

### Transparency Log (Rekor)

Ogni attestazione è registrata pubblicamente su https://search.sigstore.dev/

**Vantaggi**:
- Registro immutabile e pubblicamente auditabile
- Timestamp verificabili
- Nessun segreto da gestire (keyless signing)

**Cosa puoi controllare**:
```bash
# Cerca attestazioni per questo repository
open "https://search.sigstore.dev/?logIndex=&email=&hash=&logEntry=&uuid="
# Filtra per: tis24dev/proxmox-backup
```

### Confronto con GPG Signing

| Aspetto | Attestazioni (nuovo) | GPG Signing (vecchio) |
|---------|----------------------|----------------------|
| **Gestione chiavi** | Nessuna (keyless) | Richiede gestione chiavi private |
| **Rotazione chiavi** | Automatica | Manuale e complessa |
| **Revoca** | Timestamp-based | Require key revocation |
| **Transparency** | Registro pubblico | Solo se pubblicata su keyserver |
| **Metadati build** | SLSA provenance completo | Solo firma del checksum |
| **Verifica** | `gh attestation verify` | `gpg --verify` |
| **Trust model** | GitHub OIDC + Sigstore | Web of trust / keyserver |

### Best Practices

1. **Verifica sempre prima dell'uso**: Non eseguire binari non verificati
2. **Usa HTTPS**: Scarica sempre da `https://github.com`
3. **Verifica il repository**: Assicurati che sia `tis24dev/proxmox-backup`
4. **Aggiorna gh CLI**: Mantieni GitHub CLI aggiornato per le ultime security features
5. **Automation**: Integra la verifica nei tuoi script di deployment
6. **Archivia attestazioni**: Salva le attestazioni per audit futuri

## Migrazione da GPG

### Per Utenti che Usavano la Vecchia Firma GPG

**Cosa è cambiato**:
- ✗ Non generiamo più `SHA256SUMS.asc` (firma GPG)
- ✓ Generiamo attestazioni SLSA provenance
- ✓ Continuiamo a generare `SHA256SUMS` (checksum in chiaro)

**Release precedenti** (v0.9.0 e precedenti):
- Usano GPG signing (chiave: CD28A21CC11B270E)
- Verifica con: `gpg --verify SHA256SUMS.asc SHA256SUMS`

**Release nuove** (v0.9.1+):
- Usano attestazioni GitHub
- Verifica con: `gh attestation verify`

### Script di Migrazione

Se hai script che usano GPG, ecco come migrarli:

**Prima (GPG)**:
```bash
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/proxmox-backup-linux-amd64
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/SHA256SUMS
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.0/SHA256SUMS.asc
gpg --verify SHA256SUMS.asc SHA256SUMS
sha256sum -c SHA256SUMS
```

**Dopo (Attestazioni)**:
```bash
wget https://github.com/tis24dev/proxsave/releases/download/v0.9.1/proxmox-backup-linux-amd64
gh attestation verify proxmox-backup-linux-amd64 --repo tis24dev/proxmox-backup
```

Molto più semplice! 🎉

## Riferimenti

- [GitHub Docs - Artifact Attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations)
- [GitHub CLI - attestation verify](https://cli.github.com/manual/gh_attestation_verify)
- [SLSA Framework](https://slsa.dev/)
- [Sigstore Project](https://www.sigstore.dev/)
- [Rekor Transparency Log](https://docs.sigstore.dev/logging/overview/)
- [In-Toto Attestations](https://in-toto.io/)

## Support

Se hai problemi con la verifica delle attestazioni:
1. Controlla questa documentazione per troubleshooting
2. Verifica di avere l'ultima versione di `gh` CLI
3. Apri una issue su https://github.com/tis24dev/proxsave/issues

**Note di sicurezza**: Se sospetti che un'attestazione sia stata falsificata o compromessa, **NON usare il binario** e segnala immediatamente aprendo una security advisory privata su GitHub.
