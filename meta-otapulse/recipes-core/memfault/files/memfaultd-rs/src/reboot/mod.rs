//! Reboot reason tracking module
//!
//! Tracks and reports reboot reasons to the server.

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::Path;
use std::process::Command;
use thiserror::Error;
use tracing::{debug, info, warn};

#[derive(Error, Debug)]
pub enum RebootError {
    #[error("IO error: {0}")]
    IoError(#[from] std::io::Error),
    #[error("Failed to determine reboot reason")]
    UnknownReason,
}

/// Known reboot reasons
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum RebootReason {
    Unknown,
    PowerOn,
    UserRequested,
    SystemUpdate,
    OtaUpdate,
    Watchdog,
    KernelPanic,
    OutOfMemory,
    HardwareFault,
    ThermalShutdown,
    PowerFailure,
    Scheduled,
    RemoteCommand,
    ApplicationCrash,
    LowBattery,
    Custom(String),
}

impl std::fmt::Display for RebootReason {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            RebootReason::Unknown => write!(f, "unknown"),
            RebootReason::PowerOn => write!(f, "power_on"),
            RebootReason::UserRequested => write!(f, "user_requested"),
            RebootReason::SystemUpdate => write!(f, "system_update"),
            RebootReason::OtaUpdate => write!(f, "ota_update"),
            RebootReason::Watchdog => write!(f, "watchdog"),
            RebootReason::KernelPanic => write!(f, "kernel_panic"),
            RebootReason::OutOfMemory => write!(f, "out_of_memory"),
            RebootReason::HardwareFault => write!(f, "hardware_fault"),
            RebootReason::ThermalShutdown => write!(f, "thermal_shutdown"),
            RebootReason::PowerFailure => write!(f, "power_failure"),
            RebootReason::Scheduled => write!(f, "scheduled"),
            RebootReason::RemoteCommand => write!(f, "remote_command"),
            RebootReason::ApplicationCrash => write!(f, "application_crash"),
            RebootReason::LowBattery => write!(f, "low_battery"),
            RebootReason::Custom(s) => write!(f, "{}", s),
        }
    }
}

impl From<&str> for RebootReason {
    fn from(s: &str) -> Self {
        match s.to_lowercase().as_str() {
            "unknown" => RebootReason::Unknown,
            "power_on" | "poweron" | "cold_boot" => RebootReason::PowerOn,
            "user_requested" | "user" | "reboot" => RebootReason::UserRequested,
            "system_update" | "update" => RebootReason::SystemUpdate,
            "ota_update" | "ota" => RebootReason::OtaUpdate,
            "watchdog" | "wdt" | "watchdog_reset" => RebootReason::Watchdog,
            "kernel_panic" | "panic" | "oops" => RebootReason::KernelPanic,
            "out_of_memory" | "oom" => RebootReason::OutOfMemory,
            "hardware_fault" | "hw_fault" => RebootReason::HardwareFault,
            "thermal_shutdown" | "thermal" | "overheat" => RebootReason::ThermalShutdown,
            "power_failure" | "power_loss" => RebootReason::PowerFailure,
            "scheduled" => RebootReason::Scheduled,
            "remote_command" | "remote" => RebootReason::RemoteCommand,
            "application_crash" | "app_crash" | "memfaultd_failure" | "system_health_failure" => RebootReason::ApplicationCrash,
            "low_battery" | "battery" => RebootReason::LowBattery,
            _ => RebootReason::Custom(s.to_string()),
        }
    }
}

/// Reboot reason tracker
pub struct RebootTracker {
    reason_file: String,
    enabled: bool,
}

impl RebootTracker {
    pub fn new(reason_file: &str, enabled: bool) -> Self {
        // Ensure parent directory exists
        if let Some(parent) = Path::new(reason_file).parent() {
            let _ = fs::create_dir_all(parent);
        }

        Self {
            reason_file: reason_file.to_string(),
            enabled,
        }
    }

    /// Get the last reboot reason
    pub fn get_last_reason(&self) -> RebootReason {
        if !self.enabled {
            return RebootReason::Unknown;
        }

        // Try stored reason file first
        if let Ok(content) = fs::read_to_string(&self.reason_file) {
            let reason = content.trim();
            if !reason.is_empty() {
                debug!("Found stored reboot reason: {}", reason);
                return RebootReason::from(reason);
            }
        }

        // Try to detect from system logs
        self.detect_from_system()
    }

    /// Detect reboot reason from system state
    fn detect_from_system(&self) -> RebootReason {
        // Check dmesg for watchdog reset
        if self.check_dmesg_for("watchdog") {
            return RebootReason::Watchdog;
        }

        // Check for panic
        if self.check_dmesg_for("panic") || self.check_dmesg_for("Oops") {
            return RebootReason::KernelPanic;
        }

        // Check for OOM killer
        if self.check_dmesg_for("Out of memory") || self.check_dmesg_for("oom_reaper") {
            return RebootReason::OutOfMemory;
        }

        // Check for thermal shutdown
        if self.check_dmesg_for("thermal") && self.check_dmesg_for("shutdown") {
            return RebootReason::ThermalShutdown;
        }

        // Check kernel reboot reason (if supported by hardware)
        if let Ok(reason) = fs::read_to_string("/sys/kernel/reboot/reason") {
            let reason = reason.trim();
            if !reason.is_empty() && reason != "unknown" {
                return RebootReason::from(reason);
            }
        }

        // Check for pstore entries (kernel panic records)
        if Path::new("/sys/fs/pstore").exists() {
            if let Ok(entries) = fs::read_dir("/sys/fs/pstore") {
                for entry in entries.flatten() {
                    let name = entry.file_name();
                    let name_str = name.to_string_lossy();
                    if name_str.starts_with("dmesg-") {
                        // Found a pstore dmesg entry - indicates panic
                        return RebootReason::KernelPanic;
                    }
                }
            }
        }

        // Check boot count for potential rollback detection
        if let Ok(boot_count) = fs::read_to_string("/sys/firmware/efi/efivars/BootCount") {
            debug!("Boot count: {:?}", boot_count);
        }

        // Check last shutdown status from wtmp/utmp
        if let Some(reason) = self.check_wtmp() {
            return reason;
        }

        // Default to unknown
        RebootReason::Unknown
    }

