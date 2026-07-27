//! Metrics collection module for socmond
//!
//! Collects system metrics including CPU, memory, disk, network, and temperature.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::fs;
use std::io::{BufRead, BufReader};
use std::path::Path;
use thiserror::Error;
use tokio::sync::RwLock;

#[derive(Error, Debug)]
pub enum MetricsError {
    #[error("Failed to read system file: {0}")]
    ReadError(#[from] std::io::Error),
    #[error("Failed to parse metrics: {0}")]
    ParseError(String),
}

/// Single metric data point (format expected by server)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Metric {
    /// Metric type (cpu, memory, disk, network, temperature, system, custom)
    #[serde(rename = "type")]
    pub metric_type: String,
    /// Metric name
    pub name: String,
    /// Metric value
    pub value: f64,
    /// Timestamp in ISO 8601 format
    pub timestamp: String,
    /// Unit of measurement
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unit: Option<String>,
    /// Additional tags
    #[serde(skip_serializing_if = "HashMap::is_empty", default)]
    pub tags: HashMap<String, String>,
}

/// Batch of metrics ready for upload (format expected by server)
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MetricsBatch {
    pub metrics: Vec<Metric>,
}

/// CPU statistics
#[derive(Debug, Clone, Default)]
pub struct CpuStats {
    pub user: u64,
    pub nice: u64,
    pub system: u64,
    pub idle: u64,
    pub iowait: u64,
    pub irq: u64,
    pub softirq: u64,
    pub steal: u64,
}

impl CpuStats {
    fn total(&self) -> u64 {
        self.user + self.nice + self.system + self.idle + self.iowait + self.irq + self.softirq + self.steal
    }

    fn busy(&self) -> u64 {
        self.user + self.nice + self.system + self.irq + self.softirq + self.steal
    }
}

/// Network interface statistics
#[derive(Debug, Clone, Default)]
pub struct NetworkStats {
    pub interface: String,
    pub rx_bytes: u64,
    pub tx_bytes: u64,
    pub rx_packets: u64,
    pub tx_packets: u64,
    pub rx_errors: u64,
    pub tx_errors: u64,
}

/// Memory statistics
#[derive(Debug, Clone, Default)]
pub struct MemoryStats {
    pub total_bytes: u64,
    pub available_bytes: u64,
    pub free_bytes: u64,
    pub buffers_bytes: u64,
    pub cached_bytes: u64,
    pub swap_total_bytes: u64,
    pub swap_free_bytes: u64,
}

impl MemoryStats {
    pub fn used_percent(&self) -> f64 {
        if self.total_bytes == 0 {
            return 0.0;
        }
        ((self.total_bytes - self.available_bytes) as f64 / self.total_bytes as f64) * 100.0
    }
}

/// Disk statistics
#[derive(Debug, Clone, Default)]
pub struct DiskStats {
    pub mount_point: String,
    pub total_bytes: u64,
    pub used_bytes: u64,
    pub available_bytes: u64,
    pub usage_percent: f64,
}

/// Temperature reading
#[derive(Debug, Clone)]
pub struct TemperatureReading {
    pub zone: String,
    pub celsius: f64,
}

/// Metrics collector
pub struct MetricsCollector {
    previous_cpu: RwLock<Option<CpuStats>>,
    previous_network: RwLock<HashMap<String, NetworkStats>>,
    temperature_paths: Vec<String>,
}

impl MetricsCollector {
    pub fn new(temperature_paths: Vec<String>) -> Self {
        Self {
            previous_cpu: RwLock::new(None),
            previous_network: RwLock::new(HashMap::new()),
            temperature_paths,
        }
    }

