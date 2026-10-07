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

// Delta rootfs updates (TODO-054 / TASK-S97-005).
//
// Payload type "rootfs-image-delta" is a standard Mender v3 artifact whose
// single payload file is an xdelta3 (VCDIFF) patch from the rootfs image
// currently running in the ACTIVE slot (the "base") to a new rootfs image
// (the "target"). The artifact contract, which the backend generator must
// honour, is:
//
//   - type-info artifact_depends: rootfs-image.checksum = sha256 of the BASE
//     rootfs image (exactly what the base's own full artifact provided, and
//     what this device therefore has persisted in its provides).
//   - type-info artifact_provides: identical to the full target artifact,
//     including rootfs-image.checksum = sha256 of the TARGET rootfs image.
//   - payload meta-data: {"delta_format": "xdelta3", "target_size": <bytes>,
//     "base_checksum": <hex>, "base_size": <bytes>}. base_checksum/base_size
//     are optional; when base_size is present the first base_size bytes of
//     the active slot are hashed and compared to the signed base checksum
//     before anything is decoded or written (cheap source-drift detection).
//
// The whole header (depends, provides, meta-data) is covered by the artifact
// signature that ReadHeaders already verifies, so every value used below is
// signed.
//
// Apply path: `xdelta3 -d -s <ACTIVE partition>` reads the patch from stdin
// and its decoded output is streamed straight into the EXISTING dual-rootfs
// StoreUpdate path (inactive slot only, sha256 tee + post-write read-back
// verification). The output is bounded to target_size bytes (and
// blockdevice.Open refuses a target_size larger than the inactive partition),
// so a patch bomb is cut off and xdelta3 killed instead of overrunning the
// slot. The final gate compares the sha256 of the written bytes to the signed
// target rootfs-image.checksum; a mismatch fails the store BEFORE
// InstallUpdate, i.e. before upgrade_available is set or any slot switch is
// attempted. The active slot is only ever opened read-only, by xdelta3.
//
// Dormant by default: the device only advertises
// rootfs-image.delta.formats=xdelta3 (and only accepts a delta payload) when
// an xdelta3 binary is present AND the root filesystem is mounted read-only.
// A read-write root drifts from the base checksum the moment it is mounted,
// so a delta could never verify on it. Every board today mounts / rw, so
// this path stays inert until an image ships read-only rootfs + xdelta3
// (TODO-115).

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mendersoftware/mender-artifact/artifact"
	"github.com/mendersoftware/mender-artifact/handlers"
)

const (
	// DeltaRootfsPayloadType is the Mender payload type of a delta rootfs
	// artifact.
	DeltaRootfsPayloadType = "rootfs-image-delta"
	// DeltaFormatsProvideKey is the device provide advertising which delta
	// formats this agent can apply.
	DeltaFormatsProvideKey = "rootfs-image.delta.formats"
	// DeltaFormatXdelta3 is the only supported delta format.
	DeltaFormatXdelta3 = "xdelta3"
	// RootfsChecksumKey is the provide/depend carrying a rootfs image's sha256.
	RootfsChecksumKey = "rootfs-image.checksum"

	deltaMetaFormatKey     = "delta_format"
	deltaMetaTargetSizeKey = "target_size"
	// Optional (backend contract, Fable review F7): size of the BASE image
	// and its checksum, enabling a cheap source-drift pre-check.
	deltaMetaBaseSizeKey     = "base_size"
	deltaMetaBaseChecksumKey = "base_checksum"

	// xdelta3 decode source window (-B). Decoding works with a smaller window
	// than the encoder's (-B 64M at generation time); 16 MiB keeps xdelta3's
	// resident memory around ~30 MB on low-RAM boards.
	deltaDecodeSourceWindow = 16 * 1024 * 1024

	xdelta3BinaryName = "xdelta3"
	selfMountInfoPath = "/proc/self/mountinfo"

	// Bounded tail of xdelta3's stderr kept for error messages.
	deltaStderrTailBytes = 4096
)

