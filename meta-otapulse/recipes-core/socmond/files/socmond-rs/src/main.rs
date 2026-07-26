//! socmond - SoC Monitoring Daemon
//!
//! A comprehensive monitoring daemon for embedded Linux devices that provides:
//! - System metrics collection (CPU, memory, disk, network, temperature)
//! - Coredump capture and upload
//! - OTA update management
//! - Watchdog integration
//! - Reboot reason tracking

mod config;
mod coredump;
mod http;
mod ipc;
mod metrics;
mod ota;
mod reboot;
mod watchdog;

use crate::config::Config;
use crate::coredump::{CoredumpManager, CoredumpUploader};
use crate::http::{HttpClient, RetryPolicy};
use crate::ipc::{IpcMessage, IpcServer};
use crate::metrics::{MetricsBatch, MetricsBuffer, MetricsCollector};
use crate::ota::OtaManager;
use crate::reboot::{RebootHistory, RebootHistoryEntry, RebootReason, RebootTracker};
use crate::watchdog::{systemd, WatchdogManager};

use anyhow::{Context, Result};
use clap::Parser;
use std::fs;
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;
use tokio::signal::unix::{signal, SignalKind};
use tokio::sync::{mpsc, RwLock};
use tracing::{debug, error, info, warn, Level};
use tracing_subscriber::fmt::format::FmtSpan;
use tracing_subscriber::EnvFilter;

/// SoC Monitoring Daemon
#[derive(Parser, Debug)]
#[command(name = "socmond")]
#[command(author = "SoC Monitoring Team")]
#[command(version = env!("CARGO_PKG_VERSION"))]
#[command(about = "Embedded systems monitoring daemon for SoC Monitoring platform")]
struct Args {
    /// Path to configuration file
    #[arg(short, long, default_value = "/etc/socmond/config.json")]
    config: String,

    /// Log level (trace, debug, info, warn, error)
    #[arg(short, long, default_value = "info")]
    log_level: String,

    /// Run in foreground (don't daemonize)
    #[arg(short, long)]
    foreground: bool,

    /// Print configuration and exit
    #[arg(long)]
    print_config: bool,

    /// Validate configuration and exit
    #[arg(long)]
    validate: bool,
}

/// Daemon state
struct DaemonState {
    config: Config,
    device_id: String,
    firmware_version: String,
    hardware_version: String,
    metrics_buffer: MetricsBuffer,
    connected: RwLock<bool>,
}

impl DaemonState {
    fn new(config: Config) -> Self {
        let device_id = get_device_id(&config);
        let firmware_version = get_firmware_version();
        let hardware_version = get_hardware_version();

        Self {
            metrics_buffer: MetricsBuffer::new(config.metrics.max_buffer_size_bytes),
            config,
            device_id,
            firmware_version,
            hardware_version,
            connected: RwLock::new(false),
        }
    }
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = Args::parse();

    // Initialize logging
    init_logging(&args.log_level)?;

    info!("socmond {} starting", env!("CARGO_PKG_VERSION"));

    // Load configuration
    let config = Config::load(&args.config)
        .with_context(|| format!("Failed to load config from {}", args.config))?;

    if args.print_config {
        println!("{}", serde_json::to_string_pretty(&config)?);
        return Ok(());
    }

    if args.validate {
        println!("Configuration is valid");
        return Ok(());
    }

    // Create daemon state
    let state = Arc::new(DaemonState::new(config.clone()));

    info!(
        "Device ID: {}, Firmware: {}, Hardware: {}",
        state.device_id, state.firmware_version, state.hardware_version
    );

    // Initialize components
    let mut http_client = HttpClient::new(&config)
        .context("Failed to create HTTP client")?;
    http_client.update_device_info(&state.device_id, &state.firmware_version, &state.hardware_version);

