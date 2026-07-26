//! socmond-watchdog - System Health Monitor
//!
//! Monitors system health and socmond status, triggers reboot on failure.

use clap::Parser;
use std::fs;
use std::io::Write;
use std::os::unix::io::AsRawFd;
use std::path::Path;
use std::process::Command;
use std::time::{Duration, Instant};
use tracing::{debug, error, info, warn, Level};
use tracing_subscriber::EnvFilter;

/// System Health Monitor for SoC Monitoring
#[derive(Parser, Debug)]
#[command(name = "socmond-watchdog")]
#[command(version = env!("CARGO_PKG_VERSION"))]
#[command(about = "Monitors system health and triggers reboot on failure")]
struct Args {
    /// Path to configuration file
    #[arg(short, long, default_value = "/etc/socmond/config.json")]
    config: String,

    /// Watchdog timeout in seconds
    #[arg(short, long, default_value = "30")]
    timeout: u64,

    /// Pet interval in seconds
    #[arg(short, long, default_value = "10")]
    pet_interval: u64,

    /// Maximum consecutive failures before reboot
    #[arg(short, long, default_value = "3")]
    max_failures: u32,

    /// Log level
    #[arg(short, long, default_value = "info")]
    log_level: String,
}

/// Configuration loaded from JSON
#[derive(Debug, serde::Deserialize)]
struct Config {
    #[serde(default)]
    watchdog: WatchdogConfig,
}

#[derive(Debug, serde::Deserialize, Default)]
struct WatchdogConfig {
    #[serde(default = "default_timeout")]
    timeout_seconds: u64,
    #[serde(default = "default_pet_interval")]
    pet_interval_seconds: u64,
}

fn default_timeout() -> u64 {
    30
}

fn default_pet_interval() -> u64 {
    10
}

/// Hardware watchdog handle
struct HardwareWatchdog {
    file: Option<std::fs::File>,
}

impl HardwareWatchdog {
    fn new() -> Self {
        let file = std::fs::OpenOptions::new()
            .write(true)
            .open("/dev/watchdog")
            .ok();

        if file.is_some() {
            info!("Hardware watchdog device opened");
        } else {
            info!("Hardware watchdog not available, using software monitoring only");
        }

        Self { file }
    }

    fn pet(&mut self) -> bool {
        if let Some(ref mut file) = self.file {
            if file.write_all(b".").is_ok() {
                return true;
            }
        }
        false
    }

    fn close_gracefully(&mut self) {
        if let Some(ref mut file) = self.file {
            // Write magic 'V' to disable watchdog on close
            let _ = file.write_all(b"V");
            info!("Hardware watchdog closed gracefully");
        }
        self.file = None;
    }
}

impl Drop for HardwareWatchdog {
    fn drop(&mut self) {
        self.close_gracefully();
    }
}

fn main() {
    let args = Args::parse();

    // Initialize logging
    let level = match args.log_level.to_lowercase().as_str() {
        "trace" => Level::TRACE,
        "debug" => Level::DEBUG,
        "info" => Level::INFO,
        "warn" => Level::WARN,
        "error" => Level::ERROR,
        _ => Level::INFO,
    };

    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env().add_directive(level.into()))
        .with_target(false)
        .init();

    info!("socmond-watchdog {} starting", env!("CARGO_PKG_VERSION"));

    // Load configuration
    let (timeout, pet_interval) = if Path::new(&args.config).exists() {
        if let Ok(content) = fs::read_to_string(&args.config) {
            if let Ok(config) = serde_json::from_str::<Config>(&content) {
                (
                    config.watchdog.timeout_seconds,
                    config.watchdog.pet_interval_seconds,
                )
            } else {
                (args.timeout, args.pet_interval)
            }
        } else {
            (args.timeout, args.pet_interval)
        }
    } else {
        (args.timeout, args.pet_interval)
    };

    info!(
        "Configuration: timeout={}s, pet_interval={}s, max_failures={}",
        timeout, pet_interval, args.max_failures
    );

    // Open hardware watchdog
    let mut hw_watchdog = HardwareWatchdog::new();

    // Set up signal handlers
    let running = std::sync::Arc::new(std::sync::atomic::AtomicBool::new(true));
    let r = running.clone();

    ctrlc::set_handler(move || {
        info!("Received shutdown signal");
        r.store(false, std::sync::atomic::Ordering::SeqCst);
    })
    .expect("Failed to set signal handler");

    // Main monitoring loop
    let mut consecutive_failures = 0u32;
    let pet_duration = Duration::from_secs(pet_interval);

    while running.load(std::sync::atomic::Ordering::SeqCst) {
        let start = Instant::now();

        // Pet hardware watchdog
        if hw_watchdog.pet() {
            debug!("Hardware watchdog pet");
        }

        // Check socmond health
        if !check_socmond_health() {
            consecutive_failures += 1;
            warn!(
                "socmond check failed ({}/{})",
                consecutive_failures, args.max_failures
            );

            if consecutive_failures >= args.max_failures {
                // Try to restart socmond first
                info!("Attempting to restart socmond");
                if !restart_socmond() {
                    std::thread::sleep(Duration::from_secs(5));

                    // Check again after restart
                    if !check_socmond_health() {
                        trigger_reboot("socmond_failure");
                    } else {
                        info!("socmond restart successful");
                        consecutive_failures = 0;
                    }
                }
            }
        } else {
            if consecutive_failures > 0 {
                info!("socmond recovered");
            }
            consecutive_failures = 0;
        }

        // Check system health
        if !check_system_health() {
            trigger_reboot("system_health_failure");
        }

        // Sleep until next check
        let elapsed = start.elapsed();
        if elapsed < pet_duration {
            std::thread::sleep(pet_duration - elapsed);
        }
    }

    info!("socmond-watchdog shutting down");
    hw_watchdog.close_gracefully();
}