var (
	// How long to wait for the patch-feeding goroutine and for xdelta3's
	// pipes after xdelta3 has exited or been killed. Variables so tests can
	// shorten them.
	deltaStdinGrace = 10 * time.Second
	deltaWaitDelay  = 5 * time.Second
)

// ProvidesGetter returns the device's persisted artifact provides
// (device.DeviceManager implements it).
type ProvidesGetter interface {
	GetProvides() (map[string]string, error)
}

// DeltaRootfsFactory produces update storers for rootfs-image-delta payloads.
// It must only be created when a DualRootfsDevice is configured.
type DeltaRootfsFactory struct {
	dual     DualRootfsDevice
	provides ProvidesGetter
	timeout  time.Duration

	// Injected for tests; production uses exec.LookPath and
	// /proc/self/mountinfo.
	lookPath      func(string) (string, error)
	mountInfoPath string
	// Parent context of every xdelta3 run; cancelling it kills a running
	// xdelta3.
	baseCtx context.Context
}

// NewDeltaRootfsFactory returns nil when dual is nil (no A/B rootfs, so no
// delta support). timeoutSecs <= 0 falls back to the module default (the
// same ModuleTimeoutSeconds knob update modules use).
func NewDeltaRootfsFactory(dual DualRootfsDevice, provides ProvidesGetter,
	timeoutSecs int) *DeltaRootfsFactory {
	if dual == nil {
		return nil
	}
	if timeoutSecs <= 0 {
		timeoutSecs = defaultModuleTimeoutSecs
	}
	return &DeltaRootfsFactory{
		dual:          dual,
		provides:      provides,
		timeout:       time.Duration(timeoutSecs) * time.Second,
		lookPath:      exec.LookPath,
		mountInfoPath: selfMountInfoPath,
		baseCtx:       context.Background(),
	}
}

// Capability reports whether this device can apply an xdelta3 rootfs delta:
// the xdelta3 binary must be found AND / must be mounted read-only. It
// returns the resolved xdelta3 path, or a human-readable reason why not.
func (f *DeltaRootfsFactory) Capability() (string, string) {
	if f == nil {
		return "", "no dual-rootfs device configured"
	}
	return deltaCapability(f.lookPath, f.mountInfoPath)
}

// AddDeltaCapabilityProvides returns a copy of provides with
// rootfs-image.delta.formats=xdelta3 added ONLY when the device is capable.
// When not capable, any (stale) advertisement is stripped so the backend
// never sees it. Safe on a nil factory.
func (f *DeltaRootfsFactory) AddDeltaCapabilityProvides(
	provides map[string]string) map[string]string {
	out := make(map[string]string, len(provides)+1)
	for k, v := range provides {
		out[k] = v
	}
	delete(out, DeltaFormatsProvideKey)
	if _, reason := f.Capability(); reason != "" {
		log.Debugf("Delta rootfs updates not advertised: %s", reason)
		return out
	}
	out[DeltaFormatsProvideKey] = DeltaFormatXdelta3
	return out
}

func deltaCapability(lookPath func(string) (string, error),
	mountInfoPath string) (string, string) {
	path, err := lookPath(xdelta3BinaryName)
	if err != nil || path == "" {
		return "", "xdelta3 binary not found"
	}
	ro, err := rootMountedReadOnly(mountInfoPath)
	if err != nil {
		return "", fmt.Sprintf("cannot determine root mount mode: %v", err)
	}
	if !ro {
		return "", "root filesystem is mounted read-write"
	}
	return path, ""
}