    fn check_dmesg_for(&self, pattern: &str) -> bool {
        let output = Command::new("dmesg")
            .output()
            .ok()
            .map(|o| String::from_utf8_lossy(&o.stdout).to_string());

        if let Some(dmesg) = output {
            return dmesg.to_lowercase().contains(&pattern.to_lowercase());
        }

        false
    }

    fn check_wtmp(&self) -> Option<RebootReason> {
        // Use last command to check shutdown history
        let output = Command::new("last")
            .args(["-x", "-n", "5", "shutdown", "reboot"])
            .output()
            .ok()?;

        let output_str = String::from_utf8_lossy(&output.stdout);

        for line in output_str.lines() {
            if line.contains("shutdown") {
                // System was shut down cleanly
                return Some(RebootReason::UserRequested);
            }
            if line.contains("crash") {
                return Some(RebootReason::KernelPanic);
            }
        }

        None
    }

    /// Store a reboot reason (called before intentional reboot)
    pub fn store_reason(&self, reason: RebootReason) -> Result<(), RebootError> {
        if !self.enabled {
            return Ok(());
        }

        let reason_str = reason.to_string();
        fs::write(&self.reason_file, &reason_str)?;
        info!("Stored reboot reason: {}", reason_str);

        Ok(())
    }

    /// Clear the stored reboot reason
    pub fn clear_reason(&self) -> Result<(), RebootError> {
        if Path::new(&self.reason_file).exists() {
            fs::remove_file(&self.reason_file)?;
            debug!("Cleared reboot reason file");
        }

        Ok(())
    }

    /// Get uptime at last shutdown (if available)
    pub fn get_previous_uptime(&self) -> Option<f64> {
        // Try to read from stored file
        let uptime_file = format!("{}.uptime", self.reason_file);
        if let Ok(content) = fs::read_to_string(&uptime_file) {
            return content.trim().parse().ok();
        }

        None
    }

    /// Store current uptime (called before reboot)
    pub fn store_uptime(&self, uptime: f64) -> Result<(), RebootError> {
        let uptime_file = format!("{}.uptime", self.reason_file);
        fs::write(&uptime_file, uptime.to_string())?;
        Ok(())
    }

    /// Get current system uptime
    pub fn get_current_uptime() -> Option<f64> {
        fs::read_to_string("/proc/uptime")
            .ok()
            .and_then(|content| {
                content
                    .split_whitespace()
                    .next()
                    .and_then(|s| s.parse().ok())
            })
    }
}

/// Reboot history entry
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RebootHistoryEntry {
    pub timestamp: String,
    pub reason: String,
    pub uptime_before: Option<f64>,
    pub firmware_version: String,
}

/// Reboot history manager
pub struct RebootHistory {
    history_file: String,
    max_entries: usize,
}

impl RebootHistory {
    pub fn new(history_file: &str, max_entries: usize) -> Self {
        Self {
            history_file: history_file.to_string(),
            max_entries,
        }
    }

    /// Add an entry to the history
    pub fn add_entry(&self, entry: RebootHistoryEntry) -> Result<(), RebootError> {
        let mut history = self.load()?;

        history.push(entry);

        // Keep only the most recent entries
        while history.len() > self.max_entries {
            history.remove(0);
        }

        self.save(&history)?;

        Ok(())
    }

    /// Load history from file
    pub fn load(&self) -> Result<Vec<RebootHistoryEntry>, RebootError> {
        if !Path::new(&self.history_file).exists() {
            return Ok(Vec::new());
        }

        let content = fs::read_to_string(&self.history_file)?;
        let history: Vec<RebootHistoryEntry> = serde_json::from_str(&content)
            .unwrap_or_default();

        Ok(history)
    }

    /// Save history to file
    fn save(&self, history: &[RebootHistoryEntry]) -> Result<(), RebootError> {
        let json = serde_json::to_string_pretty(history)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;
        fs::write(&self.history_file, json)?;
        Ok(())
    }

    /// Get the count of recent reboots within a time window
    pub fn count_recent_reboots(&self, within_seconds: u64) -> usize {
        let history = self.load().unwrap_or_default();
        let now = chrono::Utc::now();

        history
            .iter()
            .filter(|entry| {
                if let Ok(ts) = chrono::DateTime::parse_from_rfc3339(&entry.timestamp) {
                    let age = (now - ts.with_timezone(&chrono::Utc)).num_seconds();
                    age >= 0 && age < within_seconds as i64
                } else {
                    false
                }
            })
            .count()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_reboot_reason_from_string() {
        assert_eq!(RebootReason::from("watchdog"), RebootReason::Watchdog);
        assert_eq!(RebootReason::from("oom"), RebootReason::OutOfMemory);
        assert_eq!(RebootReason::from("user"), RebootReason::UserRequested);
        assert!(matches!(RebootReason::from("custom_reason"), RebootReason::Custom(_)));
    }

    #[test]
    fn test_reboot_reason_display() {
        assert_eq!(RebootReason::Watchdog.to_string(), "watchdog");
        assert_eq!(RebootReason::KernelPanic.to_string(), "kernel_panic");
    }
}
