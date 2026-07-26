//! HTTP client module for server communication
//!
//! Handles all HTTP communication with the SoC Monitoring backend.

use crate::config::{Config, NetworkConfig};
use crate::metrics::MetricsBatch;
use reqwest::{
    header::{HeaderMap, HeaderName, HeaderValue, CONTENT_TYPE, USER_AGENT},
    multipart::{Form, Part},
    Client, Response, StatusCode,
};
use serde::{Deserialize, Serialize};
use std::path::Path;
use std::time::Duration;
use thiserror::Error;
use tokio::fs;
use tracing::{debug, error, info, warn};

#[derive(Error, Debug)]
pub enum HttpError {
    #[error("Request failed: {0}")]
    RequestError(#[from] reqwest::Error),
    #[error("Server returned error: {status} - {message}")]
    ServerError { status: u16, message: String },
    #[error("Authentication failed")]
    AuthError,
    #[error("Rate limited, retry after {retry_after} seconds")]
    RateLimited { retry_after: u64 },
    #[error("Network error: {0}")]
    NetworkError(String),
    #[error("IO error: {0}")]
    IoError(#[from] std::io::Error),
}

/// OTA update check response (matches server API)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OtaCheckResponse {
    pub update_available: bool,
    #[serde(default)]
    pub deployment_id: String,
    #[serde(default)]
    pub version: String,
    #[serde(default)]
    pub version_code: Option<i32>,
    #[serde(default, alias = "file_size")]
    pub size_bytes: u64,
    #[serde(default)]
    pub checksum: String,
    #[serde(default)]
    pub checksum_type: String,
    #[serde(default)]
    pub download_url: String,
    #[serde(default)]
    pub release_notes: String,
    #[serde(default, alias = "is_mandatory")]
    pub mandatory: bool,
    // Legacy field for signature verification
    #[serde(default)]
    pub signature: String,
}

/// Heartbeat response (matches server API)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HeartbeatResponse {
    pub status: String,
    pub server_time: String,
    pub update_available: bool,
    #[serde(default)]
    pub update_id: Option<String>,
    #[serde(default)]
    pub to_version: Option<String>,
}

/// OTA check request
#[derive(Debug, Serialize)]
struct OtaCheckRequest {
    device_id: String,
    current_version: String,
    hardware_version: String,
}

/// Reboot report request
#[derive(Debug, Serialize)]
struct RebootReportRequest {
    device_id: String,
    reason: String,
    firmware_version: String,
    hardware_version: String,
    uptime_before_reboot: Option<f64>,
}

/// Device heartbeat request
#[derive(Debug, Serialize)]
struct HeartbeatRequest {
    device_id: String,
    firmware_version: String,
    hardware_version: String,
    uptime_seconds: f64,
    timestamp: String,
}

/// API response wrapper
#[derive(Debug, Deserialize)]
struct ApiResponse<T> {
    #[serde(default)]
    success: bool,
    #[serde(default)]
    message: String,
    #[serde(default)]
    data: Option<T>,
}

/// HTTP client for SoC Monitoring API
pub struct HttpClient {
    client: Client,
    base_url: String,
    api_version: String,
    api_key: String,
    device_id: String,
    firmware_version: String,
    hardware_version: String,
}

impl HttpClient {
    /// Create a new HTTP client
    pub fn new(config: &Config) -> Result<Self, HttpError> {
        let network = &config.network;

        let mut headers = HeaderMap::new();
        headers.insert(
            USER_AGENT,
            HeaderValue::from_static("socmond/1.0.0 (Rust)"),
        );

        let client_builder = Client::builder()
            .timeout(Duration::from_secs(network.request_timeout_seconds))
            .connect_timeout(Duration::from_secs(network.connect_timeout_seconds))
            .default_headers(headers)
            .pool_idle_timeout(Duration::from_secs(90))
            .pool_max_idle_per_host(2);

        let client_builder = if network.use_tls && network.verify_certificates {
            // Use native certificates
            client_builder
                .use_rustls_tls()
                .tls_built_in_root_certs(true)
        } else if network.use_tls {
            // TLS without certificate verification (not recommended for production)
            client_builder
                .use_rustls_tls()
                .danger_accept_invalid_certs(true)
        } else {
            client_builder
        };

        let client = client_builder.build()?;

        Ok(Self {
            client,
            base_url: config.server.base_url.trim_end_matches('/').to_string(),
            api_version: config.server.api_version.clone(),
            api_key: config.auth.api_key.clone(),
            device_id: config.device.device_id.clone(),
            firmware_version: config.device.firmware_version.clone(),
            hardware_version: config.device.hardware_version.clone(),
        })
    }