// rootMountedReadOnly parses a mountinfo file (proc(5)) and reports whether
// the topmost mount at "/" is read-only at BOTH the mount level and the
// superblock level. A ro bind/remount over a rw superblock is not enough:
// the same filesystem may still be written through another mount.
func rootMountedReadOnly(mountInfoPath string) (bool, error) {
	f, err := os.Open(mountInfoPath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	found := false
	ro := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// id parent maj:min root mountpoint options [optional...] - fstype source superopts
		if len(fields) < 10 || fields[4] != "/" {
			continue
		}
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+3 >= len(fields) {
			continue
		}
		// Later lines are mounted on top of earlier ones: last one wins.
		found = true
		ro = hasMountOption(fields[5], "ro") && hasMountOption(fields[sep+3], "ro")
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	if !found {
		return false, errors.Errorf("no mount for / found in %s", mountInfoPath)
	}
	return ro, nil
}

func hasMountOption(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

// NewUpdateStorer implements handlers.UpdateStorerProducer.
func (f *DeltaRootfsFactory) NewUpdateStorer(
	updateType *string,
	payloadNum int,
) (handlers.UpdateStorer, error) {
	if updateType == nil || *updateType != DeltaRootfsPayloadType {
		return nil, errors.Errorf("delta rootfs factory cannot handle payload type %v",
			updateType)
	}
	return f.newInstaller(), nil
}

func (f *DeltaRootfsFactory) newInstaller() *deltaRootfsInstaller {
	return &deltaRootfsInstaller{DualRootfsDevice: f.dual, factory: f}
}

// deltaRootfsInstaller applies one rootfs-image-delta payload. Everything
// after the store (InstallUpdate, reboot, commit, rollback, ...) is the
// dual-rootfs device's own logic, inherited unchanged through embedding.
type deltaRootfsInstaller struct {
	DualRootfsDevice
	factory *DeltaRootfsFactory

	// Set by Initialize from the signed headers.
	xdelta3Path    string
	targetSize     int64
	baseChecksum   string
	baseSize       int64 // 0 = unknown, pre-hash skipped
	targetChecksum string
	initialized    bool
	filesStored    int

	// Bookkeeping for the running xdelta3, exposed for tests.
	mu        sync.Mutex
	cancel    context.CancelFunc
	lastPid   int
	stdinDone chan struct{}
}

func (d *deltaRootfsInstaller) GetType() string {
	return DeltaRootfsPayloadType
}

// Initialize validates everything that can be validated from the signed
// headers, before a single payload byte is read: device capability, payload
// meta-data, the signed target checksum, and that the delta's base is the
// rootfs this device is actually running.
func (d *deltaRootfsInstaller) Initialize(artifactHeaders,
	artifactAugmentedHeaders artifact.HeaderInfoer,
	payloadHeaders handlers.ArtifactUpdateHeaders) error {

	path, reason := d.factory.Capability()
	if reason != "" {
		return errors.Errorf("refusing %s payload: device is not delta-capable (%s)",
			DeltaRootfsPayloadType, reason)
	}
	if payloadHeaders == nil {
		return errors.New("delta payload has no headers")
	}

	meta, err := payloadHeaders.GetUpdateMetaData()
	if err != nil {
		return errors.Wrap(err, "delta payload: cannot read meta-data")
	}
	format, _ := meta[deltaMetaFormatKey].(string)
	if format != DeltaFormatXdelta3 {
		return errors.Errorf("delta payload: unsupported %s %q (want %q)",
			deltaMetaFormatKey, format, DeltaFormatXdelta3)
	}
	size, err := parsePositiveSize(deltaMetaTargetSizeKey, meta[deltaMetaTargetSizeKey])
	if err != nil {
		return errors.Wrap(err, "delta payload")
	}
	var baseSize int64
	if v, present := meta[deltaMetaBaseSizeKey]; present {
		if baseSize, err = parsePositiveSize(deltaMetaBaseSizeKey, v); err != nil {
			return errors.Wrap(err, "delta payload")
		}
	}

	provides, err := payloadHeaders.GetUpdateProvides()
	if err != nil {
		return errors.Wrap(err, "delta payload: cannot read provides")
	}
	target, err := normaliseChecksum(provides[RootfsChecksumKey])
	if err != nil {
		return errors.Wrapf(err, "delta payload: signed target provide %s", RootfsChecksumKey)
	}

	depends, err := payloadHeaders.GetUpdateDepends()
	if err != nil {
		return errors.Wrap(err, "delta payload: cannot read depends")
	}
	baseRaw, ok := depends[RootfsChecksumKey].(string)
	if !ok {
		return errors.Errorf("delta payload: depends %s must be a single checksum string, got %v",
			RootfsChecksumKey, depends[RootfsChecksumKey])
	}
	base, err := normaliseChecksum(baseRaw)
	if err != nil {
		return errors.Wrapf(err, "delta payload: depends %s", RootfsChecksumKey)
	}
	if v, present := meta[deltaMetaBaseChecksumKey]; present {
		s, _ := v.(string)
		metaBase, err := normaliseChecksum(s)
		if err != nil {
			return errors.Wrapf(err, "delta payload: meta-data %s", deltaMetaBaseChecksumKey)
		}
		if metaBase != base {
			return errors.Errorf("delta payload: meta-data %s %s contradicts depends %s %s",
				deltaMetaBaseChecksumKey, metaBase, RootfsChecksumKey, base)
		}
	}
	// Defence in depth: app/state.go already rejects an unsatisfied
	// artifact_depends before StorePayloads, but this storer must never run
	// against a base it was not built for, whatever the caller did.
	if d.factory.provides == nil {
		return errors.New("delta payload: no device provides available to check the base against")
	}
	devProvides, err := d.factory.provides.GetProvides()
	if err != nil {
		return errors.Wrap(err, "delta payload: cannot load device provides")
	}
	current, err := normaliseChecksum(devProvides[RootfsChecksumKey])
	if err != nil {
		return errors.Wrapf(err, "delta payload: running rootfs has no usable %s", RootfsChecksumKey)
	}
	if current != base {
		return errors.Errorf("delta payload base %s=%s does not match the running rootfs (%s)",
			RootfsChecksumKey, base, current)
	}

	d.xdelta3Path = path
	d.targetSize = size
	d.baseChecksum = base
	d.baseSize = baseSize
	d.targetChecksum = target
	d.initialized = true
	return nil
}

func parsePositiveSize(key string, v interface{}) (int64, error) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case int:
		f = float64(n)
	case int64:
		f = float64(n)
	default:
		return 0, errors.Errorf("meta-data %s missing or not a number (%v)", key, v)
	}
	if f <= 0 || f != math.Trunc(f) || f > float64(math.MaxInt64/2) {
		return 0, errors.Errorf("meta-data %s invalid (%v)", key, v)
	}
	return int64(f), nil
}

