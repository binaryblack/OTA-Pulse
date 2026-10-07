// Copyright 2026 OTA-Pulse
//
//	Licensed under the Apache License, Version 2.0 (the "License");
//	you may not use this file except in compliance with the License.
//	You may obtain a copy of the License at
//
//	    http://www.apache.org/licenses/LICENSE-2.0
//
//	Unless required by applicable law or agreed to in writing, software
//	distributed under the License is distributed on an "AS IS" BASIS,
//	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	See the License for the specific language governing permissions and
//	limitations under the License.

package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mendersoftware/mender-artifact/artifact"
	"github.com/mendersoftware/mender-artifact/awriter"
	"github.com/mendersoftware/mender-artifact/handlers"
)

// ---- fixtures ---------------------------------------------------------------

// requireXdelta3 returns the host xdelta3. The delta apply tests exec the
// REAL binary; when it is missing they skip LOUDLY rather than pass vacuously.
func requireXdelta3(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("xdelta3")
	if err != nil {
		msg := "SKIPPED LOUDLY: xdelta3 binary not found on PATH. The rootfs-image-delta " +
			"apply path (installer/delta_rootfs.go) is NOT tested in this run. Install it " +
			"(apt install xdelta3) or put an xdelta3 binary on PATH and re-run."
		fmt.Fprintln(os.Stderr, "!!!!! "+msg)
		t.Skip(msg)
	}
	return p
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func fileSha256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return sha256Hex(b)
}

func randomBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

type fakeProvides struct {
	p   map[string]string
	err error
}

func (f *fakeProvides) GetProvides() (map[string]string, error) { return f.p, f.err }

// fakeDeltaHeaders overrides only what deltaRootfsInstaller.Initialize reads.
type fakeDeltaHeaders struct {
	handlers.ArtifactUpdateHeaders
	meta     map[string]interface{}
	provides artifact.TypeInfoProvides
	depends  artifact.TypeInfoDepends
}

func (h *fakeDeltaHeaders) GetUpdateMetaData() (map[string]interface{}, error) {
	return h.meta, nil
}
func (h *fakeDeltaHeaders) GetUpdateProvides() (artifact.TypeInfoProvides, error) {
	return h.provides, nil
}
func (h *fakeDeltaHeaders) GetUpdateDepends() (artifact.TypeInfoDepends, error) {
	return h.depends, nil
}

const (
	rwRootMountInfo = "22 1 179:2 / / rw,relatime shared:1 - ext4 /dev/mmcblk0p2 rw\n" +
		"23 22 0:5 / /proc rw,nosuid shared:2 - proc proc rw\n"
	roRootMountInfo = "22 1 179:2 / / ro,relatime shared:1 - ext4 /dev/mmcblk0p2 ro\n" +
		"23 22 0:5 / /proc rw,nosuid shared:2 - proc proc rw\n"
)

type deltaFixture struct {
	dir          string
	activePath   string
	inactivePath string
	base         []byte
	target       []byte
	patch        []byte
	partSize     int64
	env          *fakeBootEnv
	dev          *dualRootfsDeviceImpl
	factory      *DeltaRootfsFactory
}