    /// Update device info (for dynamic values)
    pub fn update_device_info(&mut self, device_id: &str, firmware_version: &str, hardware_version: &str) {
        self.device_id = device_id.to_string();
        self.firmware_version = firmware_version.to_string();
        self.hardware_version = hardware_version.to_string();
    }

    /// Get the full URL for an endpoint
    fn url(&self, endpoint: &str) -> String {
        format!(
            "{}/api/{}/{}",
            self.base_url,
            self.api_version,
            endpoint.trim_start_matches('/')
        )
    }

    /// Get API key header name
    fn api_key_header_name() -> HeaderName {
        HeaderName::from_static("x-api-key")
    }

    /// Get device ID header name
    fn device_id_header_name() -> HeaderName {
        HeaderName::from_static("x-device-id")
    }

    /// Get API key header value
    fn api_key_header(&self) -> Result<HeaderValue, HttpError> {
        HeaderValue::from_str(&self.api_key)
            .map_err(|_| HttpError::AuthError)
    }

    /// Get device ID header value
    fn device_id_header(&self) -> Result<HeaderValue, HttpError> {
        HeaderValue::from_str(&self.device_id)
            .map_err(|_| HttpError::AuthError)
    }

    /// Handle response errors
    async fn handle_response(&self, response: Response) -> Result<Response, HttpError> {
        let status = response.status();

        match status {
            StatusCode::OK | StatusCode::CREATED | StatusCode::ACCEPTED | StatusCode::NO_CONTENT => {
                Ok(response)
            }
            StatusCode::UNAUTHORIZED | StatusCode::FORBIDDEN => {
                Err(HttpError::AuthError)
            }
            StatusCode::TOO_MANY_REQUESTS => {
                let retry_after = response
                    .headers()
                    .get("Retry-After")
                    .and_then(|v| v.to_str().ok())
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(60);
                Err(HttpError::RateLimited { retry_after })
            }
            _ => {
                let message = response.text().await.unwrap_or_else(|_| "Unknown error".to_string());
                Err(HttpError::ServerError {
                    status: status.as_u16(),
                    message,
                })
            }
        }
    }

    /// Upload metrics batch
    pub async fn upload_metrics(&self, batch: &MetricsBatch) -> Result<(), HttpError> {
        if self.api_key.is_empty() {
            debug!("No API key configured, skipping metrics upload");
            return Ok(());
        }

        let url = self.url("metrics");

        debug!("Uploading {} metrics to {}", batch.metrics.len(), url);

        let response = self
            .client
            .post(&url)
            .header(Self::api_key_header_name(), self.api_key_header()?)
            .header(Self::device_id_header_name(), self.device_id_header()?)
            .header(CONTENT_TYPE, "application/json")
            .json(batch)
            .send()
            .await?;

        self.handle_response(response).await?;
        info!("Successfully uploaded {} metrics", batch.metrics.len());

        Ok(())
    }

    /// Upload coredump file
    pub async fn upload_coredump<P: AsRef<Path>>(
        &self,
        coredump_path: P,
        process_name: Option<&str>,
        signal: Option<i32>,
        executable: Option<&str>,
    ) -> Result<(), HttpError> {
        if self.api_key.is_empty() {
            debug!("No API key configured, skipping coredump upload");
            return Ok(());
        }

        let coredump_path = coredump_path.as_ref();

        // Read coredump file
        let coredump_data = fs::read(coredump_path).await?;

        let coredump_filename = coredump_path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("coredump.gz")
            .to_string();

        let form = Form::new()
            .part(
                "file",
                Part::bytes(coredump_data)
                    .file_name(coredump_filename)
                    .mime_str("application/gzip")?,
            );

        let url = self.url("coredump");

        info!("Uploading coredump to {}", url);

        let mut request = self
            .client
            .post(&url)
            .header(Self::api_key_header_name(), self.api_key_header()?)
            .header(Self::device_id_header_name(), self.device_id_header()?);

        // Add optional headers
        if let Some(name) = process_name {
            request = request.header("X-Process-Name", name);
        }
        if let Some(sig) = signal {
            request = request.header("X-Signal", sig.to_string());
        }
        if let Some(exe) = executable {
            request = request.header("X-Executable", exe);
        }

        let response = request
            .multipart(form)
            .timeout(Duration::from_secs(300)) // 5 minute timeout for large files
            .send()
            .await?;

        self.handle_response(response).await?;
        info!("Successfully uploaded coredump: {:?}", coredump_path);

        Ok(())
    }