func normaliseChecksum(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != sha256.Size*2 {
		return "", errors.Errorf("not a sha256 hex checksum: %q", s)
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", errors.Errorf("not a sha256 hex checksum: %q", s)
	}
	return s, nil
}

func (d *deltaRootfsInstaller) PrepareStoreUpdate() error {
	if !d.initialized {
		return errors.New("delta payload was not initialized")
	}
	return nil
}

func (d *deltaRootfsInstaller) FinishStoreUpdate() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
	return nil
}

// deltaFileInfo hands the dual-rootfs StoreUpdate the TARGET size, which is
// what it opens/bounds the inactive partition with and checks against.
type deltaFileInfo struct {
	name string
	size int64
}

func (fi *deltaFileInfo) Name() string       { return fi.name }
func (fi *deltaFileInfo) Size() int64        { return fi.size }
func (fi *deltaFileInfo) Mode() os.FileMode  { return 0 }
func (fi *deltaFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *deltaFileInfo) IsDir() bool        { return false }
func (fi *deltaFileInfo) Sys() interface{}   { return nil }

var errDeltaOutputOversize = errors.New("delta output exceeds the signed target size")

// boundedOutput fails as soon as the decoder produces more than max bytes.
// It reads through io.LimitReader(max+1) so it never pulls more than one
// byte beyond the bound.
type boundedOutput struct {
	r   io.Reader
	max int64
	n   int64
}

