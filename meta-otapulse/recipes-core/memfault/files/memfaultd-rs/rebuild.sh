#!/bin/bash
# Simple rebuild script for memfaultd-rs
# Run this on the target device to rebuild and restart the daemon

set -e

echo "Building memfaultd-rs..."
cargo build --release

echo "Stopping memfaultd service..."
sudo systemctl stop memfaultd

echo "Installing new binary..."
sudo cp target/release/memfaultd-rs /usr/bin/memfaultd-rs

echo "Starting memfaultd service..."
sudo systemctl start memfaultd

echo "Checking service status..."
sudo systemctl status memfaultd

echo "Done! Tailing logs..."
sudo journalctl -u memfaultd -f