    let metrics_collector = MetricsCollector::new(config.system.temperature_paths.clone());
    let coredump_manager = CoredumpManager::new(&config.crash)
        .context("Failed to create coredump manager")?;
    let coredump_uploader = CoredumpUploader::new(
        CoredumpManager::new(&config.crash)?,
        HttpClient::new(&config)?,
        config.metrics.retry_max_attempts,
    );
    let ota_manager = OtaManager::new(&config.ota, &state.firmware_version)
        .context("Failed to create OTA manager")?;
    let reboot_tracker = RebootTracker::new(
        &config.reboot.reason_file_path,
        config.reboot.enabled,
    );
    let reboot_history = RebootHistory::new(
        "/var/lib/socmond/reboot_history.json",
        100,
    );

    // Initialize watchdog
    let mut watchdog_manager = WatchdogManager::new(&config.watchdog)
        .context("Failed to create watchdog manager")?;

    // Report reboot reason
    if config.reboot.enabled && config.reboot.track_reason {
        report_reboot_reason(&http_client, &reboot_tracker, &reboot_history, &state).await;
    }

    // Create IPC channel
    let (ipc_tx, mut ipc_rx) = mpsc::channel::<IpcMessage>(100);

    // Start IPC server
    let ipc_socket = config.metrics.metrics_socket_path.clone();
    let ipc_server = IpcServer::new(&ipc_socket, ipc_tx);
    tokio::spawn(async move {
        if let Err(e) = ipc_server.run().await {
            error!("IPC server error: {}", e);
        }
    });

    // Notify systemd we're ready
    if systemd::is_enabled() {
        systemd::notify_ready();
        info!("Notified systemd: ready");
    }

    // Set up signal handlers
    let mut sigterm = signal(SignalKind::terminate())?;
    let mut sigint = signal(SignalKind::interrupt())?;
    let mut sighup = signal(SignalKind::hangup())?;

    // Create intervals for periodic tasks
    let mut collection_interval = tokio::time::interval(Duration::from_secs(
        config.metrics.collection_interval_seconds,
    ));
    let mut upload_interval = tokio::time::interval(Duration::from_secs(
        config.metrics.upload_interval_seconds,
    ));
    let mut ota_interval = tokio::time::interval(Duration::from_secs(
        config.ota.check_interval_seconds,
    ));
    let mut watchdog_interval = tokio::time::interval(Duration::from_secs(
        config.watchdog.pet_interval_seconds,
    ));
    let mut heartbeat_interval = tokio::time::interval(Duration::from_secs(60));

    info!("Entering main loop");

