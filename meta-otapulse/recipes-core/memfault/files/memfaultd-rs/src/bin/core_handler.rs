//! memfault-core-handler - Coredump Handler
//!
//! Called by kernel via core_pattern when a process crashes.
//! Compresses and stores coredumps for later upload by memfaultd.
//!
//! Usage (in core_pattern):
//!   |/usr/bin/memfault-core-handler %p %u %g %s %t %c %h %e

use flate2::write::GzEncoder;
use flate2::Compression;
use serde::{Deserialize, Serialize};
use std::env;
use std::fs::{self, File};
use std::io::{self, Read, Write};
use std::os::unix::net::UnixStream;
use std::path::Path;

const COREDUMP_DIR: &str = "/var/lib/memfault/coredumps";
const MAX_COREDUMP_SIZE: u64 = 50 * 1024 * 1024; // 50MB
const MAX_DIR_SIZE: u64 = 500 * 1024 * 1024; // 500MB
const LOG_FILE: &str = "/var/log/memfault/coredump.log";
const SOCKET_PATH: &str = "/run/memfault/metrics.sock";

#[derive(Debug, Serialize, Deserialize)]
struct CoredumpMetadata {
    timestamp: i64,
    timestamp_iso: String,
    pid: u32,
    uid: u32,
    gid: u32,
    signal: i32,
    executable: String,
    hostname: String,
    coredump_file: String,
    coredump_size: u64,
    compressed: bool,
    uploaded: bool,
    upload_attempts: u32,
    kernel_version: String,
}

fn main() {
    // Parse command line arguments
    // Arguments from kernel: %p %u %g %s %t %c %h %e
    let args: Vec<String> = env::args().collect();

    let pid: u32 = args.get(1).and_then(|s| s.parse().ok()).unwrap_or(0);
    let uid: u32 = args.get(2).and_then(|s| s.parse().ok()).unwrap_or(0);
    let gid: u32 = args.get(3).and_then(|s| s.parse().ok()).unwrap_or(0);
    let signal: i32 = args.get(4).and_then(|s| s.parse().ok()).unwrap_or(0);
    let timestamp: i64 = args.get(5).and_then(|s| s.parse().ok()).unwrap_or_else(|| {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0)
    });
    let _limit: u64 = args.get(6).and_then(|s| s.parse().ok()).unwrap_or(0);
    let hostname: String = args
        .get(7)
        .cloned()
        .unwrap_or_else(|| gethostname().unwrap_or_else(|| "unknown".to_string()));
    let executable: String = args.get(8).cloned().unwrap_or_else(|| "unknown".to_string());

    // Create directories
    if let Err(e) = fs::create_dir_all(COREDUMP_DIR) {
        eprintln!("Failed to create coredump directory: {}", e);
        std::process::exit(1);
    }

    if let Some(parent) = Path::new(LOG_FILE).parent() {
        let _ = fs::create_dir_all(parent);
    }

    // Log start
    log(&format!(
        "Received coredump: pid={} executable={} signal={} ({})",
        pid,
        executable,
        signal,
        signal_name(signal)
    ));

    // Generate filenames
    let safe_executable = executable.replace('/', "_");
    let base_name = format!("core.{}.{}.{}", safe_executable, pid, timestamp);
    let coredump_file = format!("{}/{}.gz", COREDUMP_DIR, base_name);
    let metadata_file = format!("{}/{}.meta", COREDUMP_DIR, base_name);

    // Read core dump from stdin and compress
    match save_coredump(&coredump_file) {
        Ok(size) => {
            log(&format!(
                "Saved coredump: {} ({} bytes compressed)",
                coredump_file, size
            ));

            // Create metadata
            let timestamp_iso = chrono_format(timestamp);
            let kernel_version = get_kernel_version();

            let metadata = CoredumpMetadata {
                timestamp,
                timestamp_iso,
                pid,
                uid,
                gid,
                signal,
                executable: executable.clone(),
                hostname: hostname.clone(),
                coredump_file: coredump_file.clone(),
                coredump_size: size,
                compressed: true,
                uploaded: false,
                upload_attempts: 0,
                kernel_version,
            };

            // Write metadata
            if let Err(e) = save_metadata(&metadata_file, &metadata) {
                log(&format!("Failed to save metadata: {}", e));
            }

            // Notify memfaultd
            if let Err(e) = notify_memfaultd(&coredump_file, &metadata_file) {
                log(&format!("Failed to notify memfaultd (may not be running): {}", e));
            } else {
                log("Notified memfaultd of new coredump");
            }

            // Cleanup old coredumps
            if let Err(e) = cleanup_old_coredumps() {
                log(&format!("Failed to cleanup old coredumps: {}", e));
            }
        }
        Err(e) => {
            log(&format!("ERROR: Failed to capture coredump: {}", e));
            std::process::exit(1);
        }
    }
}