    /// Collect all metrics
    pub async fn collect_all(&self, config: &crate::config::SystemConfig) -> Vec<Metric> {
        let mut metrics = Vec::new();
        let timestamp = Utc::now().to_rfc3339();

        if config.collect_cpu_metrics {
            if let Ok(cpu_percent) = self.collect_cpu_usage().await {
                metrics.push(Metric {
                    metric_type: "cpu".to_string(),
                    name: "usage_percent".to_string(),
                    value: cpu_percent,
                    timestamp: timestamp.clone(),
                    unit: Some("percent".to_string()),
                    tags: HashMap::new(),
                });
            }

            if let Ok(load) = self.collect_load_average() {
                metrics.push(Metric {
                    metric_type: "cpu".to_string(),
                    name: "load_average_1m".to_string(),
                    value: load.0,
                    timestamp: timestamp.clone(),
                    unit: None,
                    tags: HashMap::new(),
                });
                metrics.push(Metric {
                    metric_type: "cpu".to_string(),
                    name: "load_average_5m".to_string(),
                    value: load.1,
                    timestamp: timestamp.clone(),
                    unit: None,
                    tags: HashMap::new(),
                });
                metrics.push(Metric {
                    metric_type: "cpu".to_string(),
                    name: "load_average_15m".to_string(),
                    value: load.2,
                    timestamp: timestamp.clone(),
                    unit: None,
                    tags: HashMap::new(),
                });
            }
        }

        if config.collect_memory_metrics {
            if let Ok(mem) = self.collect_memory_stats() {
                metrics.push(Metric {
                    metric_type: "memory".to_string(),
                    name: "used_percent".to_string(),
                    value: mem.used_percent(),
                    timestamp: timestamp.clone(),
                    unit: Some("percent".to_string()),
                    tags: HashMap::new(),
                });
                metrics.push(Metric {
                    metric_type: "memory".to_string(),
                    name: "available_bytes".to_string(),
                    value: mem.available_bytes as f64,
                    timestamp: timestamp.clone(),
                    unit: Some("bytes".to_string()),
                    tags: HashMap::new(),
                });
                metrics.push(Metric {
                    metric_type: "memory".to_string(),
                    name: "total_bytes".to_string(),
                    value: mem.total_bytes as f64,
                    timestamp: timestamp.clone(),
                    unit: Some("bytes".to_string()),
                    tags: HashMap::new(),
                });
                if mem.swap_total_bytes > 0 {
                    let swap_used = mem.swap_total_bytes - mem.swap_free_bytes;
                    let swap_percent = (swap_used as f64 / mem.swap_total_bytes as f64) * 100.0;
                    metrics.push(Metric {
                        metric_type: "memory".to_string(),
                        name: "swap_used_percent".to_string(),
                        value: swap_percent,
                        timestamp: timestamp.clone(),
                        unit: Some("percent".to_string()),
                        tags: HashMap::new(),
                    });
                }
            }
        }

        if config.collect_disk_metrics {
            if let Ok(disks) = self.collect_disk_stats() {
                for disk in disks {
                    let mut tags = HashMap::new();
                    tags.insert("mount_point".to_string(), disk.mount_point.clone());

                    metrics.push(Metric {
                        metric_type: "disk".to_string(),
                        name: "used_percent".to_string(),
                        value: disk.usage_percent,
                        timestamp: timestamp.clone(),
                        unit: Some("percent".to_string()),
                        tags: tags.clone(),
                    });
                    metrics.push(Metric {
                        metric_type: "disk".to_string(),
                        name: "available_bytes".to_string(),
                        value: disk.available_bytes as f64,
                        timestamp: timestamp.clone(),
                        unit: Some("bytes".to_string()),
                        tags,
                    });
                }
            }
        }

        if config.collect_network_metrics {
            if let Ok(net_stats) = self.collect_network_stats().await {
                for stats in net_stats {
                    let mut tags = HashMap::new();
                    tags.insert("interface".to_string(), stats.interface.clone());

                    metrics.push(Metric {
                        metric_type: "network".to_string(),
                        name: "rx_bytes".to_string(),
                        value: stats.rx_bytes as f64,
                        timestamp: timestamp.clone(),
                        unit: Some("bytes".to_string()),
                        tags: tags.clone(),
                    });
                    metrics.push(Metric {
                        metric_type: "network".to_string(),
                        name: "tx_bytes".to_string(),
                        value: stats.tx_bytes as f64,
                        timestamp: timestamp.clone(),
                        unit: Some("bytes".to_string()),
                        tags: tags.clone(),
                    });
                    metrics.push(Metric {
                        metric_type: "network".to_string(),
                        name: "rx_errors".to_string(),
                        value: stats.rx_errors as f64,
                        timestamp: timestamp.clone(),
                        unit: Some("count".to_string()),
                        tags: tags.clone(),
                    });
                    metrics.push(Metric {
                        metric_type: "network".to_string(),
                        name: "tx_errors".to_string(),
                        value: stats.tx_errors as f64,
                        timestamp: timestamp.clone(),
                        unit: Some("count".to_string()),
                        tags,
                    });
                }
            }
        }

        if config.collect_temperature {
            if let Ok(temps) = self.collect_temperatures() {
                for temp in temps {
                    let mut tags = HashMap::new();
                    tags.insert("zone".to_string(), temp.zone);

                    metrics.push(Metric {
                        metric_type: "temperature".to_string(),
                        name: "celsius".to_string(),
                        value: temp.celsius,
                        timestamp: timestamp.clone(),
                        unit: Some("celsius".to_string()),
                        tags,
                    });
                }
            }
        }

        // Always collect uptime
        if let Ok(uptime) = self.collect_uptime() {
            metrics.push(Metric {
                metric_type: "system".to_string(),
                name: "uptime".to_string(),
                value: uptime,
                timestamp: timestamp.clone(),
                unit: Some("seconds".to_string()),
                tags: HashMap::new(),
            });
        }

        // Collect process count
        if let Ok(procs) = self.collect_process_count() {
            metrics.push(Metric {
                metric_type: "system".to_string(),
                name: "process_count".to_string(),
                value: procs as f64,
                timestamp: timestamp.clone(),
                unit: Some("count".to_string()),
                tags: HashMap::new(),
            });
        }

        // Collect file descriptor usage
        if let Ok((used, max)) = self.collect_fd_usage() {
            metrics.push(Metric {
                metric_type: "system".to_string(),
                name: "fd_used".to_string(),
                value: used as f64,
                timestamp: timestamp.clone(),
                unit: Some("count".to_string()),
                tags: HashMap::new(),
            });
            metrics.push(Metric {
                metric_type: "system".to_string(),
                name: "fd_max".to_string(),
                value: max as f64,
                timestamp: timestamp.clone(),
                unit: Some("count".to_string()),
                tags: HashMap::new(),
            });
        }

        metrics
    }