    /// Check for OTA updates
    pub async fn check_ota_update(&self) -> Result<OtaCheckResponse, HttpError> {
        if self.api_key.is_empty() {
            debug!("No API key configured, skipping OTA check");
            return Ok(OtaCheckResponse {
                update_available: false,
                deployment_id: String::new(),
                version: String::new(),
                version_code: None,
                download_url: String::new(),
                size_bytes: 0,
                checksum: String::new(),
                checksum_type: String::new(),
                signature: String::new(),
                release_notes: String::new(),
                mandatory: false,
            });
        }

        let url = self.url("ota/check");

        debug!("Checking for OTA updates at {}", url);

        let response = self
            .client
            .get(&url)
            .header(Self::api_key_header_name(), self.api_key_header()?)
            .header(Self::device_id_header_name(), self.device_id_header()?)
            .send()
            .await?;

        let response = self.handle_response(response).await?;
        let ota_response: OtaCheckResponse = response.json().await?;

        if ota_response.update_available {
            info!(
                "OTA update available: {} -> {}",
                self.firmware_version, ota_response.version
            );
        } else {
            debug!("No OTA update available");
        }

        Ok(ota_response)
    }

    /// Download OTA firmware
    pub async fn download_ota<P: AsRef<Path>>(
        &self,
        download_url: &str,
        destination: P,
    ) -> Result<u64, HttpError> {
        // If it's a relative URL, prepend base URL
        let full_url = if download_url.starts_with("http") {
            download_url.to_string()
        } else {
            format!("{}/api/{}/{}", self.base_url, self.api_version, download_url.trim_start_matches('/'))
        };

        info!("Downloading OTA from {}", full_url);

        let response = self
            .client
            .get(&full_url)
            .header(Self::api_key_header_name(), self.api_key_header()?)
            .header(Self::device_id_header_name(), self.device_id_header()?)
            .timeout(Duration::from_secs(3600)) // 1 hour timeout for large downloads
            .send()
            .await?;

        let response = self.handle_response(response).await?;

        let content_length = response
            .content_length()
            .unwrap_or(0);

        let bytes = response.bytes().await?;
        fs::write(destination.as_ref(), &bytes).await?;

        info!("Downloaded {} bytes to {:?}", bytes.len(), destination.as_ref());

        Ok(content_length)
    }

    /// Report OTA status update
    pub async fn report_ota_status(&self, deployment_id: &str, status: &str, error: Option<&str>) -> Result<(), HttpError> {
        if self.api_key.is_empty() {
            debug!("No API key configured, skipping OTA status report");
            return Ok(());
        }

        let url = self.url(&format!("ota/status/{}", deployment_id));

        let body = serde_json::json!({
            "status": status,
            "error": error
        });

        debug!("Reporting OTA status '{}' to {}", status, url);

        let response = self
            .client
            .post(&url)
            .header(Self::api_key_header_name(), self.api_key_header()?)
            .header(Self::device_id_header_name(), self.device_id_header()?)
            .header(CONTENT_TYPE, "application/json")
            .json(&body)
            .send()
            .await?;

        self.handle_response(response).await?;
        info!("Successfully reported OTA status: {}", status);

        Ok(())
    }

