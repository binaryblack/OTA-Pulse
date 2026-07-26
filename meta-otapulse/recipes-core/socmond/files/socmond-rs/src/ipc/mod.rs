//! IPC (Inter-Process Communication) module
//!
//! Provides Unix socket server for receiving events from other processes
//! (e.g., coredump handler notifications, custom metrics from applications).

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};
use thiserror::Error;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::{UnixListener, UnixStream};
use tokio::sync::mpsc;
use tracing::{debug, error, info, warn};

#[derive(Error, Debug)]
pub enum IpcError {
    #[error("IO error: {0}")]
    IoError(#[from] std::io::Error),
    #[error("JSON error: {0}")]
    JsonError(#[from] serde_json::Error),
    #[error("Channel error")]
    ChannelError,
}

/// IPC message types
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum IpcMessage {
    /// Coredump notification from core handler
    Coredump {
        file: String,
        meta: String,
    },
    /// Custom metric from application
    Metric {
        name: String,
        value: f64,
        #[serde(default)]
        unit: Option<String>,
        #[serde(default)]
        tags: std::collections::HashMap<String, String>,
    },
    /// Custom event from application
    Event {
        name: String,
        #[serde(default)]
        data: serde_json::Value,
    },
    /// Log message
    Log {
        level: String,
        message: String,
        #[serde(default)]
        source: Option<String>,
    },
    /// Heartbeat from application
    Heartbeat {
        app_name: String,
        #[serde(default)]
        version: Option<String>,
    },
    /// Reboot request
    RebootRequest {
        reason: String,
        #[serde(default)]
        delay_seconds: Option<u64>,
    },
    /// Status query
    StatusQuery,
    /// Status response
    StatusResponse {
        version: String,
        uptime: f64,
        metrics_count: usize,
        coredumps_pending: usize,
        connected: bool,
    },
    /// Shutdown notification
    Shutdown,
    /// Generic JSON message
    Json(serde_json::Value),
}

/// IPC response
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IpcResponse {
    pub success: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<serde_json::Value>,
}

impl IpcResponse {
    pub fn ok() -> Self {
        Self {
            success: true,
            message: None,
            data: None,
        }
    }

    pub fn ok_with_data(data: serde_json::Value) -> Self {
        Self {
            success: true,
            message: None,
            data: Some(data),
        }
    }

    pub fn error(message: &str) -> Self {
        Self {
            success: false,
            message: Some(message.to_string()),
            data: None,
        }
    }
}

/// IPC server for receiving messages
pub struct IpcServer {
    socket_path: PathBuf,
    message_sender: mpsc::Sender<IpcMessage>,
}

impl IpcServer {
    pub fn new(socket_path: &str, message_sender: mpsc::Sender<IpcMessage>) -> Self {
        Self {
            socket_path: PathBuf::from(socket_path),
            message_sender,
        }
    }

    /// Start the IPC server
    pub async fn run(&self) -> Result<(), IpcError> {
        // Remove existing socket file
        if self.socket_path.exists() {
            fs::remove_file(&self.socket_path)?;
        }

        // Create parent directory if needed
        if let Some(parent) = self.socket_path.parent() {
            fs::create_dir_all(parent)?;
        }

        let listener = UnixListener::bind(&self.socket_path)?;

        // Set socket permissions (world-writable so any process can send)
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let permissions = std::fs::Permissions::from_mode(0o666);
            fs::set_permissions(&self.socket_path, permissions)?;
        }

        info!("IPC server listening on {:?}", self.socket_path);

        loop {
            match listener.accept().await {
                Ok((stream, _addr)) => {
                    let sender = self.message_sender.clone();
                    tokio::spawn(async move {
                        if let Err(e) = handle_connection(stream, sender).await {
                            debug!("Connection error: {}", e);
                        }
                    });
                }
                Err(e) => {
                    error!("Failed to accept connection: {}", e);
                }
            }
        }
    }

    /// Clean up socket file
    pub fn cleanup(&self) {
        if self.socket_path.exists() {
            let _ = fs::remove_file(&self.socket_path);
        }
    }
}

async fn handle_connection(
    stream: UnixStream,
    sender: mpsc::Sender<IpcMessage>,
) -> Result<(), IpcError> {
    let (reader, mut writer) = stream.into_split();
    let mut reader = BufReader::new(reader);
    let mut line = String::new();

    // Read one line (one JSON message)
    let bytes_read = reader.read_line(&mut line).await?;
    if bytes_read == 0 {
        return Ok(()); // Connection closed
    }

    debug!("Received IPC message: {}", line.trim());

    // Parse the message
    let response = match serde_json::from_str::<IpcMessage>(&line) {
        Ok(message) => {
            // Handle status query locally
            if matches!(message, IpcMessage::StatusQuery) {
                IpcResponse::ok_with_data(serde_json::json!({
                    "version": env!("CARGO_PKG_VERSION"),
                    "status": "running"
                }))
            } else {
                // Send to main loop for processing
                match sender.send(message).await {
                    Ok(()) => IpcResponse::ok(),
                    Err(_) => IpcResponse::error("Failed to process message"),
                }
            }
        }
        Err(e) => {
            warn!("Failed to parse IPC message: {}", e);
            IpcResponse::error(&format!("Invalid message format: {}", e))
        }
    };

    // Send response
    let response_json = serde_json::to_string(&response)?;
    writer.write_all(response_json.as_bytes()).await?;
    writer.write_all(b"\n").await?;
    writer.flush().await?;

    Ok(())
}

/// IPC client for sending messages to socmond
pub struct IpcClient {
    socket_path: PathBuf,
}

impl IpcClient {
    pub fn new(socket_path: &str) -> Self {
        Self {
            socket_path: PathBuf::from(socket_path),
        }
    }

