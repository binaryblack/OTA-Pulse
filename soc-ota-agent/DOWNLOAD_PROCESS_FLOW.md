# OTA Download Process Flow - Detailed Analysis

## Overview
This document describes the complete download process in the soc-ota-agent, including how interruptions are handled, download resumption logic, and state transitions during the download phase.

---

## 1. High-Level Download Flow

```
┌─────────────────────────────────────────────────────────────────────┐
│                        UPDATE CHECK STATE                            │
│  - Check server for available updates                                │
│  - Validate update availability                                      │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                      UPDATE FETCH STATE                              │
│  - Enable deployment logging                                         │
│  - Report status: "downloading"                                      │
│  - Initiate download from server                                     │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                      UPDATE STORE STATE                              │
│  - Download artifact data                                            │
│  - Verify headers and compatibility                                  │
│  - Store payloads to disk                                            │
│  - Handle interruptions with resume capability                       │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                   UPDATE AFTER STORE STATE                           │
│  - Execute Download_Leave scripts                                    │
│  - Transition to installation phase                                  │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 2. Detailed Download Process with Network Quality Check

```
START: FetchUpdate() Called
         │
         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    NETWORK QUALITY CHECK                             │
│                                                                       │
│  CheckNetworkQuality()                                               │
│  ├─ Check if WiFi (wlan0) is available                              │
│  ├─ Get signal strength using 'iw' command                          │
│  │                                                                    │
│  └─ Signal Evaluation:                                               │
│      • > -50 dBm: Excellent                                          │
│      • > -60 dBm: Good                                               │
│      • > -70 dBm: Fair                                               │
│      • > -75 dBm: Minimum acceptable ✓                               │
│      • < -75 dBm: Poor - REJECT DOWNLOAD                             │
│                                                                       │
└────────────┬────────────────────────────────────────────────────────┘
             │
             ├─── Signal < -75 dBm ──────────────────────┐
             │                                            │
             │                                            ▼
             │                                   ┌────────────────────┐
             │                                   │  RETURN ERROR      │
             │                                   │  "Network quality  │
             │                                   │   insufficient"    │
             │                                   └────────────────────┘
             │
             ├─── Wired Connection / No WiFi ────────────┐
             │                                            │
             │                                            ▼
             │                                   ┌────────────────────┐
             │                                   │  ALLOW DOWNLOAD    │
             │                                   │  (Assume stable)   │
             │                                   └────────────────────┘
             │
             └─── Signal >= -75 dBm ─────────────────────┤
                                                          │
                                                          ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    CREATE HTTP GET REQUEST                           │
│  - Build request with artifact URL                                   │
│  - No Range header initially (full download)                         │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    EXECUTE HTTP REQUEST                              │
│  - Send request via ApiRequester.Do()                                │
│  - Receive HTTP response                                             │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    VALIDATE RESPONSE                                 │
│  - Check status code (expect 200 OK)                                 │
│  - Verify Content-Length header exists                               │
│  - Validate minimum image size (4096 kB)                             │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                 CREATE UPDATE RESUMER                                │
│  - Wrap response body in UpdateResumer                               │
│  - Store: contentLength, maxWait, apiReq, request                    │
│  - Initialize: offset=0, retryAttempts=0                             │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
                    RETURN STREAM
```

---

## 3. Download Interruption & Resume Logic

### 3.1 UpdateResumer Read() Flow

```
┌─────────────────────────────────────────────────────────────────────┐
│                    UpdateResumer.Read(buf)                           │
│  - Called repeatedly to read artifact data                           │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    READ FROM STREAM                                  │
│  stream.Read(buf[offset-origOffset:])                                │
│  - Read data into buffer                                             │
│  - Update offset: offset += bytesRead                                │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ├─── Success (err == nil) ──────────────────┐
                         │                                            │
                         │                                            ▼
                         │                                   ┌────────────────┐
                         │                                   │ RETURN DATA    │
                         │                                   │ Continue read  │
                         │                                   └────────────────┘
                         │
                         ├─── EOF & offset >= contentLength ─────────┐
                         │                                            │
                         │                                            ▼
                         │                                   ┌────────────────┐
                         │                                   │ DOWNLOAD       │
                         │                                   │ COMPLETE       │
                         │                                   └────────────────┘
                         │
                         └─── ERROR (Connection broken) ─────────────┤
                                                                      │
                                                                      ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    INTERRUPTION DETECTED                             │
