//! Watchdog integration module
//!
//! Handles both hardware and software watchdog management.

use crate::config::WatchdogConfig;
use std::fs::{File, OpenOptions};
use std::io::{self, Write};
use std::os::unix::io::{AsRawFd, RawFd};
use std::path::Path;
use std::time::Duration;
use thiserror::Error;
use tracing::{debug, error, info, warn};

#[derive(Error, Debug)]
pub enum WatchdogError {
    #[error("IO error: {0}")]
    IoError(#[from] io::Error),
    #[error("Watchdog device not available")]
    NotAvailable,
    #[error("Failed to set timeout: {0}")]
    SetTimeoutFailed(String),
    #[error("Ioctl error: {0}")]
    IoctlError(i32),
}

// Linux watchdog ioctl constants
const WATCHDOG_IOCTL_BASE: u8 = b'W';
const WDIOC_GETSUPPORT: libc::c_ulong = 0x80285700;
const WDIOC_SETTIMEOUT: libc::c_ulong = 0xc0045706;
const WDIOC_GETTIMEOUT: libc::c_ulong = 0x80045707;
const WDIOC_KEEPALIVE: libc::c_ulong = 0x80045705;
const WDIOC_SETOPTIONS: libc::c_ulong = 0x80045704;
const WDIOS_ENABLECARD: i32 = 0x0002;
const WDIOS_DISABLECARD: i32 = 0x0001;

/// Watchdog device information
#[repr(C)]
#[derive(Debug, Default)]
struct WatchdogInfo {
    options: u32,
    firmware_version: u32,
    identity: [u8; 32],
}

/// Hardware watchdog manager
pub struct HardwareWatchdog {
    device: Option<File>,
    timeout: Duration,
    magic_close: bool,
}

impl HardwareWatchdog {
    /// Open the hardware watchdog device
    pub fn new(config: &WatchdogConfig) -> Result<Self, WatchdogError> {
        let device_path = "/dev/watchdog";

        if !Path::new(device_path).exists() {
            info!("Hardware watchdog device not available");
            return Ok(Self {
                device: None,
                timeout: Duration::from_secs(config.timeout_seconds),
                magic_close: true,
            });
        }

        let device = OpenOptions::new()
            .write(true)
            .open(device_path)?;

        let mut watchdog = Self {
            device: Some(device),
            timeout: Duration::from_secs(config.timeout_seconds),
            magic_close: true,
        };

        // Set the timeout
        watchdog.set_timeout(config.timeout_seconds)?;

        info!(
            "Hardware watchdog initialized (timeout={}s)",
            config.timeout_seconds
        );

        Ok(watchdog)
    }

    /// Check if hardware watchdog is available
    pub fn is_available(&self) -> bool {
        self.device.is_some()
    }

    /// Set the watchdog timeout
    pub fn set_timeout(&mut self, seconds: u64) -> Result<(), WatchdogError> {
        if let Some(ref device) = self.device {
            let mut timeout = seconds as i32;

            unsafe {
                let result = libc::ioctl(device.as_raw_fd(), WDIOC_SETTIMEOUT, &mut timeout);
                if result < 0 {
                    return Err(WatchdogError::IoctlError(result));
                }
            }

            self.timeout = Duration::from_secs(timeout as u64);
            debug!("Watchdog timeout set to {} seconds", timeout);
        }

        Ok(())
    }

    /// Get the current timeout
    pub fn get_timeout(&self) -> Result<u64, WatchdogError> {
        if let Some(ref device) = self.device {
            let mut timeout: i32 = 0;

            unsafe {
                let result = libc::ioctl(device.as_raw_fd(), WDIOC_GETTIMEOUT, &mut timeout);
                if result < 0 {
                    return Err(WatchdogError::IoctlError(result));
                }
            }

            Ok(timeout as u64)
        } else {
            Ok(self.timeout.as_secs())
        }
    }