    /// Send a message and get response
    pub async fn send(&self, message: &IpcMessage) -> Result<IpcResponse, IpcError> {
        let stream = UnixStream::connect(&self.socket_path).await?;
        let (reader, mut writer) = stream.into_split();

        // Send message
        let message_json = serde_json::to_string(message)?;
        writer.write_all(message_json.as_bytes()).await?;
        writer.write_all(b"\n").await?;
        writer.flush().await?;

        // Read response
        let mut reader = BufReader::new(reader);
        let mut response_line = String::new();
        reader.read_line(&mut response_line).await?;

        let response: IpcResponse = serde_json::from_str(&response_line)?;
        Ok(response)
    }

    /// Send a custom metric
    pub async fn send_metric(
        &self,
        name: &str,
        value: f64,
        unit: Option<&str>,
        tags: std::collections::HashMap<String, String>,
    ) -> Result<(), IpcError> {
        let message = IpcMessage::Metric {
            name: name.to_string(),
            value,
            unit: unit.map(|s| s.to_string()),
            tags,
        };

        let response = self.send(&message).await?;
        if response.success {
            Ok(())
        } else {
            Err(IpcError::IoError(std::io::Error::new(
                std::io::ErrorKind::Other,
                response.message.unwrap_or_default(),
            )))
        }
    }

    /// Send a custom event
    pub async fn send_event(&self, name: &str, data: serde_json::Value) -> Result<(), IpcError> {
        let message = IpcMessage::Event {
            name: name.to_string(),
            data,
        };

        let response = self.send(&message).await?;
        if response.success {
            Ok(())
        } else {
            Err(IpcError::IoError(std::io::Error::new(
                std::io::ErrorKind::Other,
                response.message.unwrap_or_default(),
            )))
        }
    }

    /// Query daemon status
    pub async fn query_status(&self) -> Result<IpcResponse, IpcError> {
        self.send(&IpcMessage::StatusQuery).await
    }

    /// Check if daemon is running
    pub async fn is_running(&self) -> bool {
        self.query_status().await.is_ok()
    }
}

/// Metric submission helper for applications
pub struct MetricSubmitter {
    client: IpcClient,
    default_tags: std::collections::HashMap<String, String>,
}

impl MetricSubmitter {
    pub fn new(socket_path: &str) -> Self {
        Self {
            client: IpcClient::new(socket_path),
            default_tags: std::collections::HashMap::new(),
        }
    }

    pub fn with_tag(mut self, key: &str, value: &str) -> Self {
        self.default_tags.insert(key.to_string(), value.to_string());
        self
    }

    pub async fn submit(&self, name: &str, value: f64, unit: Option<&str>) -> Result<(), IpcError> {
        self.client.send_metric(name, value, unit, self.default_tags.clone()).await
    }

    pub async fn gauge(&self, name: &str, value: f64) -> Result<(), IpcError> {
        self.submit(name, value, None).await
    }

    pub async fn counter(&self, name: &str, value: f64) -> Result<(), IpcError> {
        self.submit(name, value, Some("count")).await
    }

    pub async fn timing(&self, name: &str, milliseconds: f64) -> Result<(), IpcError> {
        self.submit(name, milliseconds, Some("ms")).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_ipc_message_serialization() {
        let message = IpcMessage::Metric {
            name: "test".to_string(),
            value: 42.0,
            unit: Some("count".to_string()),
            tags: std::collections::HashMap::new(),
        };

        let json = serde_json::to_string(&message).unwrap();
        assert!(json.contains("metric"));
        assert!(json.contains("test"));
    }

    #[test]
    fn test_ipc_response() {
        let response = IpcResponse::ok();
        assert!(response.success);

        let response = IpcResponse::error("test error");
        assert!(!response.success);
        assert_eq!(response.message, Some("test error".to_string()));
    }
}
