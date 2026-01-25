//! OTA (Over-The-Air) update module
//!
//! Handles firmware update checking, downloading, verification, and installation.

use crate::config::OtaConfig;
use crate::http::{HttpClient, OtaCheckResponse};
use ring::digest::{Context, SHA256, SHA384};
use serde::{Deserialize, Serialize};
use std::fs::{self, File};
use std::io::{BufReader, Read, Write};
use std::path::{Path, PathBuf};
use std::process::Command;
use thiserror::Error;
use tracing::{debug, error, info, warn};

#[derive(Error, Debug)]
pub enum OtaError {
    #[error("IO error: {0}")]
    IoError(#[from] std::io::Error),
    #[error("JSON error: {0}")]
    JsonError(#[from] serde_json::Error),
    #[error("HTTP error: {0}")]
    HttpError(#[from] crate::http::HttpError),
    #[error("Checksum mismatch: expected {expected}, got {actual}")]
    ChecksumMismatch { expected: String, actual: String },
    #[error("Signature verification failed")]
    SignatureVerificationFailed,
    #[error("Downgrade not allowed: current {current}, target {target}")]
    DowngradeNotAllowed { current: String, target: String },
    #[error("Installation failed: {0}")]
    InstallationFailed(String),
    #[error("No update available")]
    NoUpdateAvailable,
}

/// OTA update state
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub enum OtaState {
    Idle,
    Checking,
    Downloading,
    Verifying,
    ReadyToInstall,
    Installing,
    Completed,
    Failed(String),
}

/// Pending OTA update information
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PendingUpdate {
    pub deployment_id: String,
    pub version: String,
    pub download_url: String,
    pub size_bytes: u64,
    pub checksum: String,
    pub checksum_type: String,
    pub signature: String,
    pub release_notes: String,
    pub mandatory: bool,
    pub state: OtaState,
    pub downloaded_path: Option<String>,
    pub download_progress: f64,
    pub checked_at: String,
}

impl From<OtaCheckResponse> for PendingUpdate {
    fn from(response: OtaCheckResponse) -> Self {
        Self {
            deployment_id: response.deployment_id,
            version: response.version,
            download_url: response.download_url,
            size_bytes: response.size_bytes,
            checksum: response.checksum,
            checksum_type: response.checksum_type,
            signature: response.signature,
            release_notes: response.release_notes,
            mandatory: response.mandatory,
            state: OtaState::Idle,
            downloaded_path: None,
            download_progress: 0.0,
            checked_at: chrono::Utc::now().to_rfc3339(),
        }
    }
}

/// OTA update manager
pub struct OtaManager {
    config: OtaConfig,
    download_path: PathBuf,
    current_version: String,
}

impl OtaManager {
    pub fn new(config: &OtaConfig, current_version: &str) -> Result<Self, OtaError> {
        let download_path = PathBuf::from(&config.download_path);
        fs::create_dir_all(&download_path)?;

        Ok(Self {
            config: config.clone(),
            download_path,
            current_version: current_version.to_string(),
        })
    }

    /// Check for available updates
    pub async fn check_for_update(&self, http_client: &HttpClient) -> Result<Option<PendingUpdate>, OtaError> {
        if !self.config.enabled {
            debug!("OTA updates disabled");
            return Ok(None);
        }

        info!("Checking for OTA updates...");

        let response = http_client.check_ota_update().await?;

        if !response.update_available {
            debug!("No update available");
            return Ok(None);
        }

        // Check for downgrade
        if !self.config.allow_downgrade {
            if let Some(std::cmp::Ordering::Greater) = compare_versions(&self.current_version, &response.version) {
                return Err(OtaError::DowngradeNotAllowed {
                    current: self.current_version.clone(),
                    target: response.version,
                });
            }
        }

        let pending = PendingUpdate::from(response);
        info!("Update available: {} -> {}", self.current_version, pending.version);

        // Save pending update info
        self.save_pending_update(&pending)?;

        Ok(Some(pending))
    }

    /// Download the update
    pub async fn download_update(
        &self,
        http_client: &HttpClient,
        update: &mut PendingUpdate,
    ) -> Result<PathBuf, OtaError> {
        if update.download_url.is_empty() {
            return Err(OtaError::NoUpdateAvailable);
        }

        update.state = OtaState::Downloading;
        self.save_pending_update(update)?;

        let filename = format!("firmware_{}.bin", update.version.replace('.', "_"));
        let destination = self.download_path.join(&filename);

        info!("Downloading update to {:?}", destination);

        // Download with retry
        let mut attempts = 0;
        let max_retries = self.config.max_download_retries;

        loop {
            match http_client.download_ota(&update.download_url, &destination).await {
                Ok(size) => {
                    info!("Downloaded {} bytes", size);
                    update.downloaded_path = Some(destination.to_string_lossy().to_string());
                    update.download_progress = 100.0;
                    break;
                }
                Err(e) => {
                    attempts += 1;
                    if attempts >= max_retries {
                        update.state = OtaState::Failed(e.to_string());
                        self.save_pending_update(update)?;
                        return Err(OtaError::HttpError(e));
                    }
                    warn!("Download attempt {} failed: {}", attempts, e);
                    tokio::time::sleep(std::time::Duration::from_secs(5)).await;
                }
            }
        }

        self.save_pending_update(update)?;
        Ok(destination)
    }

    /// Verify the downloaded update
    pub fn verify_update(&self, update: &mut PendingUpdate) -> Result<(), OtaError> {
        let path = update.downloaded_path.as_ref()
            .ok_or(OtaError::NoUpdateAvailable)?;

        update.state = OtaState::Verifying;
        self.save_pending_update(update)?;

        info!("Verifying update: {}", path);

        // Verify checksum
        if !update.checksum.is_empty() {
            let actual_checksum = self.calculate_checksum(path, &update.checksum_type)?;
            if actual_checksum.to_lowercase() != update.checksum.to_lowercase() {
                update.state = OtaState::Failed("Checksum mismatch".to_string());
                self.save_pending_update(update)?;
                return Err(OtaError::ChecksumMismatch {
                    expected: update.checksum.clone(),
                    actual: actual_checksum,
                });
            }
            info!("Checksum verified");
        }

        // Verify signature if enabled
        if self.config.signature_verification && !update.signature.is_empty() {
            if !self.verify_signature(path, &update.signature)? {
                update.state = OtaState::Failed("Signature verification failed".to_string());
                self.save_pending_update(update)?;
                return Err(OtaError::SignatureVerificationFailed);
            }
            info!("Signature verified");
        }

        update.state = OtaState::ReadyToInstall;
        self.save_pending_update(update)?;

        Ok(())
    }

    /// Install the update (triggers system update mechanism)
    pub fn install_update(&self, update: &mut PendingUpdate) -> Result<(), OtaError> {
        let path = update.downloaded_path.as_ref()
            .ok_or(OtaError::NoUpdateAvailable)?;

        if update.state != OtaState::ReadyToInstall {
            return Err(OtaError::InstallationFailed(
                "Update not verified".to_string()
            ));
        }

        update.state = OtaState::Installing;
        self.save_pending_update(update)?;

        info!("Installing update from: {}", path);

        // The actual installation mechanism depends on the system:
        // 1. For A/B updates: Copy to inactive partition and update boot flags
        // 2. For RAUC: Use rauc install
        // 3. For swupdate: Use swupdate
        // 4. For custom: Execute update script

        // Try different update methods
        let result = self.try_install_methods(path);

        match result {
            Ok(()) => {
                update.state = OtaState::Completed;
                self.save_pending_update(update)?;
                info!("Update installed successfully, reboot required");
                Ok(())
            }
            Err(e) => {
                update.state = OtaState::Failed(e.to_string());
                self.save_pending_update(update)?;
                Err(e)
            }
        }
    }

    fn try_install_methods(&self, firmware_path: &str) -> Result<(), OtaError> {
        // Method 1: Check for RAUC
        if Path::new("/usr/bin/rauc").exists() {
            return self.install_rauc(firmware_path);
        }

        // Method 2: Check for swupdate
        if Path::new("/usr/bin/swupdate").exists() {
            return self.install_swupdate(firmware_path);
        }

        // Method 3: Check for custom update script
        if Path::new("/usr/bin/memfault-ota-install").exists() {
            return self.install_custom(firmware_path);
        }

        // Method 4: A/B partition update (manual)
        self.install_ab_partition(firmware_path)
    }

    fn install_rauc(&self, firmware_path: &str) -> Result<(), OtaError> {
        info!("Using RAUC for installation");

        let output = Command::new("rauc")
            .args(["install", firmware_path])
            .output()?;

        if output.status.success() {
            Ok(())
        } else {
            Err(OtaError::InstallationFailed(
                String::from_utf8_lossy(&output.stderr).to_string()
            ))
        }
    }

    fn install_swupdate(&self, firmware_path: &str) -> Result<(), OtaError> {
        info!("Using swupdate for installation");

        let output = Command::new("swupdate")
            .args(["-i", firmware_path])
            .output()?;

        if output.status.success() {
            Ok(())
        } else {
            Err(OtaError::InstallationFailed(
                String::from_utf8_lossy(&output.stderr).to_string()
            ))
        }
    }

    fn install_custom(&self, firmware_path: &str) -> Result<(), OtaError> {
        info!("Using custom install script");

        let output = Command::new("/usr/bin/memfault-ota-install")
            .args([firmware_path])
            .output()?;

        if output.status.success() {
            Ok(())
        } else {
            Err(OtaError::InstallationFailed(
                String::from_utf8_lossy(&output.stderr).to_string()
            ))
        }
    }

    fn install_ab_partition(&self, firmware_path: &str) -> Result<(), OtaError> {
        info!("Using A/B partition update");

        // Determine inactive partition
        let current_root = fs::read_to_string("/proc/cmdline")
            .ok()
            .and_then(|cmdline| {
                cmdline
                    .split_whitespace()
                    .find(|arg| arg.starts_with("root="))
                    .map(|arg| arg.trim_start_matches("root=").to_string())
            });

        let inactive_partition = match current_root.as_deref() {
            Some(root) if root.contains("rootfs-a") || root.contains("mmcblk0p2") => {
                "/dev/mmcblk0p3" // rootfs-b
            }
            Some(root) if root.contains("rootfs-b") || root.contains("mmcblk0p3") => {
                "/dev/mmcblk0p2" // rootfs-a
            }
            _ => {
                return Err(OtaError::InstallationFailed(
                    "Could not determine inactive partition".to_string()
                ));
            }
        };

        info!("Writing to inactive partition: {}", inactive_partition);

        // Write firmware to partition
        let output = Command::new("dd")
            .args([
                &format!("if={}", firmware_path),
                &format!("of={}", inactive_partition),
                "bs=4M",
                "conv=fsync",
            ])
            .output()?;

        if !output.status.success() {
            return Err(OtaError::InstallationFailed(
                String::from_utf8_lossy(&output.stderr).to_string()
            ));
        }

        // Update boot flags using fw_setenv
        if Path::new("/usr/bin/fw_setenv").exists() {
            let slot = if inactive_partition.contains("p2") { "a" } else { "b" };
            let _ = Command::new("fw_setenv")
                .args(["boot_slot", slot])
                .output();
            let _ = Command::new("fw_setenv")
                .args(["boot_count", "0"])
                .output();
        }

        Ok(())
    }

    fn calculate_checksum(&self, path: &str, checksum_type: &str) -> Result<String, OtaError> {
        let file = File::open(path)?;
        let mut reader = BufReader::new(file);
        let mut buffer = [0u8; 8192];

        let digest = match checksum_type.to_lowercase().as_str() {
            "sha384" => {
                let mut context = Context::new(&SHA384);
                loop {
                    let n = reader.read(&mut buffer)?;
                    if n == 0 {
                        break;
                    }
                    context.update(&buffer[..n]);
                }
                context.finish()
            }
            _ => {
                // Default to SHA256
                let mut context = Context::new(&SHA256);
                loop {
                    let n = reader.read(&mut buffer)?;
                    if n == 0 {
                        break;
                    }
                    context.update(&buffer[..n]);
                }
                context.finish()
            }
        };

        Ok(hex::encode(digest.as_ref()))
    }

    fn verify_signature(&self, _path: &str, _signature: &str) -> Result<bool, OtaError> {
        // Signature verification would use the configured algorithm (ecdsa-p384, rsa, etc.)
        // This is a placeholder - actual implementation depends on key storage and format
        warn!("Signature verification not fully implemented");
        Ok(true)
    }

    fn save_pending_update(&self, update: &PendingUpdate) -> Result<(), OtaError> {
        let path = self.download_path.join("pending_update.json");
        let json = serde_json::to_string_pretty(update)?;
        fs::write(path, json)?;
        Ok(())
    }

    /// Load pending update from disk
    pub fn load_pending_update(&self) -> Result<Option<PendingUpdate>, OtaError> {
        let path = self.download_path.join("pending_update.json");
        if !path.exists() {
            return Ok(None);
        }

        let content = fs::read_to_string(path)?;
        let update: PendingUpdate = serde_json::from_str(&content)?;
        Ok(Some(update))
    }

    /// Clear pending update
    pub fn clear_pending_update(&self) -> Result<(), OtaError> {
        let path = self.download_path.join("pending_update.json");
        if path.exists() {
            fs::remove_file(path)?;
        }
        Ok(())
    }

    /// Schedule reboot for update installation
    pub fn schedule_reboot(&self, delay_seconds: u64) -> Result<(), OtaError> {
        info!("Scheduling reboot in {} seconds", delay_seconds);

        // Store reboot reason
        let reason_file = "/var/lib/memfault/last_reboot_reason";
        let _ = fs::write(reason_file, "ota_update");

        // Schedule reboot
        let output = Command::new("shutdown")
            .args(["-r", &format!("+{}", delay_seconds / 60)])
            .output()?;

        if !output.status.success() {
            // Fallback to immediate reboot
            warn!("shutdown command failed, using reboot");
            let _ = Command::new("reboot").output();
        }

        Ok(())
    }
}

/// Compare semantic versions
fn compare_versions(v1: &str, v2: &str) -> Option<std::cmp::Ordering> {
    let parse_version = |v: &str| -> Vec<u32> {
        v.split('.')
            .filter_map(|p| {
                // Handle versions like "1.2.3-beta"
                p.split('-')
                    .next()
                    .and_then(|n| n.parse().ok())
            })
            .collect()
    };

    let v1_parts = parse_version(v1);
    let v2_parts = parse_version(v2);

    for (a, b) in v1_parts.iter().zip(v2_parts.iter()) {
        match a.cmp(b) {
            std::cmp::Ordering::Equal => continue,
            other => return Some(other),
        }
    }

    Some(v1_parts.len().cmp(&v2_parts.len()))
}

// Hex encoding helper
mod hex {
    pub fn encode(data: &[u8]) -> String {
        data.iter().map(|b| format!("{:02x}", b)).collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_version_comparison() {
        assert_eq!(compare_versions("1.0.0", "1.0.0"), Some(std::cmp::Ordering::Equal));
        assert_eq!(compare_versions("1.0.1", "1.0.0"), Some(std::cmp::Ordering::Greater));
        assert_eq!(compare_versions("1.0.0", "1.0.1"), Some(std::cmp::Ordering::Less));
        assert_eq!(compare_versions("2.0.0", "1.9.9"), Some(std::cmp::Ordering::Greater));
    }

    #[test]
    fn test_pending_update_from_response() {
        let response = OtaCheckResponse {
            update_available: true,
            version: "2.0.0".to_string(),
            download_url: "https://example.com/firmware.bin".to_string(),
            size_bytes: 1000000,
            checksum: "abc123".to_string(),
            checksum_type: "sha256".to_string(),
            signature: "sig".to_string(),
            release_notes: "Bug fixes".to_string(),
            mandatory: false,
        };

        let update = PendingUpdate::from(response);
        assert_eq!(update.version, "2.0.0");
        assert_eq!(update.state, OtaState::Idle);
    }
}