│  - Network error / Unexpected EOF                                    │
│  - Connection timeout                                                │
│  - Server disconnection                                              │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    RESUME PREPARATION                                │
│  1. Log error: "Download connection broken"                          │
│  2. Set Range header: "bytes={offset}-"                              │
│  3. Calculate exponential backoff wait time                          │
│     - Based on retryAttempts count                                   │
│     - Capped at maxWait (4 hours default)                            │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    EXPONENTIAL BACKOFF WAIT                          │
│  - Log: "Resuming download in {waitTime}"                            │
│  - time.Sleep(waitTime)                                              │
│  - Increment retryAttempts                                           │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    RESUME DOWNLOAD ATTEMPT                           │
│  - Log: "Attempting to resume from offset {offset}"                  │
│  - Execute HTTP request with Range header                            │
│  - apiReq.Do(req)                                                    │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ├─── Request Failed ────────────────────────┐
                         │                                            │
                         │                                            ▼
                         │                                   ┌────────────────┐
                         │                                   │ LOG ERROR      │
                         │                                   │ RETRY LOOP     │
                         │                                   │ (go back to    │
                         │                                   │  backoff wait) │
                         │                                   └────────────────┘
                         │
                         └─── Request Succeeded ─────────────────────┤
                                                                      │
                                                                      ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    VALIDATE PARTIAL CONTENT                          │
│  getStreamFromPartialContent(response)                               │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    CHECK HTTP STATUS                                 │
│  - Expect: 206 Partial Content                                       │
│  - If not 206: ERROR "Could not resume download"                     │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    PARSE CONTENT-RANGE HEADER                        │
│  Format: "bytes {start}-{end}/{total}"                               │
│  Example: "bytes 1048576-2097151/2097152"                            │
│                                                                       │
│  Validations:                                                        │
│  1. Header starts with "bytes "                                      │
│  2. Parse start position (newOffset)                                 │
│  3. Verify total size matches contentLength                          │
│  4. Ensure newOffset <= current offset                               │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ├─── newOffset == offset ───────────────────┐
                         │                                            │
                         │                                            ▼
                         │                                   ┌────────────────┐
                         │                                   │ PERFECT MATCH  │
                         │                                   │ Resume reading │
                         │                                   └────────────────┘
                         │
                         ├─── newOffset < offset ────────────────────┐
                         │                                            │
                         │                                            ▼
                         │                                   ┌────────────────┐
                         │                                   │ CATCH UP       │
                         │                                   │ Discard bytes  │
                         │                                   │ until offset   │
                         │                                   └────────────────┘
                         │
                         └─── newOffset > offset ────────────────────┤
                                                                      │
                                                                      ▼
                                                             ┌────────────────┐
                                                             │ ERROR          │
                                                             │ Server gave    │
                                                             │ wrong range    │
                                                             └────────────────┘
```

### 3.2 Catch-Up Logic (Server Returns Earlier Offset)

```
Server returned offset < requested offset
         │
         ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    DISCARD EXTRA BYTES                               │
│  - Calculate: bytesToDiscard = offset - newOffset                    │
│  - io.CopyN(ioutil.Discard, res.Body, bytesToDiscard)               │
│  - Consume and throw away data until we reach our offset             │
└────────────────────────┬────────────────────────────────────────────┘
                         │
                         ├─── Success ───────────────────────────────┐
                         │                                            │
                         │                                            ▼
                         │                                   ┌────────────────┐
                         │                                   │ RESUME NORMAL  │
                         │                                   │ READING        │
                         │                                   └────────────────┘
                         │
                         └─── Failed to catch up ────────────────────┤
                                                                      │
                                                                      ▼
                                                             ┌────────────────┐
                                                             │ RETURN ERROR   │
                                                             │ Cannot resume  │
                                                             └────────────────┘
```

---

## 4. State Machine During Download

### 4.1 Normal Download Flow

```
┌──────────────────┐
│  UpdateCheck     │
│  State           │
└────────┬─────────┘
         │
         ▼