/// Check if socmond is running and healthy
fn check_socmond_health() -> bool {
    // Check systemd service status
    let output = Command::new("systemctl")
        .args(["is-active", "--quiet", "socmond"])
        .status();

    if let Ok(status) = output {
        if status.success() {
            return true;
        }
    }

    // Fallback: check if process is running
    let output = Command::new("pgrep")
        .args(["-x", "socmond"])
        .status();

    if let Ok(status) = output {
        return status.success();
    }

    false
}

/// Check system health (memory, disk, load)
fn check_system_health() -> bool {
    // Check available memory
    if let Ok(content) = fs::read_to_string("/proc/meminfo") {
        let mut total: u64 = 0;
        let mut available: u64 = 0;

        for line in content.lines() {
            if line.starts_with("MemTotal:") {
                total = parse_meminfo_value(line);
            } else if line.starts_with("MemAvailable:") {
                available = parse_meminfo_value(line);
            }
        }

        if total > 0 {
            let available_percent = (available * 100) / total;
            if available_percent < 5 {
                error!(
                    "CRITICAL: Available memory below 5% ({}%)",
                    available_percent
                );
                return false;
            }
        }
    }

    // Check available disk space on root
    if let Ok(content) = fs::read_to_string("/proc/mounts") {
        for line in content.lines() {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 2 && parts[1] == "/" {
                if let Some(usage) = get_disk_usage("/") {
                    let available_percent = 100 - usage;
                    if available_percent < 5 {
                        error!(
                            "CRITICAL: Available disk space below 5% ({}%)",
                            available_percent
                        );
                        return false;
                    }
                }
                break;
            }
        }
    }

    // Check load average (warning only)
    if let Ok(content) = fs::read_to_string("/proc/loadavg") {
        let parts: Vec<&str> = content.split_whitespace().collect();
        if !parts.is_empty() {
            if let Ok(load) = parts[0].parse::<f64>() {
                let cpu_count = num_cpus();
                if load > (cpu_count * 2) as f64 {
                    warn!("High load average: {:.2} (CPUs: {})", load, cpu_count);
                }
            }
        }
    }

    true
}

fn parse_meminfo_value(line: &str) -> u64 {
    line.split_whitespace()
        .nth(1)
        .and_then(|s| s.parse().ok())
        .unwrap_or(0)
}

fn get_disk_usage(mount_point: &str) -> Option<u64> {
    let output = Command::new("df")
        .args([mount_point])
        .output()
        .ok()?;

    let output_str = String::from_utf8_lossy(&output.stdout);
    for line in output_str.lines().skip(1) {
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 5 {
            return parts[4].trim_end_matches('%').parse().ok();
        }
    }

    None
}

fn num_cpus() -> usize {
    if let Ok(content) = fs::read_to_string("/proc/cpuinfo") {
        return content.matches("processor").count().max(1);
    }
    1
}

/// Restart socmond service
fn restart_socmond() -> bool {
    let output = Command::new("systemctl")
        .args(["restart", "socmond"])
        .status();

    if let Ok(status) = output {
        return status.success();
    }

    false
}

/// Trigger system reboot
fn trigger_reboot(reason: &str) {
    error!("Triggering reboot due to: {}", reason);

    // Store reboot reason
    let reason_file = "/var/lib/socmond/last_reboot_reason";
    if let Some(parent) = Path::new(reason_file).parent() {
        let _ = fs::create_dir_all(parent);
    }
    let _ = fs::write(reason_file, reason);

    // Sync filesystems
    let _ = Command::new("sync").output();

    // Give processes a moment
    std::thread::sleep(Duration::from_secs(2));

    // Reboot
    let _ = Command::new("reboot").args(["-f"]).output();

    // If reboot command fails, try systemctl
    std::thread::sleep(Duration::from_secs(5));
    let _ = Command::new("systemctl").args(["reboot", "-f"]).output();

    // Last resort: kernel sysrq
    std::thread::sleep(Duration::from_secs(5));
    let _ = fs::write("/proc/sysrq-trigger", "b");
}
