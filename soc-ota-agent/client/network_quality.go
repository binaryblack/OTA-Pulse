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
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	// defaultMinAcceptableSignalDBm is used when OTAPULSE_WIFI_MIN_SIGNAL_DBM is unset.
	defaultMinAcceptableSignalDBm = -75

	minSignalEnvVar = "OTAPULSE_WIFI_MIN_SIGNAL_DBM"
	// enforceEnvVar opts into hard-blocking downloads below the threshold.
	// Unset (the default) makes the check advisory: a weak signal is logged
	// but the download still proceeds, since a permanent hard block below a
	// hardcoded dBm value can strand devices on flaky-but-usable links.
	enforceEnvVar = "OTAPULSE_WIFI_QUALITY_ENFORCE"
)

// detectPrimaryWirelessInterface returns the name of the first wireless
// interface reported by the kernel (/proc/net/wireless), or "" if there is
// none. This replaces a hardcoded "wlan0" assumption, which silently
// skipped the quality check entirely on boards whose primary radio is
// named differently (e.g. wlp2s0, mlan0).
func detectPrimaryWirelessInterface() string {
	f, err := os.Open("/proc/net/wireless")
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		// first two lines are headers
		if lineNum <= 2 {
			continue
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		iface := strings.TrimSuffix(strings.Fields(line)[0], ":")
		if iface != "" {
			return iface
		}
	}
	return ""
}

func minAcceptableSignal() int {
	if v := os.Getenv(minSignalEnvVar); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
		log.Warnf("Invalid %s value %q, falling back to default (%d dBm)",
			minSignalEnvVar, v, defaultMinAcceptableSignalDBm)
	}
	return defaultMinAcceptableSignalDBm
}

// CheckNetworkQuality checks WiFi signal strength before initiating OTA download.
// Returns true if the download should proceed. By default this is advisory only
// (a poor signal is logged, not blocked) — set OTAPULSE_WIFI_QUALITY_ENFORCE=1 to
// hard-block downloads below the configured threshold (OTAPULSE_WIFI_MIN_SIGNAL_DBM,
// default -75).
func CheckNetworkQuality() bool {
	iface := detectPrimaryWirelessInterface()
	if iface == "" {
		// no wireless interface present; assume wired connection - allow download
		log.Debug("Network quality check skipped (no wireless interface detected)")
		return true
	}

	cmd := exec.Command("sh", "-c", "iw dev "+iface+" link 2>/dev/null | grep signal | awk '{print $2}'")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		log.Debugf("Network quality check skipped (iw unavailable for %s)", iface)
		return true
	}

	signalStr := strings.TrimSpace(string(output))
	signalDBm, err := strconv.Atoi(signalStr)
	if err != nil {
		log.Debug("Could not parse WiFi signal strength, allowing download")
		return true
	}

	minAcceptable := minAcceptableSignal()

	if signalDBm < minAcceptable && signalDBm != 0 {
		enforce := os.Getenv(enforceEnvVar) == "1"
		if enforce {
			log.Warnf("Poor WiFi signal strength on %s (%d dBm), deferring OTA update. Minimum required: %d dBm",
				iface, signalDBm, minAcceptable)
			return false
		}
		log.Warnf("Poor WiFi signal strength on %s (%d dBm, minimum recommended: %d dBm); "+
			"proceeding anyway (advisory only; set %s=1 to hard-block)",
			iface, signalDBm, minAcceptable, enforceEnvVar)
		return true
	}

	log.Infof("Network quality acceptable for OTA download on %s (signal: %d dBm)", iface, signalDBm)
	return true
}