    /// Pet (kick) the watchdog to prevent reboot
    pub fn pet(&mut self) -> Result<(), WatchdogError> {
        if let Some(ref mut device) = self.device {
            // Writing any character to the watchdog device pets it
            device.write_all(b".")?;
            debug!("Watchdog pet");
        }

        Ok(())
    }

    /// Pet using ioctl (alternative method)
    pub fn pet_ioctl(&self) -> Result<(), WatchdogError> {
        if let Some(ref device) = self.device {
            unsafe {
                let result = libc::ioctl(device.as_raw_fd(), WDIOC_KEEPALIVE, 0);
                if result < 0 {
                    return Err(WatchdogError::IoctlError(result));
                }
            }
            debug!("Watchdog pet (ioctl)");
        }

        Ok(())
    }

    /// Enable the watchdog
    pub fn enable(&self) -> Result<(), WatchdogError> {
        if let Some(ref device) = self.device {
            let flags = WDIOS_ENABLECARD;

            unsafe {
                let result = libc::ioctl(device.as_raw_fd(), WDIOC_SETOPTIONS, &flags);
                if result < 0 {
                    return Err(WatchdogError::IoctlError(result));
                }
            }

            info!("Hardware watchdog enabled");
        }

        Ok(())
    }

    /// Disable the watchdog (if supported)
    pub fn disable(&mut self) -> Result<(), WatchdogError> {
        if let Some(ref device) = self.device {
            let flags = WDIOS_DISABLECARD;

            unsafe {
                let result = libc::ioctl(device.as_raw_fd(), WDIOC_SETOPTIONS, &flags);
                if result < 0 {
                    // Many watchdogs don't support disabling
                    warn!("Failed to disable watchdog (may not be supported)");
                }
            }
        }

        Ok(())
    }

    /// Gracefully close the watchdog (prevents reboot)
    pub fn graceful_close(&mut self) -> Result<(), WatchdogError> {
        if let Some(ref mut device) = self.device {
            if self.magic_close {
                // Write magic close character 'V' to prevent reboot
                device.write_all(b"V")?;
                info!("Watchdog gracefully closed");
            }
        }

        self.device = None;
        Ok(())
    }

    /// Get watchdog info
    pub fn get_info(&self) -> Result<String, WatchdogError> {
        if let Some(ref device) = self.device {
            let mut info = WatchdogInfo::default();

            unsafe {
                let result = libc::ioctl(device.as_raw_fd(), WDIOC_GETSUPPORT, &mut info);
                if result < 0 {
                    return Err(WatchdogError::IoctlError(result));
                }
            }

            let identity = std::str::from_utf8(&info.identity)
                .unwrap_or("unknown")
                .trim_end_matches('\0')
                .to_string();

            Ok(format!(
                "Watchdog: {} (options: 0x{:x}, firmware: {})",
                identity, info.options, info.firmware_version
            ))
        } else {
            Ok("No hardware watchdog".to_string())
        }
    }
}

impl Drop for HardwareWatchdog {
    fn drop(&mut self) {
        if let Err(e) = self.graceful_close() {
            error!("Failed to gracefully close watchdog: {}", e);
        }
    }
}

/// Software watchdog for monitoring services
pub struct SoftwareWatchdog {
    timeout: Duration,
    last_pet: std::time::Instant,
    enabled: bool,
}

impl SoftwareWatchdog {
    pub fn new(timeout_seconds: u64) -> Self {
        Self {
            timeout: Duration::from_secs(timeout_seconds),
            last_pet: std::time::Instant::now(),
            enabled: true,
        }
    }

    pub fn pet(&mut self) {
        self.last_pet = std::time::Instant::now();
    }

    pub fn is_expired(&self) -> bool {
        self.enabled && self.last_pet.elapsed() > self.timeout
    }

    pub fn time_remaining(&self) -> Duration {
        let elapsed = self.last_pet.elapsed();
        if elapsed >= self.timeout {
            Duration::ZERO
        } else {
            self.timeout - elapsed
        }
    }

    pub fn enable(&mut self) {
        self.enabled = true;
        self.pet();
    }

