//! socmond - SoC Monitoring Library
//!
//! This library provides the core functionality for the socmond daemon
//! and can be used by other Rust applications to integrate with the
//! SoC Monitoring platform.

pub mod config;
pub mod coredump;
pub mod http;
pub mod ipc;
pub mod metrics;
pub mod ota;
pub mod reboot;
pub mod watchdog;

pub use config::Config;
pub use http::HttpClient;
pub use ipc::{IpcClient, IpcMessage, MetricSubmitter};
pub use metrics::{Metric, MetricsBatch, MetricsCollector};
pub use reboot::{RebootReason, RebootTracker};
pub use watchdog::WatchdogManager;

/// Library version
pub const VERSION: &str = env!("CARGO_PKG_VERSION");

/// Default configuration path
pub const DEFAULT_CONFIG_PATH: &str = "/etc/socmond/config.json";

/// Default socket path
pub const DEFAULT_SOCKET_PATH: &str = "/run/socmond/metrics.sock";

/// Result type alias for socmond operations
pub type Result<T> = std::result::Result<T, Error>;

/// Main error type for the library
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("Configuration error: {0}")]
    Config(#[from] config::ConfigError),

    #[error("HTTP error: {0}")]
    Http(#[from] http::HttpError),

    #[error("Metrics error: {0}")]
    Metrics(#[from] metrics::MetricsError),

    #[error("Coredump error: {0}")]
    Coredump(#[from] coredump::CoredumpError),

    #[error("OTA error: {0}")]
    Ota(#[from] ota::OtaError),

    #[error("Watchdog error: {0}")]
    Watchdog(#[from] watchdog::WatchdogError),

    #[error("IPC error: {0}")]
    Ipc(#[from] ipc::IpcError),

    #[error("IO error: {0}")]
    Io(#[from] std::io::Error),
}

/// Quick initialization helper for applications wanting to send metrics
///
/// # Example
///
/// ```no_run
/// use socmond::send_metric;
///
/// #[tokio::main]
/// async fn main() {
///     // Send a custom metric
///     send_metric("my_app.requests", 42.0, Some("count")).await.ok();
/// }
/// ```
pub async fn send_metric(name: &str, value: f64, unit: Option<&str>) -> Result<()> {
    let client = IpcClient::new(DEFAULT_SOCKET_PATH);
    client
        .send_metric(name, value, unit, std::collections::HashMap::new())
        .await
        .map_err(Error::from)
}

/// Send a custom event to socmond
pub async fn send_event(name: &str, data: serde_json::Value) -> Result<()> {
    let client = IpcClient::new(DEFAULT_SOCKET_PATH);
    client.send_event(name, data).await.map_err(Error::from)
}

/// Check if socmond is running
pub async fn is_daemon_running() -> bool {
    let client = IpcClient::new(DEFAULT_SOCKET_PATH);
    client.is_running().await
}