    /// Collect CPU usage percentage
    async fn collect_cpu_usage(&self) -> Result<f64, MetricsError> {
        let current = self.read_cpu_stats()?;
        let mut previous = self.previous_cpu.write().await;

        let usage = if let Some(prev) = previous.as_ref() {
            let total_diff = current.total().saturating_sub(prev.total());
            let busy_diff = current.busy().saturating_sub(prev.busy());

            if total_diff > 0 {
                (busy_diff as f64 / total_diff as f64) * 100.0
            } else {
                0.0
            }
        } else {
            // First sample, return current busy percentage
            let total = current.total();
            if total > 0 {
                (current.busy() as f64 / total as f64) * 100.0
            } else {
                0.0
            }
        };

        *previous = Some(current);
        Ok(usage)
    }

    fn read_cpu_stats(&self) -> Result<CpuStats, MetricsError> {
        let content = fs::read_to_string("/proc/stat")?;

        for line in content.lines() {
            if line.starts_with("cpu ") {
                let parts: Vec<&str> = line.split_whitespace().collect();
                if parts.len() >= 8 {
                    return Ok(CpuStats {
                        user: parts[1].parse().unwrap_or(0),
                        nice: parts[2].parse().unwrap_or(0),
                        system: parts[3].parse().unwrap_or(0),
                        idle: parts[4].parse().unwrap_or(0),
                        iowait: parts[5].parse().unwrap_or(0),
                        irq: parts[6].parse().unwrap_or(0),
                        softirq: parts[7].parse().unwrap_or(0),
                        steal: parts.get(8).and_then(|s| s.parse().ok()).unwrap_or(0),
                    });
                }
            }
        }

        Err(MetricsError::ParseError("Failed to parse /proc/stat".to_string()))
    }

    /// Collect load average
    fn collect_load_average(&self) -> Result<(f64, f64, f64), MetricsError> {
        let content = fs::read_to_string("/proc/loadavg")?;
        let parts: Vec<&str> = content.split_whitespace().collect();

        if parts.len() >= 3 {
            Ok((
                parts[0].parse().unwrap_or(0.0),
                parts[1].parse().unwrap_or(0.0),
                parts[2].parse().unwrap_or(0.0),
            ))
        } else {
            Err(MetricsError::ParseError("Failed to parse /proc/loadavg".to_string()))
        }
    }

    /// Collect memory statistics
    fn collect_memory_stats(&self) -> Result<MemoryStats, MetricsError> {
        let file = fs::File::open("/proc/meminfo")?;
        let reader = BufReader::new(file);
        let mut stats = MemoryStats::default();

        for line in reader.lines() {
            let line = line?;
            let parts: Vec<&str> = line.split_whitespace().collect();

            if parts.len() >= 2 {
                let value: u64 = parts[1].parse().unwrap_or(0) * 1024; // Convert from KB to bytes

                match parts[0].trim_end_matches(':') {
                    "MemTotal" => stats.total_bytes = value,
                    "MemFree" => stats.free_bytes = value,
                    "MemAvailable" => stats.available_bytes = value,
                    "Buffers" => stats.buffers_bytes = value,
                    "Cached" => stats.cached_bytes = value,
                    "SwapTotal" => stats.swap_total_bytes = value,
                    "SwapFree" => stats.swap_free_bytes = value,
                    _ => {}
                }
            }
        }

        // If MemAvailable is not present (older kernels), estimate it
        if stats.available_bytes == 0 {
            stats.available_bytes = stats.free_bytes + stats.buffers_bytes + stats.cached_bytes;
        }

        Ok(stats)
    }