    loop {
        tokio::select! {
            // Handle termination signals
            _ = sigterm.recv() => {
                info!("Received SIGTERM, shutting down");
                break;
            }
            _ = sigint.recv() => {
                info!("Received SIGINT, shutting down");
                break;
            }
            _ = sighup.recv() => {
                info!("Received SIGHUP, reloading configuration");
                // Could reload config here
            }

            // Pet watchdog
            _ = watchdog_interval.tick() => {
                if let Err(e) = watchdog_manager.pet() {
                    warn!("Failed to pet watchdog: {}", e);
                }
                if systemd::is_enabled() {
                    systemd::notify_watchdog();
                }
            }

            // Collect metrics
            _ = collection_interval.tick() => {
                if config.metrics.enabled {
                    let metrics = metrics_collector.collect_all(&config.system).await;
                    debug!("Collected {} metrics", metrics.len());
                    state.metrics_buffer.add(metrics).await;
                }
            }

            // Upload metrics and coredumps
            _ = upload_interval.tick() => {
                // Upload metrics
                if config.metrics.enabled && !state.metrics_buffer.is_empty().await {
                    let metrics = state.metrics_buffer.drain().await;
                    let batch = MetricsBatch { metrics };

                    let retry_policy = RetryPolicy::new(config.metrics.retry_max_attempts);
                    match retry_policy.execute(|| async {
                        http_client.upload_metrics(&batch).await
                    }).await {
                        Ok(()) => {
                            *state.connected.write().await = true;
                            debug!("Metrics uploaded successfully");
                        }
                        Err(e) => {
                            warn!("Failed to upload metrics: {}", e);
                            *state.connected.write().await = false;
                            // Re-add metrics to buffer for retry
                            state.metrics_buffer.add(batch.metrics).await;
                        }
                    }
                }

                // Upload pending coredumps
                if config.crash.enabled {
                    match coredump_uploader.upload_pending().await {
                        Ok(count) if count > 0 => {
                            info!("Uploaded {} coredumps", count);
                        }
                        Err(e) => {
                            warn!("Failed to upload coredumps: {}", e);
                        }
                        _ => {}
                    }
                }
            }

            // Check for OTA updates
            _ = ota_interval.tick() => {
                if config.ota.enabled {
                    match ota_manager.check_for_update(&http_client).await {
                        Ok(Some(update)) => {
                            info!("OTA update available: {}", update.version);
                            // Handle auto-install if enabled
                            if config.ota.auto_install {
                                info!("Auto-install enabled, processing update");
                                // Download and install would happen here
                            }
                        }
                        Ok(None) => {
                            debug!("No OTA update available");
                        }
                        Err(e) => {
                            warn!("OTA check failed: {}", e);
                        }
                    }
                }
            }

            // Send heartbeat
            _ = heartbeat_interval.tick() => {
                if let Some(uptime) = RebootTracker::get_current_uptime() {
                    match http_client.send_heartbeat(uptime).await {
                        Ok(heartbeat_response) => {
                            if heartbeat_response.update_available {
                                info!(
                                    "OTA update available from heartbeat: {}",
                                    heartbeat_response.to_version.as_deref().unwrap_or("unknown")
                                );
                                // Trigger OTA check when update is available
                                tokio::spawn({
                                    let http_client = http_client.clone();
                                    let state = state.clone();
                                    async move {
                                        if let Err(e) = ota::check_and_perform_ota(&http_client, &state).await {
                                            error!("OTA update from heartbeat failed: {}", e);
                                        }
                                    }
                                });
                            }
                        }
                        Err(e) => {
                            debug!("Heartbeat failed: {}", e);
                        }
                    }
                }
            }

            // Handle IPC messages
            Some(message) = ipc_rx.recv() => {
                handle_ipc_message(message, &state, &http_client).await;
            }
        }
    }

    // Shutdown
    info!("Shutting down");

    if systemd::is_enabled() {
        systemd::notify_stopping();
    }

    watchdog_manager.shutdown()?;

    // Clean up socket
    let _ = fs::remove_file(&ipc_socket);

    info!("socmond stopped");

    Ok(())
}

fn init_logging(level: &str) -> Result<()> {
    let level = match level.to_lowercase().as_str() {
        "trace" => Level::TRACE,
        "debug" => Level::DEBUG,
        "info" => Level::INFO,
        "warn" => Level::WARN,
        "error" => Level::ERROR,
        _ => Level::INFO,
    };

    let filter = EnvFilter::from_default_env()
        .add_directive(level.into());

    tracing_subscriber::fmt()
        .with_env_filter(filter)
        .with_target(false)
        .with_thread_ids(false)
        .with_file(false)
        .with_line_number(false)
        .init();

    Ok(())
}

fn get_device_id(config: &Config) -> String {
    // Use configured device ID if set
    if !config.device.device_id.is_empty() {
        return config.device.device_id.clone();
    }

    // Try machine-id
    if let Ok(id) = fs::read_to_string("/etc/machine-id") {
        return id.trim().to_string();
    }

    // Try to generate from MAC address
    if let Ok(mac) = fs::read_to_string("/sys/class/net/eth0/address") {
        return mac.trim().replace(':', "");
    }

    // Try wlan0
    if let Ok(mac) = fs::read_to_string("/sys/class/net/wlan0/address") {
        return mac.trim().replace(':', "");
    }

    // Generate UUID as fallback
    uuid::Uuid::new_v4().to_string()
}

fn get_firmware_version() -> String {
    // Try os-release
    if let Ok(content) = fs::read_to_string("/etc/os-release") {
        for line in content.lines() {
            if line.starts_with("VERSION=") {
                return line
                    .trim_start_matches("VERSION=")
                    .trim_matches('"')
                    .to_string();
            }
        }
        for line in content.lines() {
            if line.starts_with("VERSION_ID=") {
                return line
                    .trim_start_matches("VERSION_ID=")
                    .trim_matches('"')
                    .to_string();
            }
        }
    }

    // Try /etc/version
    if let Ok(version) = fs::read_to_string("/etc/version") {
        return version.trim().to_string();
    }

    "unknown".to_string()
}

