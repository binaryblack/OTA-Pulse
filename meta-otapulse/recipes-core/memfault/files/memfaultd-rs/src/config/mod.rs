//! Configuration module for memfaultd
//!
//! Handles loading, parsing, and validation of the JSON configuration file.

use serde::{Deserialize, Serialize};
use std::path::Path;
use thiserror::Error;

#[derive(Error, Debug)]
pub enum ConfigError {
    #[error("Failed to read configuration file: {0}")]
    ReadError(#[from] std::io::Error),
    #[error("Failed to parse configuration: {0}")]
    ParseError(#[from] serde_json::Error),
    #[error("Configuration validation failed: {0}")]
    ValidationError(String),
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Config {
    pub server: ServerConfig,
    pub device: DeviceConfig,
    pub auth: AuthConfig,
    pub metrics: MetricsConfig,
    pub crash: CrashConfig,
    pub reboot: RebootConfig,
    pub ota: OtaConfig,
    pub logs: LogsConfig,
    pub system: SystemConfig,
    pub watchdog: WatchdogConfig,
    pub network: NetworkConfig,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ServerConfig {
    pub base_url: String,
    #[serde(default = "default_api_version")]
    pub api_version: String,
}

fn default_api_version() -> String {
    "v1".to_string()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceConfig {
    #[serde(default)]
    pub device_id: String,
    #[serde(default)]
    pub hardware_version: String,
    #[serde(default)]
    pub firmware_version: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AuthConfig {
    #[serde(default)]
    pub api_key: String,
    #[serde(default)]
    pub project_key: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MetricsConfig {
    #[serde(default = "default_true")]
    pub enabled: bool,
    #[serde(default = "default_upload_interval")]
    pub upload_interval_seconds: u64,
    #[serde(default = "default_collection_interval")]
    pub collection_interval_seconds: u64,
    #[serde(default = "default_max_buffer_size")]
    pub max_buffer_size_bytes: u64,
    #[serde(default = "default_retry_attempts")]
    pub retry_max_attempts: u32,
    #[serde(default = "default_metrics_socket")]
    pub metrics_socket_path: String,
}

fn default_true() -> bool {
    true
}

fn default_upload_interval() -> u64 {
    300
}

fn default_collection_interval() -> u64 {
    60
}

fn default_max_buffer_size() -> u64 {
    104857600 // 100MB
}

fn default_retry_attempts() -> u32 {
    5
}

fn default_metrics_socket() -> String {
    "/run/memfault/metrics.sock".to_string()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CrashConfig {
    #[serde(default = "default_true")]
    pub enabled: bool,
    #[serde(default = "default_true")]
    pub coredump_enabled: bool,
    #[serde(default = "default_coredump_max_size")]
    pub coredump_max_size_bytes: u64,
    #[serde(default = "default_compression")]
    pub coredump_compression: String,
    #[serde(default = "default_coredump_path")]
    pub coredump_storage_path: String,
    #[serde(default = "default_true")]
    pub upload_on_crash: bool,
    #[serde(default = "default_true")]
    pub capture_logs_before_crash: bool,
    #[serde(default = "default_log_lines")]
    pub log_lines_before_crash: u32,
}

fn default_coredump_max_size() -> u64 {
    52428800 // 50MB
}

fn default_compression() -> String {
    "gzip".to_string()
}

fn default_coredump_path() -> String {
    "/var/lib/memfault/coredumps".to_string()
}

fn default_log_lines() -> u32 {
    500
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RebootConfig {
    #[serde(default = "default_true")]
    pub enabled: bool,
    #[serde(default = "default_true")]
    pub track_reason: bool,
    #[serde(default = "default_reason_file")]
    pub reason_file_path: String,
}

fn default_reason_file() -> String {
    "/var/lib/memfault/last_reboot_reason".to_string()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OtaConfig {
    #[serde(default = "default_true")]
    pub enabled: bool,
    #[serde(default = "default_ota_check_interval")]
    pub check_interval_seconds: u64,
    #[serde(default = "default_ota_download_path")]
    pub download_path: String,
    #[serde(default = "default_true")]
    pub signature_verification: bool,
    #[serde(default = "default_signature_algorithm")]
    pub signature_algorithm: String,
    #[serde(default)]
    pub allow_downgrade: bool,
    #[serde(default)]
    pub auto_install: bool,
    #[serde(default = "default_true")]
    pub retry_on_failure: bool,
    #[serde(default = "default_max_download_retries")]
    pub max_download_retries: u32,
}

fn default_ota_check_interval() -> u64 {
    3600
}

fn default_ota_download_path() -> String {
    "/var/lib/memfault/ota".to_string()
}

fn default_signature_algorithm() -> String {
    "ecdsa-p384".to_string()
}

fn default_max_download_retries() -> u32 {
    3
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LogsConfig {
    #[serde(default = "default_true")]
    pub enabled: bool,
    #[serde(default = "default_true")]
    pub capture_kernel_logs: bool,
    #[serde(default = "default_true")]
    pub capture_journal_logs: bool,
    #[serde(default = "default_log_buffer")]
    pub max_log_buffer_bytes: u64,
    #[serde(default = "default_log_level")]
    pub log_level: String,
}

fn default_log_buffer() -> u64 {
    524288000 // 500MB
}

fn default_log_level() -> String {
    "info".to_string()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SystemConfig {
    #[serde(default = "default_true")]
    pub collect_cpu_metrics: bool,
    #[serde(default = "default_true")]
    pub collect_memory_metrics: bool,
    #[serde(default = "default_true")]
    pub collect_disk_metrics: bool,
    #[serde(default = "default_true")]
    pub collect_network_metrics: bool,
    #[serde(default = "default_true")]
    pub collect_temperature: bool,
    #[serde(default = "default_temperature_paths")]
    pub temperature_paths: Vec<String>,
}

fn default_temperature_paths() -> Vec<String> {
    vec![
        "/sys/class/thermal/thermal_zone0/temp".to_string(),
        "/sys/class/thermal/thermal_zone1/temp".to_string(),
    ]
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WatchdogConfig {
    #[serde(default = "default_true")]
    pub enabled: bool,
    #[serde(default = "default_watchdog_timeout")]
    pub timeout_seconds: u64,
    #[serde(default = "default_pet_interval")]
    pub pet_interval_seconds: u64,
}

fn default_watchdog_timeout() -> u64 {
    30
}

fn default_pet_interval() -> u64 {
    10
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NetworkConfig {
    #[serde(default = "default_connect_timeout")]
    pub connect_timeout_seconds: u64,
    #[serde(default = "default_request_timeout")]
    pub request_timeout_seconds: u64,
    #[serde(default = "default_true")]
    pub use_tls: bool,
    #[serde(default = "default_true")]
    pub verify_certificates: bool,
    #[serde(default = "default_ca_cert_path")]
    pub ca_cert_path: String,
}

fn default_connect_timeout() -> u64 {
    30
}

fn default_request_timeout() -> u64 {
    60
}

fn default_ca_cert_path() -> String {
    "/etc/ssl/certs/ca-certificates.crt".to_string()
}

impl Config {
    /// Load configuration from a JSON file
    pub fn load<P: AsRef<Path>>(path: P) -> Result<Self, ConfigError> {
        let content = std::fs::read_to_string(path)?;
        let config: Config = serde_json::from_str(&content)?;
        config.validate()?;
        Ok(config)
    }

    /// Validate the configuration
    pub fn validate(&self) -> Result<(), ConfigError> {
        if self.server.base_url.is_empty() {
            return Err(ConfigError::ValidationError(
                "server.base_url is required".to_string(),
            ));
        }

        if self.metrics.upload_interval_seconds < self.metrics.collection_interval_seconds {
            return Err(ConfigError::ValidationError(
                "upload_interval_seconds must be >= collection_interval_seconds".to_string(),
            ));
        }

        if self.watchdog.pet_interval_seconds >= self.watchdog.timeout_seconds {
            return Err(ConfigError::ValidationError(
                "watchdog.pet_interval_seconds must be < watchdog.timeout_seconds".to_string(),
            ));
        }

        Ok(())
    }

    /// Get the full API URL for a given endpoint
    pub fn api_url(&self, endpoint: &str) -> String {
        format!(
            "{}/api/{}/{}",
            self.server.base_url.trim_end_matches('/'),
            self.server.api_version,
            endpoint.trim_start_matches('/')
        )
    }
}

impl Default for Config {
    fn default() -> Self {
        Config {
            server: ServerConfig {
                base_url: "https://api.example.com".to_string(),
                api_version: default_api_version(),
            },
            device: DeviceConfig {
                device_id: String::new(),
                hardware_version: String::new(),
                firmware_version: String::new(),
            },
            auth: AuthConfig {
                api_key: String::new(),
                project_key: String::new(),
            },
            metrics: MetricsConfig {
                enabled: true,
                upload_interval_seconds: default_upload_interval(),
                collection_interval_seconds: default_collection_interval(),
                max_buffer_size_bytes: default_max_buffer_size(),
                retry_max_attempts: default_retry_attempts(),
                metrics_socket_path: default_metrics_socket(),
            },
            crash: CrashConfig {
                enabled: true,
                coredump_enabled: true,
                coredump_max_size_bytes: default_coredump_max_size(),
                coredump_compression: default_compression(),
                coredump_storage_path: default_coredump_path(),
                upload_on_crash: true,
                capture_logs_before_crash: true,
                log_lines_before_crash: default_log_lines(),
            },
            reboot: RebootConfig {
                enabled: true,
                track_reason: true,
                reason_file_path: default_reason_file(),
            },
            ota: OtaConfig {
                enabled: true,
                check_interval_seconds: default_ota_check_interval(),
                download_path: default_ota_download_path(),
                signature_verification: true,
                signature_algorithm: default_signature_algorithm(),
                allow_downgrade: false,
                auto_install: false,
                retry_on_failure: true,
                max_download_retries: default_max_download_retries(),
            },
            logs: LogsConfig {
                enabled: true,
                capture_kernel_logs: true,
                capture_journal_logs: true,
                max_log_buffer_bytes: default_log_buffer(),
                log_level: default_log_level(),
            },
            system: SystemConfig {
                collect_cpu_metrics: true,
                collect_memory_metrics: true,
                collect_disk_metrics: true,
                collect_network_metrics: true,
                collect_temperature: true,
                temperature_paths: default_temperature_paths(),
            },
            watchdog: WatchdogConfig {
                enabled: true,
                timeout_seconds: default_watchdog_timeout(),
                pet_interval_seconds: default_pet_interval(),
            },
            network: NetworkConfig {
                connect_timeout_seconds: default_connect_timeout(),
                request_timeout_seconds: default_request_timeout(),
                use_tls: true,
                verify_certificates: true,
                ca_cert_path: default_ca_cert_path(),
            },
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_default_config_is_valid() {
        let config = Config::default();
        assert!(config.validate().is_ok());
    }

    #[test]
    fn test_api_url_generation() {
        let config = Config::default();
        let url = config.api_url("devices/123/metrics");
        assert_eq!(url, "https://api.example.com/api/v1/devices/123/metrics");
    }
}