    /// Send device heartbeat
    pub async fn send_heartbeat(&self, uptime_seconds: f64) -> Result<HeartbeatResponse, HttpError> {
        if self.api_key.is_empty() {
            return Ok(HeartbeatResponse {
                status: "ok".to_string(),
                server_time: chrono::Utc::now().to_rfc3339(),
                update_available: false,
                update_id: None,
                to_version: None,
            });
        }

        let url = self.url("heartbeat");

        let request = HeartbeatRequest {
            device_id: self.device_id.clone(),
            firmware_version: self.firmware_version.clone(),
            hardware_version: self.hardware_version.clone(),
            uptime_seconds,
            timestamp: chrono::Utc::now().to_rfc3339(),
        };

        let response = self
            .client
            .post(&url)
            .header(Self::api_key_header_name(), self.api_key_header()?)
            .header(Self::device_id_header_name(), self.device_id_header()?)
            .header(CONTENT_TYPE, "application/json")
            .json(&request)
            .send()
            .await?;

        let response = self.handle_response(response).await?;
        let heartbeat_response: HeartbeatResponse = response.json().await?;
        
        if heartbeat_response.update_available {
            info!(
                "Update available in heartbeat: {} -> {}",
                self.firmware_version,
                heartbeat_response.to_version.as_deref().unwrap_or("unknown")
            );
        }
        
        debug!("Sent heartbeat");

        Ok(heartbeat_response)
    }

    /// Register device with server (self-registration)
    pub async fn register_device(&self, serial_number: &str, metadata: Option<serde_json::Value>) -> Result<String, HttpError> {
        if self.api_key.is_empty() {
            return Err(HttpError::AuthError);
        }

        let url = self.url("register");

        let mut body = serde_json::json!({
            "serial_number": serial_number,
            "name": format!("Device-{}", &serial_number[serial_number.len().saturating_sub(6)..]),
            "hardware_version": self.hardware_version,
            "firmware_version": self.firmware_version,
        });

        if let Some(meta) = metadata {
            body["metadata"] = meta;
        }

        info!("Registering device with serial {}", serial_number);

        let response = self
            .client
            .post(&url)
            .header(Self::api_key_header_name(), self.api_key_header()?)
            .header(CONTENT_TYPE, "application/json")
            .json(&body)
            .send()
            .await?;

        let response = self.handle_response(response).await?;
        let result: serde_json::Value = response.json().await?;

        let device_id = result["device_id"]
            .as_str()
            .ok_or_else(|| HttpError::ServerError {
                status: 500,
                message: "No device_id in response".to_string(),
            })?
            .to_string();

        info!("Device registered with ID: {}", device_id);

        Ok(device_id)
    }

    /// Check server connectivity
    pub async fn check_connectivity(&self) -> bool {
        let url = format!("{}/health", self.base_url);

        match self
            .client
            .get(&url)
            .timeout(Duration::from_secs(10))
            .send()
            .await
        {
            Ok(response) => response.status().is_success(),
            Err(e) => {
                debug!("Connectivity check failed: {}", e);
                false
            }
        }
    }
}

/// Retry wrapper for HTTP operations with exponential backoff
pub struct RetryPolicy {
    max_attempts: u32,
    base_delay_ms: u64,
    max_delay_ms: u64,
}

impl RetryPolicy {
    pub fn new(max_attempts: u32) -> Self {
        Self {
            max_attempts,
            base_delay_ms: 1000,
            max_delay_ms: 60000,
        }
    }

    pub async fn execute<F, Fut, T>(&self, operation: F) -> Result<T, HttpError>
    where
        F: Fn() -> Fut,
        Fut: std::future::Future<Output = Result<T, HttpError>>,
    {
        let mut attempts = 0;
        let mut delay = self.base_delay_ms;

        loop {
            attempts += 1;

            match operation().await {
                Ok(result) => return Ok(result),
                Err(e) => {
                    if attempts >= self.max_attempts {
                        return Err(e);
                    }

                    // Check if error is retryable
                    let should_retry = matches!(
                        &e,
                        HttpError::NetworkError(_)
                            | HttpError::RequestError(_)
                            | HttpError::RateLimited { .. }
                            | HttpError::ServerError { status, .. } if *status >= 500
                    );

                    if !should_retry {
                        return Err(e);
                    }

                    // Handle rate limiting
                    if let HttpError::RateLimited { retry_after } = &e {
                        delay = *retry_after * 1000;
                    }

                    warn!(
                        "Request failed (attempt {}/{}), retrying in {}ms: {}",
                        attempts, self.max_attempts, delay, e
                    );

                    tokio::time::sleep(Duration::from_millis(delay)).await;

                    // Exponential backoff
                    delay = (delay * 2).min(self.max_delay_ms);
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_url_construction() {
        let config = Config::default();
        let client = HttpClient::new(&config).unwrap();

        let url = client.url("devices/123/metrics");
        assert_eq!(url, "https://api.example.com/api/v1/devices/123/metrics");
    }
}