func newBoundedOutput(r io.Reader, max int64) *boundedOutput {
	return &boundedOutput{r: io.LimitReader(r, max+1), max: max}
}

func (b *boundedOutput) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.n > b.max {
		keep := n - int(b.n-b.max)
		if keep < 0 {
			keep = 0
		}
		return keep, errDeltaOutputOversize
	}
	return n, err
}

// tailBuffer keeps only the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// StoreUpdate decodes the patch against the active slot and writes the result
// to the inactive slot through the dual-rootfs StoreUpdate.
func (d *deltaRootfsInstaller) StoreUpdate(patch io.Reader, info os.FileInfo) error {
	if !d.initialized {
		return errors.New("delta payload was not initialized")
	}
	d.filesStored++
	if d.filesStored > 1 {
		return errors.New("delta payload must contain exactly one file")
	}

	active, err := d.GetActive()
	if err != nil {
		return errors.Wrap(err, "delta apply: cannot determine the active partition")
	}
	inactive, err := d.GetInactive()
	if err != nil {
		return errors.Wrap(err, "delta apply: cannot determine the inactive partition")
	}
	if active == "" || inactive == "" || sameFile(active, inactive) {
		return errors.Errorf("delta apply: refusing, active (%q) and inactive (%q) partitions are not distinct",
			active, inactive)
	}

	ctx, cancel := context.WithTimeout(d.factory.baseCtx, d.factory.timeout)
	defer cancel()

	// Cheap source-drift check BEFORE anything is written: the first
	// base_size bytes of the active slot must be exactly the signed base.
	if d.baseSize > 0 {
		if err := verifyDeltaSource(ctx, active, d.baseSize, d.baseChecksum); err != nil {
			return errors.Wrap(err, "delta apply refused before writing (no slot switch)")
		}
	}

	// Fixed argv (xdelta3DecodeArgs), no shell. A minimal environment keeps
	// XDELTA (xdelta3's default-options variable) and anything else in the
	// agent's environment out of it.
	cmd := exec.CommandContext(ctx, d.xdelta3Path, xdelta3DecodeArgs(active)...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = deltaWaitDelay
	stderr := &tailBuffer{limit: deltaStderrTailBytes}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errors.Wrap(err, "delta apply: stdin pipe")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return errors.Wrap(err, "delta apply: stdout pipe")
	}

	log.Infof("Applying %s payload: xdelta3 source %s (read-only) -> inactive %s, "+
		"target %d bytes, sha256 %s, timeout %s",
		DeltaRootfsPayloadType, active, inactive, d.targetSize, d.targetChecksum,
		d.factory.timeout)

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return errors.Wrap(err, "delta apply: failed to start xdelta3")
	}

	stdinDone := make(chan struct{})
	d.mu.Lock()
	d.cancel = cancel
	d.lastPid = cmd.Process.Pid
	d.stdinDone = stdinDone
	d.mu.Unlock()

	var stdinErr error
	go func() {
		defer close(stdinDone)
		_, stdinErr = io.Copy(stdin, patch)
		stdin.Close()
	}()

	hasher := sha256.New()
	out := io.TeeReader(newBoundedOutput(stdout, d.targetSize), hasher)
	storeErr := d.DualRootfsDevice.StoreUpdate(out, &deltaFileInfo{
		name: info.Name(),
		size: d.targetSize,
	})
	// Timeout / external cancellation, captured before our own cancel below.
	ctxErr := ctx.Err()
	if storeErr != nil {
		// Kill xdelta3 now; it may be blocked writing output nobody reads.
		cancel()
	}
	waitErr := cmd.Wait()
	if ctxErr == nil && storeErr == nil {
		// xdelta3 finished on its own; a deadline hitting during Wait still
		// means it was killed.
		ctxErr = ctx.Err()
	}

	// Never return while the patch-feeding goroutine may still be reading
	// the artifact stream. Once xdelta3 is gone its stdin write fails; the
	// only way it can stay alive is a blocked read of the upstream stream,
	// which the update state machine closes on failure.
	stdinFinished := true
	select {
	case <-stdinDone:
	case <-time.After(deltaStdinGrace):
		stdinFinished = false
	}

	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return errors.Errorf("delta apply timed out after %s; xdelta3 killed (inactive slot %s left unbootable, no slot switch)",
			d.factory.timeout, inactive)
	}
	if ctxErr != nil {
		return errors.Wrap(ctxErr, "delta apply cancelled; xdelta3 killed (no slot switch)")
	}
	if storeErr != nil {
		if errors.Is(storeErr, errDeltaOutputOversize) {
			return errors.Errorf("delta apply aborted: xdelta3 produced more than the signed target size of %d bytes (patch bomb?); xdelta3 killed, no slot switch",
				d.targetSize)
		}
		return errors.Wrapf(storeErr, "delta apply failed writing inactive slot %s (xdelta3: %s)",
			inactive, stderr.String())
	}
	if waitErr != nil {
		return errors.Wrapf(waitErr, "delta apply: xdelta3 failed (%s)", stderr.String())
	}
	if !stdinFinished {
		return errors.New("delta apply: patch stream did not finish after xdelta3 exited")
	}
	if stdinErr != nil {
		return errors.Wrap(stdinErr, "delta apply: feeding the patch to xdelta3 failed")
	}

	// Final gate: the bytes now on the inactive slot (already read back and
	// re-hashed by the dual-rootfs StoreUpdate) must be exactly the signed
	// target image. Failing here keeps InstallUpdate, and with it
	// upgrade_available and any slot switch, from ever running.
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != d.targetChecksum {
		return errors.Errorf("delta apply: written image sha256 %s does not match the signed target %s %s; refusing to install (no slot switch)",
			got, RootfsChecksumKey, d.targetChecksum)
	}
	log.Infof("Delta rootfs applied to %s: %d bytes, sha256 %s matches the signed target",
		inactive, d.targetSize, got)
	return nil
}

