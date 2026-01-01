# Restore Workflow Diagrams

Visual diagrams for understanding the restore system architecture and flow.

## Table of Contents

- [Complete Restore Workflow](#complete-restore-workflow)
- [Category Decision Tree](#category-decision-tree)
- [Service Management Flow](#service-management-flow)
- [Two-Pass Extraction](#two-pass-extraction)
- [Cluster Database Restore Sequence](#cluster-database-restore-sequence)
- [Error Handling Flow](#error-handling-flow)

---

## Complete Restore Workflow

```mermaid
flowchart TD
    Start([User runs --restore]) --> SelectBackup[Select Backup Source]
    SelectBackup --> ScanBackups[Scan for Backups]
    ScanBackups --> ChooseBackup[Choose Specific Backup]

    ChooseBackup --> CheckEncryption{Encrypted?}
    CheckEncryption -->|Yes| Decrypt[Decrypt with AGE]
    CheckEncryption -->|No| SystemDetect[Detect System Type]
    Decrypt --> VerifyChecksum[Verify SHA256]
    VerifyChecksum --> SystemDetect

    SystemDetect --> Compatibility[Validate Compatibility]
    Compatibility --> CompatCheck{Compatible?}
    CompatCheck -->|No| WarnUser[Warn User]
    WarnUser --> UserDecision{Continue?}
    UserDecision -->|No| Abort([Abort])
    UserDecision -->|Yes| AnalyzeCategories
    CompatCheck -->|Yes| AnalyzeCategories[Analyze Categories]

    AnalyzeCategories --> SelectMode[Select Restore Mode]
    SelectMode --> ModeChoice{Mode?}
    ModeChoice -->|Full| GetFullCats[All Categories]
    ModeChoice -->|Storage| GetStorageCats[Storage Categories]
    ModeChoice -->|Base| GetBaseCats[Base Categories]
    ModeChoice -->|Custom| CustomSelect[Interactive Selection]

    GetFullCats --> ShowPlan[Show Restore Plan]
    GetStorageCats --> ShowPlan
    GetBaseCats --> ShowPlan
    CustomSelect --> ShowPlan

    ShowPlan --> Confirm{Type RESTORE?}
    Confirm -->|No| Abort
    Confirm -->|Yes| SafetyBackup[Create Safety Backup]

    SafetyBackup --> SafetyOK{Success?}
    SafetyOK -->|No| AskContinue{Continue anyway?}
    AskContinue -->|No| Abort
    AskContinue -->|Yes| CheckCluster
    SafetyOK -->|Yes| CheckCluster{Cluster Restore?}

    CheckCluster -->|Yes| StopServices[Stop PVE Services]
    StopServices --> UnmountPVE[Unmount /etc/pve]
    UnmountPVE --> DeferRestart[Defer Service Restart]
    DeferRestart --> ExtractNormal

    CheckCluster -->|No| ExtractNormal[Extract Normal Categories]
    ExtractNormal --> ExtractExport{Export Categories?}
    ExtractExport -->|Yes| ExtractToExport[Extract to Export Dir]
    ExtractExport -->|No| PostRestore
    ExtractToExport --> PostRestore[Post-Restore Tasks]

    PostRestore --> RecreateDir[Recreate Directories]
    RecreateDir --> CheckZFS{ZFS Category?}
    CheckZFS -->|Yes| WarnZFS[Warn About ZFS Import]
    CheckZFS -->|No| RestartServices
    WarnZFS --> RestartServices[Restart Services - Deferred]

    RestartServices --> DisplaySummary[Display Summary]
    DisplaySummary --> Success([Success])

    style Start fill:#90EE90
    style Success fill:#90EE90
    style Abort fill:#FFB6C1
    style StopServices fill:#FFD700
    style RestartServices fill:#FFD700
```

---

## Category Decision Tree

```mermaid
flowchart TD
    Start([Select Restore Mode]) --> Mode{Which Mode?}

    Mode -->|1. FULL| Full[FULL Mode]
    Mode -->|2. STORAGE| Storage[STORAGE Mode]
    Mode -->|3. BASE| Base[BASE Mode]
    Mode -->|4. CUSTOM| Custom[CUSTOM Mode]

    Full --> SystemFull{System Type?}
    SystemFull -->|PVE| PVEFull[PVE Categories:<br/>- pve_cluster<br/>- storage_pve<br/>- pve_jobs<br/>- corosync<br/>- ceph<br/>+ Common]
    SystemFull -->|PBS| PBSFull[PBS Categories:<br/>- pbs_config<br/>- datastore_pbs<br/>- pbs_jobs<br/>+ Common]
    SystemFull -->|Unknown| CommonFull[Common Only:<br/>- network<br/>- ssl<br/>- ssh<br/>- scripts<br/>- crontabs<br/>- services<br/>- zfs]

    Storage --> SystemStorage{System Type?}
    SystemStorage -->|PVE| PVEStorage[- pve_cluster<br/>- storage_pve<br/>- pve_jobs<br/>- zfs]
    SystemStorage -->|PBS| PBSStorage[- pbs_config<br/>- datastore_pbs<br/>- pbs_jobs<br/>- zfs]

    Base --> BaseCats[- network<br/>- ssl<br/>- ssh<br/>- services]

    Custom --> CheckboxMenu[Interactive Menu]
    CheckboxMenu --> ToggleLoop{Toggle Categories}
    ToggleLoop -->|Number| Toggle[Toggle Category]
    Toggle --> ToggleLoop
    ToggleLoop -->|'a'| SelectAll[Select All]
    SelectAll --> ToggleLoop
    ToggleLoop -->|'n'| DeselectAll[Deselect All]
    DeselectAll --> ToggleLoop
    ToggleLoop -->|'c'| CustomSelected[User Selection]
    ToggleLoop -->|'0'| Cancel([Cancel])

    PVEFull --> FilterExport[Filter Export-Only]
    PBSFull --> FilterExport
    CommonFull --> FilterExport
    PVEStorage --> FilterExport
    PBSStorage --> FilterExport
    BaseCats --> FilterExport
    CustomSelected --> NoFilter[Include Export if Selected]

    FilterExport --> Split[Split: Normal vs Export]
    NoFilter --> Split
    Split --> Result([Selected Categories])

    style Start fill:#90EE90
    style Result fill:#90EE90
    style Cancel fill:#FFB6C1
    style Custom fill:#87CEEB
    style CheckboxMenu fill:#87CEEB
```

---

## Service Management Flow

```mermaid
sequenceDiagram
    participant User
    participant Restore
    participant Services
    participant FS as Filesystem
    participant DB as config.db

    User->>Restore: Start restore with pve_cluster
    Restore->>Restore: Detect needsClusterRestore = true

    Note over Restore,Services: Service Stop Phase
    Restore->>Services: systemctl stop pve-cluster
    Services->>FS: Unmount /etc/pve (FUSE)
    Services->>DB: Close file handles
    Services-->>Restore: Stopped

    Restore->>Services: systemctl stop pvedaemon
    Services-->>Restore: Stopped
    Restore->>Services: systemctl stop pveproxy
    Services-->>Restore: Stopped
    Restore->>Services: systemctl stop pvestatd
    Services-->>Restore: Stopped

    Note over Restore,FS: Unmount Phase
    Restore->>FS: umount /etc/pve
    FS-->>Restore: Unmounted (or already unmounted)

    Note over Restore: Schedule Deferred Restart (defer)

    Note over Restore,DB: Restore Phase
    Restore->>DB: Extract /var/lib/pve-cluster/
    Restore->>DB: Extract config.db
    DB-->>Restore: Files restored

    Note over Restore,Services: Deferred Restart Executes
    Restore->>Services: systemctl start pve-cluster
    Services->>DB: Open config.db
    Services->>FS: Mount /etc/pve (FUSE)
    Services-->>Restore: Started

    Restore->>Services: systemctl start pvedaemon
    Services->>FS: Read /etc/pve config
    Services-->>Restore: Started

    Restore->>Services: systemctl start pveproxy
    Services-->>Restore: Started

    Restore->>Services: systemctl start pvestatd
    Services-->>Restore: Started

    Restore-->>User: Restore Complete

    Note over User,Services: Verification Phase
    User->>Services: pvecm status
    Services->>DB: Query cluster state
    Services-->>User: Quorate
    User->>FS: ls /etc/pve/
    FS->>DB: Query via pmxcfs
    FS-->>User: Config files visible
```

---

## Two-Pass Extraction

```mermaid
flowchart LR
    subgraph Input
        Archive[backup.tar.gz]
    end

    subgraph "Pass 1: Normal Categories"
        Open1[Open Archive]
        Decompress1[Decompress]
        Scan1[Scan TAR Entries]
        Filter1{Matches Normal<br/>Category?}
        Extract1[Extract to /]
        Skip1[Skip Entry]
    end

    subgraph "Pass 2: Export-Only Categories"
        Open2[Open Archive]
        Decompress2[Decompress]
        Scan2[Scan TAR Entries]
        Filter2{Matches Export<br/>Category?}
        Extract2[Extract to<br/>Export Dir]
        Skip2[Skip Entry]
    end

    subgraph Output
        SystemRoot[System Root /<br/>- /var/lib/pve-cluster/<br/>- /etc/network/<br/>- etc.]
        ExportDir[Export Directory<br/>BASE_DIR/proxmox-config-export-TIMESTAMP/<br/>- etc/pve/storage.cfg<br/>- etc/pve/datacenter.cfg<br/>- etc.]
    end

    Archive --> Open1
    Open1 --> Decompress1
    Decompress1 --> Scan1
    Scan1 --> Filter1
    Filter1 -->|Yes| Extract1
    Filter1 -->|No| Skip1
    Skip1 --> Scan1
    Extract1 --> SystemRoot

    Archive --> Open2
    Open2 --> Decompress2
    Decompress2 --> Scan2
    Scan2 --> Filter2
    Filter2 -->|Yes| Extract2
    Filter2 -->|No| Skip2
    Skip2 --> Scan2
    Extract2 --> ExportDir

    style Archive fill:#87CEEB
    style SystemRoot fill:#90EE90
    style ExportDir fill:#FFD700
```

---

## Cluster Database Restore Sequence

```mermaid
stateDiagram-v2
    [*] --> Running: Initial State

    state Running {
        [*] --> ServicesActive
        ServicesActive: pve-cluster: active
        ServicesActive: pvedaemon: active
        ServicesActive: pveproxy: active
        ServicesActive: /etc/pve: mounted
    }

    Running --> Stopping: User initiates restore

    state Stopping {
        [*] --> StopCluster
        StopCluster: systemctl stop pve-cluster
        StopCluster --> StopDaemon
        StopDaemon: systemctl stop pvedaemon
        StopDaemon --> StopProxy
        StopProxy: systemctl stop pveproxy
        StopProxy --> StopStatd
        StopStatd: systemctl stop pvestatd
        StopStatd --> UnmountPVE
        UnmountPVE: umount /etc/pve
        UnmountPVE --> [*]
    }

    Stopping --> Stopped: All services stopped

    state Stopped {
        [*] --> ServicesStopped
        ServicesStopped: All services: inactive
        ServicesStopped: /etc/pve: unmounted
        ServicesStopped: config.db: not in use
    }

    Stopped --> Restoring: Extract files

    state Restoring {
        [*] --> ExtractDB
        ExtractDB: Extract /var/lib/pve-cluster/
        ExtractDB --> WriteFiles
        WriteFiles: Write config.db
        WriteFiles: Write supporting files
        WriteFiles --> [*]
    }

    Restoring --> Restarting: Files restored

    state Restarting {
        [*] --> StartCluster
        StartCluster: systemctl start pve-cluster
        StartCluster --> ReadDB
        ReadDB: pmxcfs reads config.db
        ReadDB --> MountPVE
        MountPVE: Mount /etc/pve (FUSE)
        MountPVE --> StartDaemon
        StartDaemon: systemctl start pvedaemon
        StartDaemon --> StartProxy
        StartProxy: systemctl start pveproxy
        StartProxy --> StartStatd
        StartStatd: systemctl start pvestatd
        StartStatd --> [*]
    }

    Restarting --> Restored: Services restarted

    state Restored {
        [*] --> ServicesRestored
        ServicesRestored: All services: active
        ServicesRestored: /etc/pve: mounted with RESTORED config
        ServicesRestored: Cluster: operational
    }

    Restored --> [*]: Restore complete
```

---

## Error Handling Flow

```mermaid
flowchart TD
    Start([Operation]) --> Operation{Operation Type}

    Operation -->|User Action| UserAbort{User Cancels?}
    UserAbort -->|Yes| ReturnAbort[Return ErrRestoreAborted]
    UserAbort -->|No| Continue[Continue]

    Operation -->|Service Stop| ServiceStop[Stop Service]
    ServiceStop --> StopResult{Success?}
    StopResult -->|No| FailFast[FAIL-FAST: Abort Restore]
    StopResult -->|Yes| Continue

    Operation -->|Safety Backup| CreateBackup[Create Safety Backup]
    CreateBackup --> BackupResult{Success?}
    BackupResult -->|No| AskUser{Ask User Continue?}
    AskUser -->|No| ReturnAbort
    AskUser -->|Yes| Continue
    BackupResult -->|Yes| Continue

    Operation -->|File Extract| ExtractFile[Extract File]
    ExtractFile --> ExtractResult{Success?}
    ExtractResult -->|No| LogWarning[Log Warning, Increment Failed]
    ExtractResult -->|Yes| Continue
    LogWarning --> ContinueLoop[Continue to Next File]

    Operation -->|Service Restart| RestartService[Restart Service - Deferred]
    RestartService --> RestartResult{Success?}
    RestartResult -->|No| WarnOnly[Log Warning Only]
    RestartResult -->|Yes| Continue
    WarnOnly --> ContinueFinal[Continue Anyway]

    Operation -->|Unmount| UnmountFS[Unmount /etc/pve]
    UnmountFS --> UnmountResult{Success?}
    UnmountResult -->|No| WarnContinue[Log Warning, Continue]
    UnmountResult -->|Yes| Continue
    WarnContinue --> Continue

    FailFast --> Abort([Abort Restore])
    ReturnAbort --> Abort
    Continue --> Success([Success])
    ContinueLoop --> Success
    ContinueFinal --> Success

    style Start fill:#87CEEB
    style Success fill:#90EE90
    style Abort fill:#FFB6C1
    style FailFast fill:#FF6347
    style LogWarning fill:#FFD700
    style WarnOnly fill:#FFD700
    style WarnContinue fill:#FFD700
```

---

## Path Matching Algorithm

```mermaid
flowchart TD
    Start([Archive Entry]) --> GetName[Get Entry Name]
    GetName --> Normalize{Starts with ./ ?}
    Normalize -->|No| AddPrefix["Prepend './'"]
    Normalize -->|Yes| LoopCats[For Each Category]
    AddPrefix --> LoopCats

    LoopCats --> LoopPaths[For Each Category Path]
    LoopPaths --> CheckExact{Exact Match?}
    CheckExact -->|Yes| Match([MATCH])
    CheckExact -->|No| CheckDir{Path Ends with / ?}

    CheckDir -->|No| NextPath[Next Path]
    CheckDir -->|Yes| CheckPrefix{Entry Starts with Path?}
    CheckPrefix -->|Yes| Match
    CheckPrefix -->|No| CheckParent{Entry == Path without / ?}
    CheckParent -->|Yes| Match
    CheckParent -->|No| NextPath

    NextPath --> MorePaths{More Paths?}
    MorePaths -->|Yes| LoopPaths
    MorePaths -->|No| NextCat[Next Category]
    NextCat --> MoreCats{More Categories?}
    MoreCats -->|Yes| LoopCats
    MoreCats -->|No| NoMatch([NO MATCH])

    style Start fill:#87CEEB
    style Match fill:#90EE90
    style NoMatch fill:#FFB6C1
```

**Examples**:

| Archive Entry | Category Path | Result |
|--------------|---------------|--------|
| `./etc/network/interfaces` | `./etc/network/` | ✅ Prefix match |
| `./etc/hostname` | `./etc/network/` | ❌ No match |
| `etc/hostname` | `./etc/hostname` | ✅ After normalize |
| `./var/lib/pve-cluster/config.db` | `./var/lib/pve-cluster/` | ✅ Prefix match |

---

## Category Type Filter

```mermaid
flowchart TD
    Start([All Categories]) --> CheckSystem{System Type?}

    CheckSystem -->|PVE| FilterPVE[Filter Categories]
    CheckSystem -->|PBS| FilterPBS[Filter Categories]
    CheckSystem -->|Unknown| FilterCommon[Filter Categories]

    FilterPVE --> IncludePVE["Include:<br/>- CategoryTypePVE<br/>- CategoryTypeCommon"]
    FilterPBS --> IncludePBS["Include:<br/>- CategoryTypePBS<br/>- CategoryTypeCommon"]
    FilterCommon --> IncludeOnlyCommon["Include:<br/>- CategoryTypeCommon only"]

    IncludePVE --> CheckMode{Restore Mode?}
    IncludePBS --> CheckMode
    IncludeOnlyCommon --> CheckMode

    CheckMode -->|Full/Storage/Base| RemoveExport[Remove ExportOnly = true]
    CheckMode -->|Custom| KeepAll[Keep All - User Choice]

    RemoveExport --> Available{In Archive?}
    KeepAll --> Available

    Available --> CheckAvailable[Check IsAvailable]
    CheckAvailable --> Result([Final Category List])

    style Start fill:#87CEEB
    style Result fill:#90EE90
```

---

## Safety Backup Process

```mermaid
flowchart TD
    Start([Categories Selected]) --> CreatePath[Create /tmp/proxsave/]
    CreatePath --> CreateArchive[Create restore_backup_TIMESTAMP.tar.gz]
    CreateArchive --> LoopCats[For Each Category]

    LoopCats --> LoopPaths[For Each Path in Category]
    LoopPaths --> BuildFull["Build Full Path:<br/>/ + path"]
    BuildFull --> CheckExists{Path Exists?}

    CheckExists -->|No| NextPath[Next Path]
    CheckExists -->|Yes| CheckType{Type?}

    CheckType -->|File| BackupFile[Add File to TAR]
    CheckType -->|Directory| WalkDir[Walk Directory Recursively]

    WalkDir --> BackupTree[Add All Files/Dirs to TAR]
    BackupTree --> NextPath
    BackupFile --> NextPath

    NextPath --> MorePaths{More Paths?}
    MorePaths -->|Yes| LoopPaths
    MorePaths -->|No| NextCat[Next Category]

    NextCat --> MoreCats{More Categories?}
    MoreCats -->|Yes| LoopCats
    MoreCats -->|No| CloseTar[Close TAR Archive]

    CloseTar --> Success([Safety Backup Created])
    Success --> DisplayPath["Display:<br/>/tmp/proxsave/restore_backup_TIMESTAMP.tar.gz"]
    DisplayPath --> Rollback["Show Rollback Command:<br/>tar -xzf backup.tar.gz -C /"]

    style Start fill:#87CEEB
    style Success fill:#90EE90
    style DisplayPath fill:#FFD700
```

---

## Decryption Workflow

```mermaid
flowchart TD
    Start([Backup Selected]) --> ReadManifest[Read Manifest]
    ReadManifest --> CheckEnc{EncryptionMode?}

    CheckEnc -->|"none"| Plain[Use Archive Directly]
    CheckEnc -->|"age"| ShowOptions[Show Decryption Options]

    ShowOptions --> UserChoice{User Selects?}
    UserChoice -->|1. Passphrase| GetPass[Get AGE Passphrase]
    UserChoice -->|2. Identity| GetKey[Get AGE Identity File]
    UserChoice -->|0. Cancel| Abort([Abort])

    GetPass --> PrepareTemp[Prepare /tmp/proxsave/proxmox-decrypt-XXXX/]
    GetKey --> PrepareTemp

    PrepareTemp --> Decrypt[Run AGE Decrypt]
    Decrypt --> DecryptResult{Success?}

    DecryptResult -->|No| RetryPrompt{Retry?}
    RetryPrompt -->|Yes| ShowOptions
    RetryPrompt -->|No| Abort

    DecryptResult -->|Yes| ChecksumCheck{Checksum Available?}
    ChecksumCheck -->|No| WarnNoCheck[Warn: No Checksum]
    ChecksumCheck -->|Yes| VerifySum[Calculate SHA256]

    VerifySum --> CompareSum{Match?}
    CompareSum -->|No| ChecksumFail[Error: Checksum Mismatch]
    CompareSum -->|Yes| Success

    ChecksumFail --> Abort
    WarnNoCheck --> Success([Decrypted Archive Ready])
    Plain --> Success

    Success --> DeferCleanup["Schedule Cleanup<br/>(remove temp files on exit)"]

    style Start fill:#87CEEB
    style Success fill:#90EE90
    style Abort fill:#FFB6C1
    style ChecksumFail fill:#FF6347
```

---

## Storage Directory Recreation

```mermaid
flowchart TD
    Start([Post-Restore]) --> CheckSystem{System Type?}

    CheckSystem -->|PVE| CheckPVECat{storage_pve<br/>Restored?}
    CheckSystem -->|PBS| CheckPBSCat{datastore_pbs<br/>Restored?}
    CheckSystem -->|Unknown| Skip([Skip])

    CheckPVECat -->|No| Skip
    CheckPVECat -->|Yes| ParsePVE[Parse /etc/pve/storage.cfg]

    CheckPBSCat -->|No| Skip
    CheckPBSCat -->|Yes| ParsePBS[Parse /etc/proxmox-backup/datastore.cfg]

    ParsePVE --> LoopPVE[For Each Storage]
    LoopPVE --> CheckPVEType{Storage Type?}

    CheckPVEType -->|dir| CreateDirStruct["Create:<br/>- dump/<br/>- images/<br/>- template/<br/>- snippets/<br/>- private/"]
    CheckPVEType -->|nfs/cifs| CreateNFSStruct["Create:<br/>- dump/<br/>- images/<br/>- template/"]
    CheckPVEType -->|Other| NextPVE[Next Storage]

    CreateDirStruct --> NextPVE
    CreateNFSStruct --> NextPVE
    NextPVE --> MorePVE{More Storage?}
    MorePVE -->|Yes| LoopPVE
    MorePVE -->|No| Done([Done])

    ParsePBS --> LoopPBS[For Each Datastore]
    LoopPBS --> CheckZFSMount{Likely ZFS Mount?}

    CheckZFSMount -->|Yes| WarnZFS["Warn: Don't create<br/>(would block mount)"]
    CheckZFSMount -->|No| CreatePBSStruct["Create:<br/>- .chunks/<br/>- .lock/"]

    WarnZFS --> LogOwner["Log: Should be<br/>backup:backup"]
    CreatePBSStruct --> LogOwner
    LogOwner --> NextPBS[Next Datastore]

    NextPBS --> MorePBS{More Datastores?}
    MorePBS -->|Yes| LoopPBS
    MorePBS -->|No| CheckZFSCat{zfs Category<br/>Restored?}

    CheckZFSCat -->|Yes| WarnZFSImport["Warn: ZFS pools<br/>may need manual import"]
    CheckZFSCat -->|No| Done
    WarnZFSImport --> DisplayCmds["Display:<br/>zpool import<br/>zpool import pool-name"]
    DisplayCmds --> Done

    style Start fill:#87CEEB
    style Done fill:#90EE90
    style Skip fill:#D3D3D3
```

---

## Compatibility Validation

```mermaid
flowchart TD
    Start([Backup Prepared]) --> DetectCurrent[Detect Current System]
    DetectCurrent --> CheckPVE{"/etc/pve exists<br/>AND /usr/bin/qm exists?"}

    CheckPVE -->|Yes| CurrentPVE[Current: PVE]
    CheckPVE -->|No| CheckPBS{"/etc/proxmox-backup exists<br/>AND /usr/sbin/proxmox-backup-proxy?"}

    CheckPBS -->|Yes| CurrentPBS[Current: PBS]
    CheckPBS -->|No| CurrentUnknown[Current: Unknown]

    CurrentPVE --> ReadManifest
    CurrentPBS --> ReadManifest
    CurrentUnknown --> ReadManifest[Read Backup Manifest]

    ReadManifest --> CheckBackupType{manifest.ProxmoxType<br/>OR hostname pattern}
    CheckBackupType -->|pve| BackupPVE[Backup: PVE]
    CheckBackupType -->|pbs| BackupPBS[Backup: PBS]
    CheckBackupType -->|Unknown| BackupUnknown[Backup: Unknown]

    BackupPVE --> Compare
    BackupPBS --> Compare
    BackupUnknown --> Compare[Compare Types]

    Compare --> Match{Current == Backup?}
    Match -->|Yes| Compatible([Compatible])
    Match -->|No| CheckUnknown{Either Unknown?}

    CheckUnknown -->|Yes| Compatible
    CheckUnknown -->|No| Incompatible[Incompatible]

    Incompatible --> DisplayWarning["Display Warning:<br/>PVE ↔ PBS mismatch"]
    DisplayWarning --> AskOverride{Type 'yes'<br/>to continue?}

    AskOverride -->|No| Abort([Abort])
    AskOverride -->|Yes| ProceedAnyway([Proceed with Warning])

    Compatible --> Proceed([Proceed])

    style Start fill:#87CEEB
    style Proceed fill:#90EE90
    style ProceedAnyway fill:#FFD700
    style Abort fill:#FFB6C1
```

---

## Usage Examples

### Viewing Diagrams

**GitHub**: Mermaid diagrams render automatically in GitHub markdown.

**VS Code**: Install "Markdown Preview Mermaid Support" extension.

**Command Line**: Use `mermaid-cli` to generate images:
```bash
npm install -g @mermaid-js/mermaid-cli

# Generate PNG
mmdc -i docs/RESTORE_DIAGRAMS.md -o diagrams/

# Generate SVG
mmdc -i docs/RESTORE_DIAGRAMS.md -o diagrams/ -t svg
```

**Web Browser**: Copy diagram to https://mermaid.live/

---

## Related Documentation

- [RESTORE_GUIDE.md](RESTORE_GUIDE.md) - Complete user guide
- [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md) - Technical architecture
- [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md) - Disaster recovery procedures