// newDeltaFixture builds a base "rootfs" in the active slot file, a target
// derived from it, a REAL xdelta3 patch base->target, an inactive slot file
// pre-filled with 0xAA, and a dual-rootfs device + delta factory on top of
// them (read-only root mountinfo, real xdelta3).
func newDeltaFixture(t *testing.T) *deltaFixture {
	t.Helper()
	xd := requireXdelta3(t)
	dir := t.TempDir()

	base := randomBytes(1, 4*1024*1024)
	target := append([]byte(nil), base...)
	copy(target[100*1024:], randomBytes(2, 10*1024))
	copy(target[2*1024*1024:], randomBytes(3, 50*1024))
	target = append(target, randomBytes(4, 300*1024+12345)...) // not sector aligned

	f := &deltaFixture{
		dir:          dir,
		activePath:   filepath.Join(dir, "rootfs-a"),
		inactivePath: filepath.Join(dir, "rootfs-b"),
		base:         base,
		target:       target,
		partSize:     8 * 1024 * 1024,
	}
	require.NoError(t, os.WriteFile(f.activePath, base, 0600))
	require.NoError(t, os.WriteFile(f.inactivePath, bytes.Repeat([]byte{0xAA}, int(f.partSize)), 0600))

	f.patch = makeXdelta3Patch(t, xd, dir, base, target)
	t.Logf("delta ratio: patch %d bytes for a %d byte target (%.2f%%)",
		len(f.patch), len(target), 100*float64(len(f.patch))/float64(len(target)))
	require.Less(t, len(f.patch), len(target)/5, "delta should be far smaller than the target")

	oldSize, oldSector := BlockDeviceGetSizeOf, BlockDeviceGetSectorSizeOf
	BlockDeviceGetSizeOf = func(file *os.File) (uint64, error) {
		st, err := file.Stat()
		if err != nil {
			return 0, err
		}
		return uint64(st.Size()), nil
	}
	BlockDeviceGetSectorSizeOf = func(file *os.File) (int, error) { return 512, nil }
	t.Cleanup(func() {
		BlockDeviceGetSizeOf, BlockDeviceGetSectorSizeOf = oldSize, oldSector
	})

	f.env = &fakeBootEnv{}
	f.dev = &dualRootfsDeviceImpl{
		BootEnvReadWriter: f.env,
		partitions: &partitions{
			BootEnvReadWriter: f.env,
			rootfsPartA:       f.activePath,
			rootfsPartB:       f.inactivePath,
			active:            f.activePath,
			inactive:          f.inactivePath,
		},
	}
	mi := filepath.Join(dir, "mountinfo")
	require.NoError(t, os.WriteFile(mi, []byte(roRootMountInfo), 0600))
	f.factory = NewDeltaRootfsFactory(f.dev,
		&fakeProvides{p: map[string]string{RootfsChecksumKey: sha256Hex(base)}}, 60)
	f.factory.mountInfoPath = mi
	return f
}

func makeXdelta3Patch(t *testing.T, xd, dir string, base, target []byte) []byte {
	t.Helper()
	bp := filepath.Join(dir, fmt.Sprintf("enc-base-%d", rand.Int()))
	tp := filepath.Join(dir, fmt.Sprintf("enc-target-%d", rand.Int()))
	pp := filepath.Join(dir, fmt.Sprintf("enc-patch-%d", rand.Int()))
	require.NoError(t, os.WriteFile(bp, base, 0600))
	require.NoError(t, os.WriteFile(tp, target, 0600))
	out, err := exec.Command(xd, "-e", "-f", "-9", "-S", "none", "-s", bp, tp, pp).CombinedOutput()
	require.NoError(t, err, "xdelta3 encode: %s", out)
	patch, err := os.ReadFile(pp)
	require.NoError(t, err)
	for _, p := range []string{bp, tp, pp} {
		os.Remove(p)
	}
	return patch
}

func (f *deltaFixture) headers(targetSize int64) *fakeDeltaHeaders {
	return &fakeDeltaHeaders{
		meta: map[string]interface{}{
			"delta_format": "xdelta3",
			"target_size":  float64(targetSize), // as decoded from JSON
		},
		provides: artifact.TypeInfoProvides{RootfsChecksumKey: sha256Hex(f.target)},
		depends:  artifact.TypeInfoDepends{RootfsChecksumKey: sha256Hex(f.base)},
	}
}

// storer returns an initialized delta installer for the fixture.
func (f *deltaFixture) storer(t *testing.T, h *fakeDeltaHeaders) *deltaRootfsInstaller {
	t.Helper()
	ut := DeltaRootfsPayloadType
	us, err := f.factory.NewUpdateStorer(&ut, 0)
	require.NoError(t, err)
	d := us.(*deltaRootfsInstaller)
	require.NoError(t, d.Initialize(nil, nil, h))
	require.NoError(t, d.PrepareStoreUpdate())
	return d
}

func (f *deltaFixture) assertActiveUntouchedAndNoSlotSwitch(t *testing.T) {
	t.Helper()
	assert.Equal(t, sha256Hex(f.base), fileSha256(t, f.activePath),
		"the ACTIVE slot must never be written")
	assert.Nil(t, f.env.writeVars,
		"no boot variable (upgrade_available / mender_boot_part) may be written by a store")
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	require.NotZero(t, pid)
	err := syscall.Kill(pid, 0)
	assert.ErrorIs(t, err, syscall.ESRCH, "xdelta3 (pid %d) must be killed and reaped", pid)
}

// ---- apply path -------------------------------------------------------------

