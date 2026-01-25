// Copyright 2026 SoC Monitoring
// Modified from Mender (Apache 2.0 License)
//
//	Licensed under the Apache License, Version 2.0 (the "License");
//	you may not use this file except in compliance with the License.
//	You may obtain a copy of the License at
//
//	    http://www.apache.org/licenses/LICENSE-2.0
package client

import (
	"os/exec"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

// CheckNetworkQuality checks WiFi signal strength before initiating OTA download
// Returns true if network quality is acceptable for download, false otherwise
// This is critical for Indian market deployments with unreliable connectivity
func CheckNetworkQuality() bool {
	// Try to get WiFi signal strength using 'iw' command
	cmd := exec.Command("sh", "-c", "iw dev wlan0 link 2>/dev/null | grep signal | awk '{print $2}'")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		// If iw fails or wlan0 doesn't exist, assume wired connection - allow download
		log.Debug("Network quality check skipped (wired connection or iw unavailable)")
		return true
	}

	signalStr := strings.TrimSpace(string(output))
	signalDBm, err := strconv.Atoi(signalStr)
	if err != nil {
		// Failed to parse signal, allow download
		log.Debug("Could not parse WiFi signal strength, allowing download")
		return true
	}

	// Signal strength thresholds (in dBm):
	// > -50: Excellent
	// > -60: Good  
	// > -70: Fair
	// > -75: Minimum acceptable
	// < -75: Poor - defer update
	const minAcceptableSignal = -75
	
	if signalDBm < minAcceptableSignal && signalDBm != 0 {
		log.Warnf("Poor WiFi signal strength (%d dBm), deferring OTA update. Minimum required: %d dBm", 
			signalDBm, minAcceptableSignal)
		return false
	}

	log.Infof("Network quality acceptable for OTA download (signal: %d dBm)", signalDBm)
	return true
}