fn get_hardware_version() -> String {
    // Try device tree model
    if let Ok(model) = fs::read_to_string("/proc/device-tree/model") {
        return model.trim_end_matches('\0').to_string();
    }

    // Try DMI product name
    if let Ok(product) = fs::read_to_string("/sys/class/dmi/id/product_name") {
        return product.trim().to_string();
    }

    // Try /proc/cpuinfo
    if let Ok(cpuinfo) = fs::read_to_string("/proc/cpuinfo") {
        for line in cpuinfo.lines() {
            if line.starts_with("Hardware") || line.starts_with("Model") {
                if let Some(value) = line.split(':').nth(1) {
                    return value.trim().to_string();
                }
            }
        }
    }

    "generic".to_string()
}

async fn report_reboot_reason(
    http_client: &HttpClient,
    tracker: &RebootTracker,
    history: &RebootHistory,
    state: &DaemonState,
) {
    let reason = tracker.get_last_reason();
    let uptime_before = tracker.get_previous_uptime();

    info!("Last reboot reason: {}", reason);

    // Record in history
    let entry = RebootHistoryEntry {
        timestamp: chrono::Utc::now().to_rfc3339(),
        reason: reason.to_string(),
        uptime_before,
        firmware_version: state.firmware_version.clone(),
    };
    let _ = history.add_entry(entry);

    // Report to server
    if let Err(e) = http_client.report_reboot(&reason.to_string(), uptime_before).await {
        warn!("Failed to report reboot reason: {}", e);
    }

    // Clear stored reason
    let _ = tracker.clear_reason();
}

async fn handle_ipc_message(message: IpcMessage, state: &DaemonState, http_client: &HttpClient) {
    match message {
        IpcMessage::Metric { name, value, unit, tags } => {
            debug!("Received custom metric: {} = {}", name, value);
            let metric = crate::metrics::Metric {
                name,
                value,
                timestamp: chrono::Utc::now(),
                unit,
                tags,
            };
            state.metrics_buffer.add(vec![metric]).await;
        }
        IpcMessage::Event { name, data } => {
            info!("Received event: {} - {:?}", name, data);
            // Could send events to server
        }
        IpcMessage::Coredump { file, meta } => {
            info!("Received coredump notification: {}", file);
            // Coredump will be picked up in next upload cycle
        }
        IpcMessage::RebootRequest { reason, delay_seconds } => {
            info!("Received reboot request: {} (delay: {:?}s)", reason, delay_seconds);
            let tracker = RebootTracker::new(
                &state.config.reboot.reason_file_path,
                state.config.reboot.enabled,
            );
            let _ = tracker.store_reason(RebootReason::Custom(reason));
            let _ = tracker.store_uptime(RebootTracker::get_current_uptime().unwrap_or(0.0));

            if let Some(delay) = delay_seconds {
                tokio::spawn(async move {
                    tokio::time::sleep(Duration::from_secs(delay)).await;
                    let _ = std::process::Command::new("reboot").output();
                });
            }
        }
        IpcMessage::Heartbeat { app_name, version } => {
            debug!("Heartbeat from app: {} ({:?})", app_name, version);
        }
        IpcMessage::Log { level, message, source } => {
            // Forward log messages
            match level.as_str() {
                "error" => error!(source = ?source, "{}", message),
                "warn" => warn!(source = ?source, "{}", message),
                "info" => info!(source = ?source, "{}", message),
                "debug" => debug!(source = ?source, "{}", message),
                _ => debug!(source = ?source, "{}", message),
            }
        }
        IpcMessage::Shutdown => {
            info!("Received shutdown request via IPC");
            std::process::exit(0);
        }
        _ => {
            debug!("Unhandled IPC message type");
        }
    }
}