func TestDeltaRootfs_GoodDeltaAppliesAndHashMatches(t *testing.T) {
	f := newDeltaFixture(t)
	d := f.storer(t, f.headers(int64(len(f.target))))

	err := d.StoreUpdate(bytes.NewReader(f.patch), &sizeOnlyFileInfo{int64(len(f.patch))})
	require.NoError(t, err)
	require.NoError(t, d.FinishStoreUpdate())

	written, err := os.ReadFile(f.inactivePath)
	require.NoError(t, err)
	assert.Equal(t, sha256Hex(f.target), sha256Hex(written[:len(f.target)]),
		"inactive slot must hold exactly the target image")
	assert.Equal(t, bytes.Repeat([]byte{0xAA}, len(written)-len(f.target)),
		written[len(f.target):], "nothing may be written past the target size")
	f.assertActiveUntouchedAndNoSlotSwitch(t)
	assertProcessGone(t, d.lastPid)
	<-d.stdinDone

	// Only now, in the state machine's next step, does the slot switch.
	assert.Equal(t, DeltaRootfsPayloadType, d.GetType())
}

func TestDeltaRootfs_TruncatedPatchFails(t *testing.T) {
	f := newDeltaFixture(t)
	d := f.storer(t, f.headers(int64(len(f.target))))

	trunc := f.patch[:len(f.patch)/2]
	err := d.StoreUpdate(bytes.NewReader(trunc), &sizeOnlyFileInfo{int64(len(trunc))})
	require.Error(t, err)
	t.Logf("truncated patch error: %v", err)
	f.assertActiveUntouchedAndNoSlotSwitch(t)
	assertProcessGone(t, d.lastPid)
}

func TestDeltaRootfs_WrongBaseFails(t *testing.T) {
	f := newDeltaFixture(t)
	// The device CLAIMS the right base checksum (depends check passes) but the
	// active slot actually holds different bytes — e.g. a rw-mounted root
	// that drifted. The decode must not produce an installable image.
	drifted := append([]byte(nil), f.base...)
	copy(drifted[1024*1024:], randomBytes(9, 64*1024))
	require.NoError(t, os.WriteFile(f.activePath, drifted, 0600))

	d := f.storer(t, f.headers(int64(len(f.target))))
	err := d.StoreUpdate(bytes.NewReader(f.patch), &sizeOnlyFileInfo{int64(len(f.patch))})
	require.Error(t, err)
	t.Logf("wrong base error: %v", err)
	assert.Equal(t, sha256Hex(drifted), fileSha256(t, f.activePath), "active slot untouched")
	assert.Nil(t, f.env.writeVars, "no slot switch")
	assertProcessGone(t, d.lastPid)
}