fn save_coredump(output_path: &str) -> io::Result<u64> {
    let file = File::create(output_path)?;
    let mut encoder = GzEncoder::new(file, Compression::default());

    let stdin = io::stdin();
    let mut stdin_lock = stdin.lock();
    let mut buffer = [0u8; 65536];
    let mut total_read: u64 = 0;

    loop {
        let n = stdin_lock.read(&mut buffer)?;
        if n == 0 {
            break;
        }

        total_read += n as u64;

        // Check size limit
        if total_read > MAX_COREDUMP_SIZE {
            log(&format!(
                "Coredump exceeds max size ({} > {}), truncating",
                total_read, MAX_COREDUMP_SIZE
            ));
            encoder.write_all(&buffer[..n])?;
            break;
        }

        encoder.write_all(&buffer[..n])?;
    }

    encoder.finish()?;

    // Get compressed size
    let metadata = fs::metadata(output_path)?;
    Ok(metadata.len())
}

fn save_metadata(path: &str, metadata: &CoredumpMetadata) -> io::Result<()> {
    let json = serde_json::to_string_pretty(metadata)
        .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;
    fs::write(path, json)
}

fn notify_memfaultd(coredump_file: &str, metadata_file: &str) -> io::Result<()> {
    if !Path::new(SOCKET_PATH).exists() {
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            "Socket not found",
        ));
    }

    let mut stream = UnixStream::connect(SOCKET_PATH)?;
    stream.set_write_timeout(Some(std::time::Duration::from_secs(1)))?;

    let message = serde_json::json!({
        "type": "coredump",
        "file": coredump_file,
        "meta": metadata_file
    });

    let message_str = message.to_string();
    stream.write_all(message_str.as_bytes())?;
    stream.write_all(b"\n")?;

    Ok(())
}

fn cleanup_old_coredumps() -> io::Result<()> {
    let mut files: Vec<(String, u64, std::time::SystemTime)> = Vec::new();
    let mut total_size: u64 = 0;

    for entry in fs::read_dir(COREDUMP_DIR)? {
        let entry = entry?;
        let path = entry.path();
        let metadata = entry.metadata()?;

        if path.extension().map_or(false, |e| e == "gz") {
            let size = metadata.len();
            let modified = metadata.modified()?;
            total_size += size;
            files.push((path.to_string_lossy().to_string(), size, modified));
        }
    }

    // Sort by modification time (oldest first)
    files.sort_by(|a, b| a.2.cmp(&b.2));

    // Remove oldest files until under limit
    while total_size > MAX_DIR_SIZE && !files.is_empty() {
        let (path, size, _) = files.remove(0);
        log(&format!("Removing old coredump: {}", path));

        if fs::remove_file(&path).is_ok() {
            total_size -= size;
        }

        // Also remove metadata file
        let meta_path = path.replace(".gz", ".meta");
        let _ = fs::remove_file(&meta_path);
    }

    Ok(())
}

fn log(message: &str) {
    let timestamp = chrono_format(
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0),
    );

    let log_line = format!("{} {}\n", timestamp, message);

    // Print to stderr (will be captured by kernel)
    eprintln!("{}", message);

    // Append to log file
    if let Ok(mut file) = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(LOG_FILE)
    {
        let _ = file.write_all(log_line.as_bytes());
    }
}

fn signal_name(signal: i32) -> &'static str {
    match signal {
        1 => "SIGHUP",
        2 => "SIGINT",
        3 => "SIGQUIT",
        4 => "SIGILL",
        5 => "SIGTRAP",
        6 => "SIGABRT",
        7 => "SIGBUS",
        8 => "SIGFPE",
        9 => "SIGKILL",
        10 => "SIGUSR1",
        11 => "SIGSEGV",
        12 => "SIGUSR2",
        13 => "SIGPIPE",
        14 => "SIGALRM",
        15 => "SIGTERM",
        _ => "UNKNOWN",
    }
}

fn gethostname() -> Option<String> {
    fs::read_to_string("/etc/hostname")
        .ok()
        .map(|s| s.trim().to_string())
}

fn get_kernel_version() -> String {
    fs::read_to_string("/proc/version")
        .ok()
        .and_then(|content| {
            content
                .split_whitespace()
                .nth(2)
                .map(|s| s.to_string())
        })
        .unwrap_or_else(|| "unknown".to_string())
}

fn chrono_format(timestamp: i64) -> String {
    use std::time::{Duration, UNIX_EPOCH};

    let system_time = UNIX_EPOCH + Duration::from_secs(timestamp as u64);
    let datetime: chrono::DateTime<chrono::Utc> = system_time.into();
    datetime.to_rfc3339()
}
