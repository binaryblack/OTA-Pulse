//! Coredump handling module
//!
//! Manages coredump capture, storage, and upload.

use crate::config::CrashConfig;
use crate::http::HttpClient;
use chrono::{DateTime, Utc};
use flate2::read::GzDecoder;
use flate2::write::GzEncoder;
use flate2::Compression;
use serde::{Deserialize, Serialize};
use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use thiserror::Error;
use tokio::sync::mpsc;
use tracing::{debug, error, info, warn};

#[derive(Error, Debug)]
pub enum CoredumpError {
    #[error("IO error: {0}")]
    IoError(#[from] std::io::Error),
    #[error("JSON error: {0}")]
    JsonError(#[from] serde_json::Error),
    #[error("Coredump too large: {size} bytes (max: {max} bytes)")]
    TooLarge { size: u64, max: u64 },
    #[error("Upload failed: {0}")]
    UploadError(String),
    #[error("Invalid coredump path")]
    InvalidPath,
}

/// Coredump metadata
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CoredumpMetadata {
    pub timestamp: i64,
    pub timestamp_iso: String,
    pub pid: u32,
    pub uid: u32,
    pub gid: u32,
    pub signal: i32,
    pub executable: String,
    pub hostname: String,
    pub coredump_file: String,
    pub coredump_size: u64,
    pub compressed: bool,
    pub uploaded: bool,
    #[serde(default)]
    pub upload_attempts: u32,
    #[serde(default)]
    pub last_upload_error: Option<String>,
    #[serde(default)]
    pub kernel_version: String,
    #[serde(default)]
    pub device_id: String,
    #[serde(default)]
    pub firmware_version: String,
}

/// Coredump event for IPC
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CoredumpEvent {
    pub event: String,
    pub file: String,
    pub meta: String,
}

/// Coredump manager
pub struct CoredumpManager {
    config: CrashConfig,
    storage_path: PathBuf,
}

impl CoredumpManager {
    pub fn new(config: &CrashConfig) -> Result<Self, CoredumpError> {
        let storage_path = PathBuf::from(&config.coredump_storage_path);

        // Ensure storage directory exists
        fs::create_dir_all(&storage_path)?;

        Ok(Self {
            config: config.clone(),
            storage_path,
        })
    }

    /// Save a coredump from stdin (called by kernel core_pattern)
    pub fn save_coredump(
        &self,
        pid: u32,
        uid: u32,
        gid: u32,
        signal: i32,
        executable: &str,
        hostname: &str,
        input: &mut dyn Read,
    ) -> Result<(PathBuf, PathBuf), CoredumpError> {
        let timestamp = Utc::now();
        let timestamp_unix = timestamp.timestamp();

        // Generate filenames
        let base_name = format!(
            "core.{}.{}.{}",
            executable.replace('/', "_"),
            pid,
            timestamp_unix
        );
        let coredump_file = self.storage_path.join(format!("{}.gz", base_name));
        let metadata_file = self.storage_path.join(format!("{}.meta", base_name));

        info!(
            "Saving coredump for {} (pid={}, signal={})",
            executable, pid, signal
        );

        // Read and compress the coredump
        let mut encoder = GzEncoder::new(
            fs::File::create(&coredump_file)?,
            Compression::default(),
        );

        let mut total_read: u64 = 0;
        let max_size = self.config.coredump_max_size_bytes;
        let mut buffer = [0u8; 65536];

        loop {
            let n = input.read(&mut buffer)?;
            if n == 0 {
                break;
            }

            total_read += n as u64;

            // Check size limit
            if total_read > max_size {
                warn!(
                    "Coredump exceeds max size ({} > {}), truncating",
                    total_read, max_size
                );
                encoder.write_all(&buffer[..n])?;
                break;
            }

            encoder.write_all(&buffer[..n])?;
        }

        encoder.finish()?;

        // Get compressed size
        let compressed_size = fs::metadata(&coredump_file)?.len();

        // Get kernel version
        let kernel_version = fs::read_to_string("/proc/version")
            .ok()
            .map(|v| v.lines().next().unwrap_or("").to_string())
            .unwrap_or_default();

        // Create metadata
        let metadata = CoredumpMetadata {
            timestamp: timestamp_unix,
            timestamp_iso: timestamp.to_rfc3339(),
            pid,
            uid,
            gid,
            signal,
            executable: executable.to_string(),
            hostname: hostname.to_string(),
            coredump_file: coredump_file.to_string_lossy().to_string(),
            coredump_size: compressed_size,
            compressed: true,
            uploaded: false,
            upload_attempts: 0,
            last_upload_error: None,
            kernel_version,
            device_id: String::new(),
            firmware_version: String::new(),
        };

        // Write metadata
        let metadata_json = serde_json::to_string_pretty(&metadata)?;
        fs::write(&metadata_file, metadata_json)?;

        info!(
            "Saved coredump: {} ({} bytes compressed)",
            coredump_file.display(),
            compressed_size
        );

        // Cleanup old coredumps if needed
        self.cleanup_old_coredumps()?;

        Ok((coredump_file, metadata_file))
    }

    /// List all pending coredumps
    pub fn list_pending(&self) -> Result<Vec<(PathBuf, PathBuf)>, CoredumpError> {
        let mut pending = Vec::new();

        for entry in fs::read_dir(&self.storage_path)? {
            let entry = entry?;
            let path = entry.path();

            if path.extension().map_or(false, |e| e == "meta") {
                let metadata: CoredumpMetadata = serde_json::from_str(&fs::read_to_string(&path)?)?;

                if !metadata.uploaded {
                    let coredump_path = PathBuf::from(&metadata.coredump_file);
                    if coredump_path.exists() {
                        pending.push((coredump_path, path));
                    }
                }
            }
        }

        // Sort by timestamp (oldest first)
        pending.sort_by(|a, b| a.1.cmp(&b.1));

        Ok(pending)
    }

    /// Mark a coredump as uploaded
    pub fn mark_uploaded(&self, metadata_path: &Path) -> Result<(), CoredumpError> {
        let content = fs::read_to_string(metadata_path)?;
        let mut metadata: CoredumpMetadata = serde_json::from_str(&content)?;

        metadata.uploaded = true;
        metadata.upload_attempts += 1;
        metadata.last_upload_error = None;

        let json = serde_json::to_string_pretty(&metadata)?;
        fs::write(metadata_path, json)?;

        Ok(())
    }

    /// Record upload failure
    pub fn record_upload_failure(&self, metadata_path: &Path, error: &str) -> Result<(), CoredumpError> {
        let content = fs::read_to_string(metadata_path)?;
        let mut metadata: CoredumpMetadata = serde_json::from_str(&content)?;

        metadata.upload_attempts += 1;
        metadata.last_upload_error = Some(error.to_string());

        let json = serde_json::to_string_pretty(&metadata)?;
        fs::write(metadata_path, json)?;

        Ok(())
    }

    /// Cleanup old coredumps to stay within storage limits
    fn cleanup_old_coredumps(&self) -> Result<(), CoredumpError> {
        const MAX_DIR_SIZE: u64 = 500 * 1024 * 1024; // 500MB

        // Calculate current directory size
        let mut total_size: u64 = 0;
        let mut files: Vec<(PathBuf, u64, std::time::SystemTime)> = Vec::new();

        for entry in fs::read_dir(&self.storage_path)? {
            let entry = entry?;
            let path = entry.path();
            let metadata = entry.metadata()?;

            if path.extension().map_or(false, |e| e == "gz") {
                let size = metadata.len();
                let modified = metadata.modified()?;
                total_size += size;
                files.push((path, size, modified));
            }
        }

        // Sort by modification time (oldest first)
        files.sort_by(|a, b| a.2.cmp(&b.2));

        // Remove oldest files until under limit
        while total_size > MAX_DIR_SIZE && !files.is_empty() {
            let (path, size, _) = files.remove(0);
            let meta_path = path.with_extension("meta");

            info!("Removing old coredump: {}", path.display());

            if let Err(e) = fs::remove_file(&path) {
                warn!("Failed to remove {}: {}", path.display(), e);
            } else {
                total_size -= size;
            }

            if meta_path.exists() {
                let _ = fs::remove_file(&meta_path);
            }
        }

        Ok(())
    }

    /// Get recent crash logs from journal (if enabled)
    pub fn get_recent_logs(&self, lines: u32) -> Option<String> {
        use std::process::Command;

        if !self.config.capture_logs_before_crash {
            return None;
        }

        // Try to get logs from journalctl
        let output = Command::new("journalctl")
            .args(["-n", &lines.to_string(), "--no-pager", "-o", "short-iso"])
            .output()
            .ok()?;

        if output.status.success() {
            String::from_utf8(output.stdout).ok()
        } else {
            // Fallback to dmesg
            let output = Command::new("dmesg")
                .args(["-T", "--read-clear"])
                .output()
                .ok()?;

            String::from_utf8(output.stdout).ok()
        }
    }
}

/// Coredump upload worker
pub struct CoredumpUploader {
    manager: CoredumpManager,
    http_client: HttpClient,
    max_retries: u32,
}

impl CoredumpUploader {
    pub fn new(manager: CoredumpManager, http_client: HttpClient, max_retries: u32) -> Self {
        Self {
            manager,
            http_client,
            max_retries,
        }
    }

    /// Upload all pending coredumps
    pub async fn upload_pending(&self) -> Result<u32, CoredumpError> {
        let pending = self.manager.list_pending()?;
        let mut uploaded_count = 0;

        for (coredump_path, metadata_path) in pending {
            // Check retry count
            let content = fs::read_to_string(&metadata_path)?;
            let metadata: CoredumpMetadata = serde_json::from_str(&content)?;

            if metadata.upload_attempts >= self.max_retries {
                debug!(
                    "Skipping coredump {} (max retries exceeded)",
                    coredump_path.display()
                );
                continue;
            }

            match self.http_client.upload_coredump(&coredump_path, &metadata_path).await {
                Ok(()) => {
                    self.manager.mark_uploaded(&metadata_path)?;
                    uploaded_count += 1;
                    info!("Uploaded coredump: {}", coredump_path.display());
                }
                Err(e) => {
                    error!("Failed to upload coredump {}: {}", coredump_path.display(), e);
                    self.manager.record_upload_failure(&metadata_path, &e.to_string())?;
                }
            }
        }

        Ok(uploaded_count)
    }
}

/// IPC listener for coredump notifications
pub struct CoredumpListener {
    socket_path: PathBuf,
}

impl CoredumpListener {
    pub fn new(socket_path: &str) -> Self {
        Self {
            socket_path: PathBuf::from(socket_path),
        }
    }

    /// Start listening for coredump events
    pub async fn listen(&self, sender: mpsc::Sender<CoredumpEvent>) -> Result<(), CoredumpError> {
        use tokio::net::UnixListener;

        // Remove existing socket
        let _ = fs::remove_file(&self.socket_path);

        // Create parent directory
        if let Some(parent) = self.socket_path.parent() {
            fs::create_dir_all(parent)?;
        }

        let listener = UnixListener::bind(&self.socket_path)?;
        info!("Coredump listener started on {:?}", self.socket_path);

        loop {
            match listener.accept().await {
                Ok((stream, _)) => {
                    let sender = sender.clone();
                    tokio::spawn(async move {
                        if let Err(e) = handle_coredump_connection(stream, sender).await {
                            error!("Error handling coredump connection: {}", e);
                        }
                    });
                }
                Err(e) => {
                    error!("Error accepting connection: {}", e);
                }
            }
        }
    }
}

async fn handle_coredump_connection(
    stream: tokio::net::UnixStream,
    sender: mpsc::Sender<CoredumpEvent>,
) -> Result<(), CoredumpError> {
    use tokio::io::AsyncReadExt;

    let mut stream = stream;
    let mut buffer = Vec::new();
    stream.read_to_end(&mut buffer).await?;

    let message = String::from_utf8_lossy(&buffer);
    debug!("Received coredump notification: {}", message);

    if let Ok(event) = serde_json::from_str::<CoredumpEvent>(&message) {
        if sender.send(event).await.is_err() {
            warn!("Failed to send coredump event to channel");
        }
    }

    Ok(())
}

/// Signal name lookup
pub fn signal_name(signal: i32) -> &'static str {
    match signal {
        1 => "SIGHUP",
        2 => "SIGINT",
        3 => "SIGQUIT",
        4 => "SIGILL",
        5 => "SIGTRAP",
        6 => "SIGABRT",
        7 => "SIGBUS",
        8 => "SIGFPE",
        9 => "SIGKILL",
        10 => "SIGUSR1",
        11 => "SIGSEGV",
        12 => "SIGUSR2",
        13 => "SIGPIPE",
        14 => "SIGALRM",
        15 => "SIGTERM",
        _ => "UNKNOWN",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_signal_name() {
        assert_eq!(signal_name(11), "SIGSEGV");
        assert_eq!(signal_name(6), "SIGABRT");
        assert_eq!(signal_name(999), "UNKNOWN");
    }
}
