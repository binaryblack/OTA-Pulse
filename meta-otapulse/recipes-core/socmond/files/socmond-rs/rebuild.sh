#!/bin/bash
# Simple rebuild script for socmond-rs
# Run this on the target device to rebuild and restart the daemon

set -e

echo "Building socmond-rs..."
cargo build --release

echo "Stopping socmond service..."
sudo systemctl stop socmond

echo "Installing new binary..."
sudo cp target/release/socmond-rs /usr/bin/socmond-rs

echo "Starting socmond service..."
sudo systemctl start socmond

echo "Checking service status..."
sudo systemctl status socmond

echo "Done! Tailing logs..."
sudo journalctl -u socmond -f
