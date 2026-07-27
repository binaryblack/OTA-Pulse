// Copyright 2026 SoC Monitoring
package client

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectPrimaryWirelessInterfaceNoProcFile(t *testing.T) {
	// on a build/test host /proc/net/wireless may or may not exist; either
	// way detectPrimaryWirelessInterface must not panic and must return ""
	// when there is no wireless interface, causing CheckNetworkQuality to
	// treat the device as wired (allow download).
	iface := detectPrimaryWirelessInterface()
	_ = iface // value depends on host; just confirm no panic occurred
}

func TestMinAcceptableSignalDefault(t *testing.T) {
	os.Unsetenv(minSignalEnvVar)
	assert.Equal(t, defaultMinAcceptableSignalDBm, minAcceptableSignal())
}

func TestMinAcceptableSignalConfigurable(t *testing.T) {
	os.Setenv(minSignalEnvVar, "-60")
	defer os.Unsetenv(minSignalEnvVar)
	assert.Equal(t, -60, minAcceptableSignal())
}

func TestMinAcceptableSignalInvalidFallsBackToDefault(t *testing.T) {
	os.Setenv(minSignalEnvVar, "not-a-number")
	defer os.Unsetenv(minSignalEnvVar)
	assert.Equal(t, defaultMinAcceptableSignalDBm, minAcceptableSignal())
}

func TestCheckNetworkQualityNoWirelessInterfaceAllowsDownload(t *testing.T) {
	// On hosts without a wireless interface (e.g. CI runners), the check
	// must be a no-op that allows the download - it must never hard-block
	// on a wired connection.
	if detectPrimaryWirelessInterface() != "" {
		t.Skip("host has a wireless interface; this case is exercised on wired hosts only")
	}
	assert.True(t, CheckNetworkQuality())
}