    /// Collect disk statistics
    fn collect_disk_stats(&self) -> Result<Vec<DiskStats>, MetricsError> {
        let mut disks = Vec::new();
        let mounts = fs::read_to_string("/proc/mounts")?;

        for line in mounts.lines() {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 2 {
                let mount_point = parts[1];

                // Only collect stats for real filesystems
                if mount_point.starts_with('/')
                    && !mount_point.starts_with("/proc")
                    && !mount_point.starts_with("/sys")
                    && !mount_point.starts_with("/dev")
                    && !mount_point.starts_with("/run")
                {
                    if let Ok(stats) = self.get_disk_usage(mount_point) {
                        disks.push(stats);
                    }
                }
            }
        }

        // Always include root if not already present
        if !disks.iter().any(|d| d.mount_point == "/") {
            if let Ok(stats) = self.get_disk_usage("/") {
                disks.push(stats);
            }
        }

        Ok(disks)
    }

    fn get_disk_usage(&self, mount_point: &str) -> Result<DiskStats, MetricsError> {
        use std::ffi::CString;

        let path = CString::new(mount_point).map_err(|_| {
            MetricsError::ParseError("Invalid mount point path".to_string())
        })?;

        unsafe {
            let mut stat: libc::statvfs = std::mem::zeroed();
            if libc::statvfs(path.as_ptr(), &mut stat) == 0 {
                let block_size = stat.f_frsize as u64;
                let total = stat.f_blocks * block_size;
                let available = stat.f_bavail * block_size;
                let free = stat.f_bfree * block_size;
                let used = total - free;

                let usage_percent = if total > 0 {
                    (used as f64 / total as f64) * 100.0
                } else {
                    0.0
                };

                Ok(DiskStats {
                    mount_point: mount_point.to_string(),
                    total_bytes: total,
                    used_bytes: used,
                    available_bytes: available,
                    usage_percent,
                })
            } else {
                Err(MetricsError::ReadError(std::io::Error::last_os_error()))
            }
        }
    }

    /// Collect network statistics
    async fn collect_network_stats(&self) -> Result<Vec<NetworkStats>, MetricsError> {
        let mut stats = Vec::new();
        let net_dev = fs::read_to_string("/proc/net/dev")?;

        for line in net_dev.lines().skip(2) {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 11 {
                let interface = parts[0].trim_end_matches(':').to_string();

                // Skip loopback
                if interface == "lo" {
                    continue;
                }

                stats.push(NetworkStats {
                    interface,
                    rx_bytes: parts[1].parse().unwrap_or(0),
                    rx_packets: parts[2].parse().unwrap_or(0),
                    rx_errors: parts[3].parse().unwrap_or(0),
                    tx_bytes: parts[9].parse().unwrap_or(0),
                    tx_packets: parts[10].parse().unwrap_or(0),
                    tx_errors: parts[11].parse().unwrap_or(0),
                });
            }
        }

        Ok(stats)
    }

    /// Collect temperature readings
    fn collect_temperatures(&self) -> Result<Vec<TemperatureReading>, MetricsError> {
        let mut readings = Vec::new();

        // Check configured paths first
        for path in &self.temperature_paths {
            if let Ok(temp) = self.read_temperature(path) {
                let zone = Path::new(path)
                    .parent()
                    .and_then(|p| p.file_name())
                    .and_then(|n| n.to_str())
                    .unwrap_or("unknown")
                    .to_string();

                readings.push(TemperatureReading {
                    zone,
                    celsius: temp,
                });
            }
        }

        // Also check hwmon sensors
        if let Ok(entries) = fs::read_dir("/sys/class/hwmon") {
            for entry in entries.flatten() {
                let hwmon_path = entry.path();
                if let Ok(files) = fs::read_dir(&hwmon_path) {
                    for file in files.flatten() {
                        let file_name = file.file_name();
                        let name_str = file_name.to_string_lossy();
                        if name_str.starts_with("temp") && name_str.ends_with("_input") {
                            if let Ok(temp) = self.read_temperature(&file.path().to_string_lossy()) {
                                let zone = hwmon_path
                                    .file_name()
                                    .and_then(|n| n.to_str())
                                    .unwrap_or("unknown")
                                    .to_string();

                                readings.push(TemperatureReading {
                                    zone: format!("{}_{}", zone, name_str.trim_end_matches("_input")),
                                    celsius: temp,
                                });
                            }
                        }
                    }
                }
            }
        }

        Ok(readings)
    }