┌──────────────────┐
│  UpdateFetch     │──────────────────────────────────────────┐
│  State           │                                           │
│                  │  Actions:                                 │
│  - Enable logs   │  • Enable deployment logging              │
│  - Report status │  • Report "downloading" to server         │
│  - Fetch update  │  • Call FetchUpdate()                     │
└────────┬─────────┘  • Network quality check                  │
         │            • Create HTTP request                    │
         │            • Return UpdateResumer stream            │
         │                                                      │
         ▼                                                      │
┌──────────────────┐                                           │
│  UpdateStore     │──────────────────────────────────────────┤
│  State           │                                           │
│                  │  Actions:                                 │
│  - Read headers  │  • Read artifact headers                  │
│  - Verify data   │  • Verify artifact name & compatibility   │
│  - Store payload │  • Check dependencies & provides          │
│  - Check rollback│  • Store payloads (actual download)       │
└────────┬─────────┘  • Determine rollback support             │
         │            • Save state data to DB                  │
         │                                                      │
         ▼                                                      │
┌──────────────────┐                                           │
│ UpdateAfterStore │                                           │
│  State           │                                           │
│                  │  Actions:                                 │
│  - Execute       │  • Run Download_Leave scripts             │
│    Download_Leave│                                           │
└────────┬─────────┘                                           │
         │                                                      │
         ▼                                                      │
┌──────────────────┐                                           │
│  UpdateInstall   │                                           │
│  State           │                                           │
└──────────────────┘                                           │
```

### 4.2 Download Interruption Scenarios

#### Scenario A: Network Interruption During Download

```
UpdateStore State
    │
    ├─ Reading artifact data via UpdateResumer
    │
    ▼
┌─────────────────────────────────────────────────────────────┐
│  NETWORK INTERRUPTION                                        │
│  - Connection drops                                          │
│  - Read() returns error                                      │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────┐
│  AUTOMATIC RESUME (Inside UpdateResumer)                     │
│  1. Log error                                                │
│  2. Calculate backoff time                                   │
│  3. Wait (exponential backoff)                               │
│  4. Retry with Range header                                  │
│  5. Validate 206 response                                    │
│  6. Continue reading from offset                             │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ├─── Resume Success ──────────────────────┐
                     │                                          │
                     │                                          ▼
                     │                                 ┌────────────────┐
                     │                                 │ CONTINUE       │
                     │                                 │ DOWNLOAD       │
                     │                                 └────────────────┘
                     │
                     └─── Max Retries Exceeded ───────────────┤
                                                                │
                                                                ▼
                                                       ┌────────────────┐
                                                       │ RETURN ERROR   │
                                                       │ to caller      │
                                                       └────────┬───────┘
                                                                │
                                                                ▼
                                                       ┌────────────────┐
                                                       │ FetchStoreRetry│
                                                       │ State          │
                                                       └────────────────┘
```

#### Scenario B: System Reboot During Download

```
UpdateStore State
    │
    ├─ Downloading artifact
    │
    ▼
┌─────────────────────────────────────────────────────────────┐
│  SYSTEM REBOOT / POWER LOSS                                  │
│  - Process terminated                                        │
│  - State data saved to DB                                    │
│  - Partial download lost                                     │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────┐
│  SYSTEM RESTART                                              │
│  - Agent starts                                              │
│  - Enters Init State                                         │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────┐
│  Init State - Load State Data                                │
│  - Read state from DB                                        │
│  - Detect: MenderStateUpdateStore or                         │
│            MenderStateUpdateAfterStore                       │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────┐
│  SPECIAL HANDLING FOR DOWNLOAD STATES                        │
│                                                               │
│  If state == UpdateStore || UpdateAfterStore:                │
│    • Go straight to UpdateCleanup                            │
│    • Status: FAILURE                                         │
│    • Reason: Artifact not yet signature verified             │
│    • Cannot trust partially downloaded data                  │
│                                                               │
│  This prevents running artifact scripts before               │
│  signature verification is complete!                         │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────┐
│  UpdateCleanup State                                         │
│  - Clean up partial download                                 │
│  - Report failure to server                                  │
│  - Return to Idle                                            │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────┐
│  Idle State                                                  │
│  - Next update check will retry download                     │
│  - Fresh download from beginning                             │
└─────────────────────────────────────────────────────────────┘
```

#### Scenario C: Download Failure - Retry Logic

```
UpdateFetch State
    │
    ├─ FetchUpdate() fails
    │
    ▼