    pub fn disable(&mut self) {
        self.enabled = false;
    }
}

/// Combined watchdog manager for systemd integration
pub struct WatchdogManager {
    hardware: Option<HardwareWatchdog>,
    pet_interval: Duration,
    enabled: bool,
}

impl WatchdogManager {
    pub fn new(config: &WatchdogConfig) -> Result<Self, WatchdogError> {
        let hardware = match HardwareWatchdog::new(config) {
            Ok(hw) => Some(hw),
            Err(e) => {
                warn!("Failed to initialize hardware watchdog: {}", e);
                None
            }
        };

        Ok(Self {
            hardware,
            pet_interval: Duration::from_secs(config.pet_interval_seconds),
            enabled: config.enabled,
        })
    }

    pub fn is_enabled(&self) -> bool {
        self.enabled
    }

    pub fn pet_interval(&self) -> Duration {
        self.pet_interval
    }

    pub fn pet(&mut self) -> Result<(), WatchdogError> {
        if !self.enabled {
            return Ok(());
        }

        if let Some(ref mut hw) = self.hardware {
            hw.pet()?;
        }

        Ok(())
    }

    pub fn shutdown(&mut self) -> Result<(), WatchdogError> {
        if let Some(ref mut hw) = self.hardware {
            hw.graceful_close()?;
        }
        Ok(())
    }
}

/// Systemd watchdog notification
#[cfg(target_os = "linux")]
pub mod systemd {
    use std::env;
    use std::os::unix::net::UnixDatagram;
    use std::time::Duration;

    /// Check if systemd watchdog is enabled
    pub fn is_enabled() -> bool {
        env::var("WATCHDOG_USEC").is_ok() && env::var("NOTIFY_SOCKET").is_ok()
    }

    /// Get the watchdog timeout from systemd
    pub fn get_timeout() -> Option<Duration> {
        env::var("WATCHDOG_USEC")
            .ok()
            .and_then(|v| v.parse::<u64>().ok())
            .map(|usec| Duration::from_micros(usec))
    }

    /// Notify systemd that the service is ready
    pub fn notify_ready() -> bool {
        notify("READY=1")
    }

    /// Notify systemd watchdog (keep-alive)
    pub fn notify_watchdog() -> bool {
        notify("WATCHDOG=1")
    }

    /// Notify systemd that the service is stopping
    pub fn notify_stopping() -> bool {
        notify("STOPPING=1")
    }

    /// Notify systemd of status
    pub fn notify_status(status: &str) -> bool {
        notify(&format!("STATUS={}", status))
    }

    /// Send notification to systemd
    fn notify(message: &str) -> bool {
        let socket_path = match env::var("NOTIFY_SOCKET") {
            Ok(path) => path,
            Err(_) => return false,
        };

        let socket = match UnixDatagram::unbound() {
            Ok(s) => s,
            Err(_) => return false,
        };

        let path = if socket_path.starts_with('@') {
            // Abstract socket
            format!("\0{}", &socket_path[1..])
        } else {
            socket_path
        };

        socket.send_to(message.as_bytes(), &path).is_ok()
    }
}

#[cfg(not(target_os = "linux"))]
pub mod systemd {
    use std::time::Duration;

    pub fn is_enabled() -> bool {
        false
    }

    pub fn get_timeout() -> Option<Duration> {
        None
    }

    pub fn notify_ready() -> bool {
        true
    }

    pub fn notify_watchdog() -> bool {
        true
    }

    pub fn notify_stopping() -> bool {
        true
    }

    pub fn notify_status(_status: &str) -> bool {
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_software_watchdog() {
        let mut sw = SoftwareWatchdog::new(10);
        assert!(!sw.is_expired());

        sw.pet();
        assert!(!sw.is_expired());
    }

    #[test]
    fn test_software_watchdog_disabled() {
        let mut sw = SoftwareWatchdog::new(0);
        sw.disable();
        assert!(!sw.is_expired());
    }
}