    fn read_temperature(&self, path: &str) -> Result<f64, MetricsError> {
        let content = fs::read_to_string(path)?;
        let millidegrees: i64 = content.trim().parse().map_err(|_| {
            MetricsError::ParseError(format!("Failed to parse temperature from {}", path))
        })?;
        Ok(millidegrees as f64 / 1000.0)
    }

    /// Collect system uptime
    fn collect_uptime(&self) -> Result<f64, MetricsError> {
        let content = fs::read_to_string("/proc/uptime")?;
        let parts: Vec<&str> = content.split_whitespace().collect();

        if !parts.is_empty() {
            Ok(parts[0].parse().unwrap_or(0.0))
        } else {
            Err(MetricsError::ParseError("Failed to parse /proc/uptime".to_string()))
        }
    }

    /// Collect number of running processes
    fn collect_process_count(&self) -> Result<u32, MetricsError> {
        let mut count = 0u32;

        if let Ok(entries) = fs::read_dir("/proc") {
            for entry in entries.flatten() {
                if let Some(name) = entry.file_name().to_str() {
                    if name.chars().all(|c| c.is_ascii_digit()) {
                        count += 1;
                    }
                }
            }
        }

        Ok(count)
    }

    /// Collect file descriptor usage
    fn collect_fd_usage(&self) -> Result<(u64, u64), MetricsError> {
        let content = fs::read_to_string("/proc/sys/fs/file-nr")?;
        let parts: Vec<&str> = content.split_whitespace().collect();

        if parts.len() >= 3 {
            let allocated: u64 = parts[0].parse().unwrap_or(0);
            let max: u64 = parts[2].parse().unwrap_or(0);
            Ok((allocated, max))
        } else {
            Err(MetricsError::ParseError("Failed to parse /proc/sys/fs/file-nr".to_string()))
        }
    }
}

/// Metrics buffer for storing metrics before upload
pub struct MetricsBuffer {
    metrics: RwLock<Vec<Metric>>,
    max_size: usize,
}

impl MetricsBuffer {
    pub fn new(max_size_bytes: u64) -> Self {
        // Estimate ~200 bytes per metric
        let max_metrics = (max_size_bytes / 200) as usize;
        Self {
            metrics: RwLock::new(Vec::with_capacity(max_metrics.min(10000))),
            max_size: max_metrics,
        }
    }

    pub async fn add(&self, metrics: Vec<Metric>) {
        let mut buffer = self.metrics.write().await;

        for metric in metrics {
            if buffer.len() >= self.max_size {
                // Remove oldest metrics
                buffer.remove(0);
            }
            buffer.push(metric);
        }
    }

    pub async fn drain(&self) -> Vec<Metric> {
        let mut buffer = self.metrics.write().await;
        std::mem::take(&mut *buffer)
    }

    pub async fn len(&self) -> usize {
        self.metrics.read().await.len()
    }

    pub async fn is_empty(&self) -> bool {
        self.metrics.read().await.is_empty()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_collect_memory_stats() {
        let collector = MetricsCollector::new(vec![]);
        let stats = collector.collect_memory_stats();
        assert!(stats.is_ok());
        let stats = stats.unwrap();
        assert!(stats.total_bytes > 0);
    }

    #[tokio::test]
    async fn test_collect_uptime() {
        let collector = MetricsCollector::new(vec![]);
        let uptime = collector.collect_uptime();
        assert!(uptime.is_ok());
        assert!(uptime.unwrap() > 0.0);
    }

    #[tokio::test]
    async fn test_metrics_buffer() {
        let buffer = MetricsBuffer::new(10000);
        let metric = Metric {
            metric_type: "test".to_string(),
            name: "test_metric".to_string(),
            value: 42.0,
            timestamp: Utc::now().to_rfc3339(),
            unit: None,
            tags: HashMap::new(),
        };

        buffer.add(vec![metric.clone()]).await;
        assert_eq!(buffer.len().await, 1);

        let drained = buffer.drain().await;
        assert_eq!(drained.len(), 1);
        assert!(buffer.is_empty().await);
    }
}