func TestDeltaRootfs_HashGateRejectsWrongTargetChecksum(t *testing.T) {
	f := newDeltaFixture(t)
	h := f.headers(int64(len(f.target)))
	h.provides[RootfsChecksumKey] = sha256Hex([]byte("some other image"))
	d := f.storer(t, h)

	err := d.StoreUpdate(bytes.NewReader(f.patch), &sizeOnlyFileInfo{int64(len(f.patch))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match the signed target")
	f.assertActiveUntouchedAndNoSlotSwitch(t)
}

func TestDeltaRootfs_OversizeOutputAborted(t *testing.T) {
	f := newDeltaFixture(t)
	declared := int64(len(f.target)) - 64*1024 // signed size smaller than real output
	d := f.storer(t, f.headers(declared))

	err := d.StoreUpdate(bytes.NewReader(f.patch), &sizeOnlyFileInfo{int64(len(f.patch))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than the signed target size")
	written, rerr := os.ReadFile(f.inactivePath)
	require.NoError(t, rerr)
	assert.Equal(t, bytes.Repeat([]byte{0xAA}, len(written)-int(declared)),
		written[declared:], "not one byte may be written past the declared target size")
	f.assertActiveUntouchedAndNoSlotSwitch(t)
	assertProcessGone(t, d.lastPid)
}

func TestDeltaRootfs_PatchBombKilled(t *testing.T) {
	f := newDeltaFixture(t)
	xd := requireXdelta3(t)
	// A tiny patch that expands to 64 MiB of zeros: far bigger than the
	// 8 MiB inactive "partition" and the declared target.
	bombTarget := make([]byte, 64*1024*1024)
	bomb := makeXdelta3Patch(t, xd, f.dir, f.base, bombTarget)
	bombTarget = nil
	t.Logf("patch bomb: %d byte patch -> 64 MiB", len(bomb))

	d := f.storer(t, f.headers(int64(len(f.target))))
	start := time.Now()
	err := d.StoreUpdate(bytes.NewReader(bomb), &sizeOnlyFileInfo{int64(len(bomb))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than the signed target size")
	assert.Less(t, time.Since(start), 30*time.Second)
	fi, serr := os.Stat(f.inactivePath)
	require.NoError(t, serr)
	assert.Equal(t, f.partSize, fi.Size(), "inactive partition must not grow")
	f.assertActiveUntouchedAndNoSlotSwitch(t)
	assertProcessGone(t, d.lastPid)
}

func TestDeltaRootfs_TargetLargerThanInactivePartitionRejected(t *testing.T) {
	f := newDeltaFixture(t)
	d := f.storer(t, f.headers(f.partSize+1))

	err := d.StoreUpdate(bytes.NewReader(f.patch), &sizeOnlyFileInfo{int64(len(f.patch))})
	require.Error(t, err)
	assert.True(t, errors.Is(err, syscall.ENOSPC), "got %v", err)
	f.assertActiveUntouchedAndNoSlotSwitch(t)
	assertProcessGone(t, d.lastPid)
}

func TestDeltaRootfs_TimeoutKillsProcess(t *testing.T) {
	f := newDeltaFixture(t)
	f.factory.timeout = 1 * time.Second
	oldGrace := deltaStdinGrace
	deltaStdinGrace = 500 * time.Millisecond
	t.Cleanup(func() { deltaStdinGrace = oldGrace })

	d := f.storer(t, f.headers(int64(len(f.target))))
	// A patch stream that stalls forever (a hung download).
	pr, pw := io.Pipe()
	go func() { _, _ = pw.Write(f.patch[:16]) }()

	start := time.Now()
	err := d.StoreUpdate(pr, &sizeOnlyFileInfo{int64(len(f.patch))})
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.Less(t, elapsed, 15*time.Second)
	assertProcessGone(t, d.lastPid)
	f.assertActiveUntouchedAndNoSlotSwitch(t)

	// The feeder goroutine is parked in the stalled upstream read; closing
	// the upstream (as the state machine does on failure) must end it.
	pw.CloseWithError(errors.New("download aborted"))
	select {
	case <-d.stdinDone:
	case <-time.After(5 * time.Second):
		t.Fatal("patch feeder goroutine leaked after the upstream stream was closed")
	}
}

func TestDeltaRootfs_CancelKillsProcess(t *testing.T) {
	f := newDeltaFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.factory.baseCtx = ctx
	d := f.storer(t, f.headers(int64(len(f.target))))

	oldGrace := deltaStdinGrace
	deltaStdinGrace = 500 * time.Millisecond
	t.Cleanup(func() { deltaStdinGrace = oldGrace })

	pr, pw := io.Pipe()
	time.AfterFunc(300*time.Millisecond, cancel)
	err := d.StoreUpdate(pr, &sizeOnlyFileInfo{int64(len(f.patch))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
	assertProcessGone(t, d.lastPid)
	f.assertActiveUntouchedAndNoSlotSwitch(t)

	pw.CloseWithError(errors.New("download aborted"))
	select {
	case <-d.stdinDone:
	case <-time.After(5 * time.Second):
		t.Fatal("patch feeder goroutine leaked after the upstream stream was closed")
	}
}

// ---- Initialize: validation before any byte is read -------------------------

func TestDeltaRootfs_InitializeRejects(t *testing.T) {
	f := newDeltaFixture(t)
	good := func() *fakeDeltaHeaders { return f.headers(int64(len(f.target))) }
	ut := DeltaRootfsPayloadType

	cases := map[string]struct {
		mutate func(h *fakeDeltaHeaders, fac *DeltaRootfsFactory)
		want   string
	}{
		"depends base mismatch": {
			func(h *fakeDeltaHeaders, _ *DeltaRootfsFactory) {
				h.depends[RootfsChecksumKey] = sha256Hex([]byte("another base"))
			}, "does not match the running rootfs"},
		"device has no checksum provide": {
			func(_ *fakeDeltaHeaders, fac *DeltaRootfsFactory) {
				fac.provides = &fakeProvides{p: map[string]string{"artifact_name": "x"}}
			}, "running rootfs has no usable"},
		"depends missing": {
			func(h *fakeDeltaHeaders, _ *DeltaRootfsFactory) {
				delete(h.depends, RootfsChecksumKey)
			}, "must be a single checksum string"},
		"target checksum missing": {
			func(h *fakeDeltaHeaders, _ *DeltaRootfsFactory) {
				delete(h.provides, RootfsChecksumKey)
			}, "signed target provide"},
		"unknown format": {
			func(h *fakeDeltaHeaders, _ *DeltaRootfsFactory) {
				h.meta["delta_format"] = "bsdiff"
			}, "unsupported delta_format"},
		"missing target size": {
			func(h *fakeDeltaHeaders, _ *DeltaRootfsFactory) {
				delete(h.meta, "target_size")
			}, "target_size missing"},
		"fractional target size": {
			func(h *fakeDeltaHeaders, _ *DeltaRootfsFactory) {
				h.meta["target_size"] = 10.5
			}, "target_size invalid"},
		"rw root": {
			func(_ *fakeDeltaHeaders, fac *DeltaRootfsFactory) {
				mi := filepath.Join(f.dir, "mountinfo-rw")
				require.NoError(t, os.WriteFile(mi, []byte(rwRootMountInfo), 0600))
				fac.mountInfoPath = mi
			}, "mounted read-write"},
		"no xdelta3": {
			func(_ *fakeDeltaHeaders, fac *DeltaRootfsFactory) {
				fac.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
			}, "xdelta3 binary not found"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fac := *f.factory
			h := good()
			c.mutate(h, &fac)
			us, err := fac.NewUpdateStorer(&ut, 0)
			require.NoError(t, err)
			err = us.Initialize(nil, nil, h)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
			// An uninitialized storer refuses to store anything.
			assert.Error(t, us.PrepareStoreUpdate())
			assert.Error(t, us.StoreUpdate(bytes.NewReader(f.patch), &sizeOnlyFileInfo{1}))
		})
	}
	written, err := os.ReadFile(f.inactivePath)
	require.NoError(t, err)
	assert.Equal(t, bytes.Repeat([]byte{0xAA}, int(f.partSize)), written,
		"a rejected delta must not write a single byte to the inactive slot")
	f.assertActiveUntouchedAndNoSlotSwitch(t)
}

// ---- capability gating -------------------------------------------------------

func TestDeltaCapabilityGating(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0600))
		return p
	}
	found := func(string) (string, error) { return "/usr/bin/xdelta3", nil }
	missing := func(string) (string, error) { return "", exec.ErrNotFound }

	cases := []struct {
		name      string
		lookPath  func(string) (string, error)
		mountinfo string
		capable   bool
	}{
		{"ro root + binary", found, write("ro", roRootMountInfo), true},
		{"rw root + binary", found, write("rw", rwRootMountInfo), false},
		{"ro root, no binary", missing, write("ro2", roRootMountInfo), false},
		{"rw root, no binary", missing, write("rw2", rwRootMountInfo), false},
		{"ro mount over rw superblock", found, write("bind",
			"22 1 179:2 / / ro,relatime shared:1 - ext4 /dev/mmcblk0p2 rw\n"), false},
		{"rw overmount on ro root (last wins)", found, write("over",
			roRootMountInfo+"40 22 0:40 / / rw,relatime - overlay overlay rw,lowerdir=/\n"), false},
		{"ro overmount on rw root (last wins)", found, write("over2",
			rwRootMountInfo+"40 22 179:3 / / ro,relatime - squashfs /dev/mmcblk0p3 ro\n"), true},
		{"optional fields present", found, write("opt",
			"22 1 179:2 / / ro,noatime shared:1 master:2 - ext4 /dev/sda2 ro,data=ordered\n"), true},
		{"no root line", found, write("noroot", "23 22 0:5 / /proc rw - proc proc rw\n"), false},
		{"unreadable mountinfo", found, filepath.Join(dir, "does-not-exist"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fac := &DeltaRootfsFactory{lookPath: c.lookPath, mountInfoPath: c.mountinfo}
			path, reason := fac.Capability()
			assert.Equal(t, c.capable, reason == "", "reason=%q", reason)
			assert.Equal(t, c.capable, path != "")

			in := map[string]string{RootfsChecksumKey: "abc", DeltaFormatsProvideKey: "stale"}
			out := fac.AddDeltaCapabilityProvides(in)
			assert.Equal(t, "abc", out[RootfsChecksumKey], "existing provides preserved")
			v, ok := out[DeltaFormatsProvideKey]
			assert.Equal(t, c.capable, ok)
			if c.capable {
				assert.Equal(t, "xdelta3", v)
			}
			assert.Equal(t, "stale", in[DeltaFormatsProvideKey], "input map not mutated")
		})
	}

	var nilFac *DeltaRootfsFactory
	out := nilFac.AddDeltaCapabilityProvides(map[string]string{"artifact_name": "a"})
	assert.NotContains(t, out, DeltaFormatsProvideKey)
	assert.Nil(t, NewDeltaRootfsFactory(nil, nil, 0), "no dual rootfs -> no delta support")
}

// The real host: today's dev machines and boards mount / read-write, so the
// production capability check must say "no" here unless / really is ro.
func TestDeltaCapability_RealHost(t *testing.T) {
	ro, err := rootMountedReadOnly(selfMountInfoPath)
	if err != nil {
		t.Skipf("cannot read %s: %v", selfMountInfoPath, err)
	}
	_, reason := deltaCapability(exec.LookPath, selfMountInfoPath)
	if !ro {
		assert.NotEmpty(t, reason, "a rw root must never advertise delta support")
	}
	t.Logf("real host: root ro=%v, capability reason=%q", ro, reason)
}

// ---- end to end through the real signed artifact reader ---------------------

func writeDeltaArtifact(t *testing.T, f *deltaFixture, patch []byte, baseSum string,
	targetSize int64) io.ReadCloser {
	t.Helper()
	pp := filepath.Join(f.dir, "rootfs.vcdiff")
	require.NoError(t, os.WriteFile(pp, patch, 0600))

	s, err := artifact.NewPKISigner([]byte(PrivateRSAKey))
	require.NoError(t, err)
	buf := bytes.NewBuffer(nil)
	aw := awriter.NewWriterSigned(buf, artifact.NewCompressorGzip(), s)

	u := handlers.NewModuleImage(DeltaRootfsPayloadType)
	require.NoError(t, u.SetUpdateFiles([]*handlers.DataFile{{Name: pp}}))
	ut := DeltaRootfsPayloadType
	require.NoError(t, aw.WriteArtifact(&awriter.WriteArtifactArgs{
		Format:   "mender",
		Version:  3,
		Devices:  []string{"vexpress-qemu"},
		Name:     "release-2",
		Updates:  &awriter.Updates{Updates: []handlers.Composer{u}},
		Provides: &artifact.ArtifactProvides{ArtifactName: "release-2"},
		Depends:  &artifact.ArtifactDepends{CompatibleDevices: []string{"vexpress-qemu"}},
		TypeInfoV3: &artifact.TypeInfoV3{
			Type:                   &ut,
			ArtifactProvides:       artifact.TypeInfoProvides{RootfsChecksumKey: sha256Hex(f.target)},
			ArtifactDepends:        artifact.TypeInfoDepends{RootfsChecksumKey: baseSum},
			ClearsArtifactProvides: []string{"rootfs-image.*"},
		},
		MetaData: map[string]interface{}{"delta_format": "xdelta3", "target_size": targetSize},
	}))
	return &rc{buf}
}

func TestDeltaRootfs_InstallSignedArtifactEndToEnd(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	f := newDeltaFixture(t)
	inst := &AllModules{DualRootfs: f.dev, DeltaRootfs: f.factory}

	art := writeDeltaArtifact(t, f, f.patch, sha256Hex(f.base), int64(len(f.target)))
	installers, err := Install(art, "vexpress-qemu", testVerificationKeys, "", inst)
	require.NoError(t, err)
	require.Len(t, installers, 1)
	assert.Equal(t, DeltaRootfsPayloadType, installers[0].GetType())

	written, err := os.ReadFile(f.inactivePath)
	require.NoError(t, err)
	assert.Equal(t, sha256Hex(f.target), sha256Hex(written[:len(f.target)]))
	f.assertActiveUntouchedAndNoSlotSwitch(t)

	// The slot switch only happens in InstallUpdate, delegated unchanged to
	// the dual-rootfs device. (Commander is nil here, so stop short of
	// switch-boot-slot.sh by checking only the env write it does first.)
	assert.Equal(t, f.dev, installers[0].(*deltaRootfsInstaller).DualRootfsDevice)
}

func TestDeltaRootfs_InstallDependsMismatchRejectedBeforeAnyWrite(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	f := newDeltaFixture(t)
	inst := &AllModules{DualRootfs: f.dev, DeltaRootfs: f.factory}

	art := writeDeltaArtifact(t, f, f.patch, sha256Hex([]byte("not this device's base")),
		int64(len(f.target)))
	_, err := Install(art, "vexpress-qemu", testVerificationKeys, "", inst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match the running rootfs")

	written, rerr := os.ReadFile(f.inactivePath)
	require.NoError(t, rerr)
	assert.Equal(t, bytes.Repeat([]byte{0xAA}, int(f.partSize)), written,
		"depends mismatch must be rejected before any write to the inactive slot")
	f.assertActiveUntouchedAndNoSlotSwitch(t)
}

func TestDeltaRootfs_NotRegisteredWithoutFactory(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	f := newDeltaFixture(t)
	art := writeDeltaArtifact(t, f, f.patch, sha256Hex(f.base), int64(len(f.target)))
	_, err := Install(art, "vexpress-qemu", testVerificationKeys, "",
		&AllModules{DualRootfs: f.dev})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
	f.assertActiveUntouchedAndNoSlotSwitch(t)
}

func TestCreateInstallersFromList_DeltaResumesOnDualRootfs(t *testing.T) {
	dev := &dualRootfsDeviceImpl{partitions: &partitions{}}
	inst := &AllModules{
		DualRootfs:  dev,
		DeltaRootfs: NewDeltaRootfsFactory(dev, &fakeProvides{}, 0),
		Modules:     NewModuleInstallerFactory(t.TempDir(), t.TempDir(), nil, nil, 0),
	}
	list, err := CreateInstallersFromList(inst, []string{DeltaRootfsPayloadType})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, DeltaRootfsPayloadType, list[0].GetType())
	d, ok := list[0].(*deltaRootfsInstaller)
	require.True(t, ok)
	assert.Equal(t, dev, d.DualRootfsDevice, "post-reboot states run on the dual-rootfs device")
	// A resumed (uninitialized) delta installer can never store data.
	assert.Error(t, d.StoreUpdate(bytes.NewReader(nil), &sizeOnlyFileInfo{0}))

	// Without a dual rootfs it degrades to a stub, like rootfs-image.
	list, err = CreateInstallersFromList(&AllModules{
		Modules: NewModuleInstallerFactory(t.TempDir(), t.TempDir(), nil, nil, 0),
	}, []string{DeltaRootfsPayloadType})
	require.NoError(t, err)
	require.Len(t, list, 1)
	_, isStub := list[0].(*StubInstaller)
	assert.True(t, isStub)
}

// ---- Fable review F7: bounded decode window + base pre-hash ------------------

func TestXdelta3DecodeArgs_BoundedSourceWindow(t *testing.T) {
	args := xdelta3DecodeArgs("/dev/mmcblk0p2")
	assert.Equal(t, []string{"-d", "-c", "-D", "-R", "-B", "16777216", "-s", "/dev/mmcblk0p2"}, args)
	assert.Equal(t, 16*1024*1024, deltaDecodeSourceWindow)
}

// A patch encoded with the generator's 64 MiB window, whose copies reach
// across more than 16 MiB of source, must still decode with -B 16 MiB.
func TestDeltaRootfs_SmallDecodeWindowDecodesLargeWindowPatch(t *testing.T) {
	f := newDeltaFixture(t)
	xd := requireXdelta3(t)

	const mib = 1024 * 1024
	base := randomBytes(21, 40*mib)
	// Rotate: the target starts with the source's LAST 10 MiB, then the first
	// 30 MiB, with a few edits — copies span the whole 40 MiB source.
	target := append(append([]byte(nil), base[30*mib:]...), base[:30*mib]...)
	copy(target[5*mib:], randomBytes(22, 4096))
	copy(target[33*mib:], randomBytes(23, 4096))
	target = append(target, randomBytes(24, 777)...)

	pbase := filepath.Join(f.dir, "enc64-base")
	ptarget := filepath.Join(f.dir, "enc64-target")
	ppatch := filepath.Join(f.dir, "enc64-patch")
	require.NoError(t, os.WriteFile(pbase, base, 0600))
	require.NoError(t, os.WriteFile(ptarget, target, 0600))
	out, err := exec.Command(xd, "-e", "-f", "-9", "-S", "none", "-B", "67108864",
		"-s", pbase, ptarget, ppatch).CombinedOutput()
	require.NoError(t, err, "xdelta3 encode: %s", out)
	patch, err := os.ReadFile(ppatch)
	require.NoError(t, err)
	t.Logf("64M-window patch: %d bytes for a %d byte target", len(patch), len(target))
	require.Less(t, len(patch), len(target)/10)

	require.NoError(t, os.WriteFile(f.activePath, base, 0600))
	require.NoError(t, os.WriteFile(f.inactivePath, bytes.Repeat([]byte{0xAA}, 48*mib), 0600))
	f.factory.provides = &fakeProvides{p: map[string]string{RootfsChecksumKey: sha256Hex(base)}}

	h := &fakeDeltaHeaders{
		meta: map[string]interface{}{
			"delta_format":  "xdelta3",
			"target_size":   float64(len(target)),
			"base_size":     float64(len(base)),
			"base_checksum": sha256Hex(base),
		},
		provides: artifact.TypeInfoProvides{RootfsChecksumKey: sha256Hex(target)},
		depends:  artifact.TypeInfoDepends{RootfsChecksumKey: sha256Hex(base)},
	}
	d := f.storer(t, h)
	require.NoError(t, d.StoreUpdate(bytes.NewReader(patch), &sizeOnlyFileInfo{int64(len(patch))}))
	written, err := os.ReadFile(f.inactivePath)
	require.NoError(t, err)
	assert.Equal(t, sha256Hex(target), sha256Hex(written[:len(target)]))
	assert.Equal(t, sha256Hex(base), fileSha256(t, f.activePath), "active untouched")
	assert.Nil(t, f.env.writeVars)
}

func (f *deltaFixture) headersWithBase(targetSize int64) *fakeDeltaHeaders {
	h := f.headers(targetSize)
	h.meta["base_size"] = float64(len(f.base))
	h.meta["base_checksum"] = sha256Hex(f.base)
	return h
}

func TestDeltaRootfs_BasePreHashPassesOnLargerPartition(t *testing.T) {
	f := newDeltaFixture(t)
	// Real partitions are larger than the image: trailing bytes must not
	// affect the base check.
	require.NoError(t, os.WriteFile(f.activePath,
		append(append([]byte(nil), f.base...), randomBytes(31, 123457)...), 0600))
	d := f.storer(t, f.headersWithBase(int64(len(f.target))))

	require.NoError(t, d.StoreUpdate(bytes.NewReader(f.patch), &sizeOnlyFileInfo{int64(len(f.patch))}))
	written, err := os.ReadFile(f.inactivePath)
	require.NoError(t, err)
	assert.Equal(t, sha256Hex(f.target), sha256Hex(written[:len(f.target)]))
	assert.Nil(t, f.env.writeVars)
}

func TestDeltaRootfs_BasePreHashDriftDetectedBeforeAnyWrite(t *testing.T) {
	f := newDeltaFixture(t)
	drifted := append([]byte(nil), f.base...)
	drifted[len(drifted)-1] ^= 0xFF // a single flipped byte at the very end
	require.NoError(t, os.WriteFile(f.activePath, drifted, 0600))
	d := f.storer(t, f.headersWithBase(int64(len(f.target))))

	err := d.StoreUpdate(bytes.NewReader(f.patch), &sizeOnlyFileInfo{int64(len(f.patch))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "drifted from the delta base")
	assert.Zero(t, d.lastPid, "xdelta3 must never be started on a drifted base")
	written, rerr := os.ReadFile(f.inactivePath)
	require.NoError(t, rerr)
	assert.Equal(t, bytes.Repeat([]byte{0xAA}, int(f.partSize)), written,
		"drift must be detected before a single byte is written")
	assert.Nil(t, f.env.writeVars, "no upgrade_available / slot switch")
	assert.Equal(t, sha256Hex(drifted), fileSha256(t, f.activePath))
}

func TestDeltaRootfs_BasePreHashActiveShorterThanBase(t *testing.T) {
	f := newDeltaFixture(t)
	require.NoError(t, os.WriteFile(f.activePath, f.base[:len(f.base)/2], 0600))
	d := f.storer(t, f.headersWithBase(int64(len(f.target))))

	err := d.StoreUpdate(bytes.NewReader(f.patch), &sizeOnlyFileInfo{int64(len(f.patch))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shorter than the delta base")
	assert.Zero(t, d.lastPid)
	assert.Nil(t, f.env.writeVars)
}

func TestDeltaRootfs_InitializeRejectsBadBaseMeta(t *testing.T) {
	f := newDeltaFixture(t)
	ut := DeltaRootfsPayloadType
	for name, c := range map[string]struct {
		mutate func(h *fakeDeltaHeaders)
		want   string
	}{
		"base_checksum contradicts depends": {func(h *fakeDeltaHeaders) {
			h.meta["base_checksum"] = sha256Hex([]byte("x"))
		}, "contradicts depends"},
		"base_checksum malformed": {func(h *fakeDeltaHeaders) {
			h.meta["base_checksum"] = "nope"
		}, "base_checksum"},
		"base_size zero": {func(h *fakeDeltaHeaders) {
			h.meta["base_size"] = float64(0)
		}, "base_size invalid"},
		"base_size not a number": {func(h *fakeDeltaHeaders) {
			h.meta["base_size"] = "big"
		}, "base_size missing or not a number"},
	} {
		t.Run(name, func(t *testing.T) {
			h := f.headersWithBase(int64(len(f.target)))
			c.mutate(h)
			us, err := f.factory.NewUpdateStorer(&ut, 0)
			require.NoError(t, err)
			err = us.Initialize(nil, nil, h)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}