┌─────────────────────────────────────────────────────────────┐
│  FetchStoreRetry State                                       │
│  - Exponential backoff retry mechanism                       │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────┐
│  Calculate Retry Interval                                    │
│  - Use GetExponentialBackoffTime()                           │
│  - Based on ctx.fetchInstallAttempts                         │
│  - Capped by UpdatePollInterval                              │
└────────────────────┬────────────────────────────────────────┘
                     │
                     ├─── Attempts < Max ──────────────────────┐
                     │                                          │
                     │                                          ▼
                     │                                 ┌────────────────┐
                     │                                 │ WAIT           │
                     │                                 │ Sleep interval │
                     │                                 └────────┬───────┘
                     │                                          │
                     │                                          ▼
                     │                                 ┌────────────────┐
                     │                                 │ RETRY          │
                     │                                 │ UpdateFetch    │
                     │                                 │ State          │
                     │                                 └────────────────┘
                     │
                     └─── Attempts >= Max ─────────────────────┤
                                                                │
                                                                ▼
                                                       ┌────────────────┐
                                                       │ UpdateError    │
                                                       │ State          │
                                                       └────────────────┘
```

---

## 5. Key Components & Their Roles

### 5.1 UpdateResumer
**File:** `client/update_resumer.go`

**Purpose:** Transparent download resumption wrapper

**Key Fields:**
- `stream`: Current HTTP response body reader
- `offset`: Current download position (bytes)
- `contentLength`: Total file size
- `retryAttempts`: Number of resume attempts
- `maxWait`: Maximum backoff time (4 hours default)
- `apiReq`: API requester for making new requests
- `req`: Original HTTP request (modified for resume)

**Key Methods:**
- `Read(buf)`: Implements io.Reader, handles interruptions
- `getStreamFromPartialContent()`: Validates and processes 206 responses
- `Close()`: Closes the underlying stream

### 5.2 UpdateClient
**File:** `client/client_update.go`

**Purpose:** High-level update operations

**Key Methods:**
- `FetchUpdate()`: Initiates download with network quality check
- `GetScheduledUpdate()`: Checks for available updates

**Network Quality Integration:**
- Calls `CheckNetworkQuality()` before download
- Rejects download if WiFi signal < -75 dBm
- Allows download for wired connections

### 5.3 State Machine States
**File:** `app/state.go`

**Download-Related States:**
1. **updateFetchState**: Initiates download
2. **updateStoreState**: Performs actual download and storage
3. **updateAfterStoreState**: Post-download cleanup
4. **fetchStoreRetryState**: Handles download retry logic

---

## 6. Error Handling & Recovery

### 6.1 Transient Errors (Recoverable)
```
Network timeout
Connection reset
Temporary server error (5xx)
    │
    ▼
Automatic Resume (UpdateResumer)
    │
    ├─ Exponential backoff
    ├─ Range request
    └─ Continue from offset
```

### 6.2 Fatal Errors (Non-Recoverable)
```
Invalid artifact
Signature verification failure
Incompatible device type
Insufficient disk space
    │
    ▼
UpdateError State
    │
    ├─ Report failure
    ├─ Cleanup
    └─ Return to Idle
```

### 6.3 System Interruptions
```
Power loss during download
System crash
Process kill
    │
    ▼
Init State (on restart)
    │
    ├─ Detect incomplete download
    ├─ Go to UpdateCleanup
    └─ Retry on next check
```

---

## 7. Standalone Mode Download

**File:** `app/standalone.go`

### Standalone Download Flow

```
DoStandaloneInstall()
    │
    ├─── Remote Update (HTTP/HTTPS) ────────────────────────────┐
    │                                                             │
    │    1. Create ApiClient                                     │
    │    2. Create UpdateClient                                  │
    │    3. FetchUpdate() - same as daemon mode                  │
    │    4. Returns UpdateResumer stream                         │
    │                                                             │
    └─── Local Update (File) ───────────────────────────────────┤
                                                                  │
         1. Open local file                                      │
         2. Get file size                                        │
         3. Return file reader                                   │
                                                                  │
                                                                  ▼