// xdelta3DecodeArgs: -d decode, -c to stdout, -D/-R never spawn external
// (de)compressors, -B bounded source window, -s the ACTIVE slot as the
// read-only source. Patch on stdin.
func xdelta3DecodeArgs(active string) []string {
	return []string{"-d", "-c", "-D", "-R",
		"-B", strconv.Itoa(deltaDecodeSourceWindow), "-s", active}
}

// ctxReader aborts a long read loop once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// verifyDeltaSource hashes the first size bytes of the active slot (opened
// read-only) and compares them with the signed base checksum.
func verifyDeltaSource(ctx context.Context, active string, size int64, want string) error {
	f, err := os.Open(active)
	if err != nil {
		return errors.Wrapf(err, "cannot open active slot %q for the base check", active)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyN(h, &ctxReader{ctx: ctx, r: f}, size)
	if err != nil {
		if ctx.Err() != nil {
			return errors.Wrap(ctx.Err(), "base check of the active slot interrupted")
		}
		return errors.Wrapf(err, "active slot %q shorter than the delta base (%d of %d bytes)",
			active, n, size)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return errors.Errorf("active slot %q has drifted from the delta base: first %d bytes sha256 %s, signed base %s %s",
			active, size, got, RootfsChecksumKey, want)
	}
	log.Infof("Delta base check passed: first %d bytes of %s match %s", size, active, want)
	return nil
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil && ra == rb {
		return true
	}
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	if os.SameFile(sa, sb) {
		return true
	}
	// Two different device nodes for the same block device.
	if sa.Mode()&os.ModeDevice != 0 && sb.Mode()&os.ModeDevice != 0 {
		ta, okA := sa.Sys().(*syscall.Stat_t)
		tb, okB := sb.Sys().(*syscall.Stat_t)
		return okA && okB && ta.Rdev == tb.Rdev
	}
	return false
}