┌─────────────────────────────────────────────────────────────────────┐
│  doStandaloneInstallStatesDownload()                                 │
│                                                                       │
│  Download State:                                                     │
│  1. Execute Download_Enter scripts                                   │
│  2. Read artifact headers                                            │
│  3. Verify dependencies                                              │
│  4. Store payloads (actual download/copy)                            │
│  5. Execute Download_Leave scripts                                   │
│                                                                       │
│  Error Handling:                                                     │
│  - On any error: call Download_Error script                          │
│  - Run doStandaloneFailureStates()                                   │
│  - Return error to caller                                            │
└─────────────────────────────────────────────────────────────────────┘
```

**Key Differences from Daemon Mode:**
- No automatic retry logic
- No state persistence to database during download
- Errors immediately fail the operation
- User must manually retry
- Progress display via ProgressWriter

---

## 8. Configuration & Timeouts

### HTTP Client Configuration
**File:** `client/client.go`

```
Default Timeouts:
- Connection Timeout: 10 seconds
- Reading Timeout: 4 hours
  (Allows ~1 Mbps for 2GB file)
- TLS Handshake: 10 seconds
```

### Retry Configuration
```
Exponential Backoff:
- Base: RetryPollInterval (from config)
- Max: UpdatePollInterval (from config)
- Formula: min(base * 2^attempts, max)
```

### Network Quality Thresholds
```
WiFi Signal Strength (dBm):
- Excellent: > -50
- Good: > -60
- Fair: > -70
- Minimum: -75 (threshold)
- Poor: < -75 (reject download)
```

---

## 9. Database State Persistence

### State Data Stored During Download

```
StateData {
    Name: "update-store"
    UpdateInfo: {
        ID: "deployment-id"
        ArtifactName: "artifact-name"
        Artifact: {
            PayloadTypes: ["rootfs-image"]
        }
        SupportsRollback: RollbackSupported/NotSupported
    }
}
```

**When Saved:**
- Before starting payload download
- After determining rollback support
- Updated during state transitions

**Purpose:**
- Recovery after system restart
- Track update progress
- Enable proper cleanup on failure

---

## 10. Summary of Interruption Handling

| Interruption Type | Detection | Recovery Mechanism | Data Loss |
|-------------------|-----------|-------------------|-----------|
| **Network Timeout** | Read() error | Automatic resume with Range header | None (resume from offset) |
| **Connection Drop** | EOF before contentLength | Exponential backoff + retry | None (resume from offset) |
| **System Reboot** | Process termination | Init state detects incomplete download | Full download (restart) |
| **Power Loss** | Process termination | Init state → UpdateCleanup | Full download (restart) |
| **Process Kill** | Signal termination | Init state recovery | Full download (restart) |
| **Server Error (5xx)** | HTTP status code | Retry with backoff | None (resume from offset) |
| **Poor WiFi Signal** | Pre-download check | Defer download, retry later | None (not started) |

---

## 11. Best Practices & Recommendations

### For Reliable Downloads:
1. **Network Quality Check**: Always performed before download initiation
2. **Exponential Backoff**: Prevents server overload during retries
3. **Range Requests**: Minimizes data re-download on interruption
4. **State Persistence**: Enables recovery after system restart
5. **Signature Verification**: Only after complete download
6. **Timeout Configuration**: Generous for large files over slow networks

### For Debugging:
- Enable debug logging: `log.SetLevel(log.DebugLevel)`
- Monitor deployment logs: `/var/lib/mender/deployment-logs/`
- Check state data: Database key `state-data`
- Review network quality logs: Signal strength readings

---

## Document Version
- **Created**: 2026-01-19
- **Based on**: soc-ota-agent codebase
- **Key Files Analyzed**:
  - `client/update_resumer.go`
  - `client/client_update.go`
  - `client/network_quality.go`
  - `app/state.go`
  - `app/standalone.go`
