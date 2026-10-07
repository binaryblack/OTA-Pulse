package client

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wireServer is a minimal device-API server that serves the native
// (/api/devices/v1/otapulse/...) route set, the legacy set, or both, and
// records every request. A route set it does not serve answers 404, exactly
// like a real server that lacks it.
type wireServer struct {
	*httptest.Server

	mu     sync.Mutex
	hits   []string
	native bool
	legacy bool
	// nativeStatus, when non-zero, is the status every native route answers
	// (e.g. 401 or 500) instead of serving normally.
	nativeStatus int
	// nativePostStatus, when non-zero, is the status native POST routes
	// answer (e.g. 405 for a GET-only native next route).
	nativePostStatus int
	// consumed counts how many update checks a route actually served: the
	// stand-in for "a deployment was handed out/burned".
	consumed int
}

func newWireServer(t *testing.T, native, legacy bool) *wireServer {
	ws := &wireServer{native: native, legacy: legacy}
	ws.Server = startTestHTTPS(http.HandlerFunc(ws.handle), localhostCert, localhostKey)
	t.Cleanup(ws.Close)
	return ws
}

func (ws *wireServer) hitList() []string {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return append([]string(nil), ws.hits...)
}

func (ws *wireServer) reset() {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.hits = nil
}

func (ws *wireServer) handle(w http.ResponseWriter, r *http.Request) {
	_, _ = ioutil.ReadAll(r.Body)
	ws.mu.Lock()
	ws.hits = append(ws.hits, r.Method+" "+r.URL.Path)
	ws.mu.Unlock()

	isNative := strings.Contains(r.URL.Path, "/v1/otapulse/")
	if isNative && !ws.native || !isNative && !ws.legacy {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if isNative && ws.nativeStatus != 0 {
		w.WriteHeader(ws.nativeStatus)
		return
	}
	if isNative && r.Method == http.MethodPost && ws.nativePostStatus != 0 {
		w.WriteHeader(ws.nativePostStatus)
		return
	}

	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/auth/requests"),
		strings.HasSuffix(p, "/authentication/auth_requests"):
		_, _ = w.Write([]byte("jwt-token"))
	case strings.HasSuffix(p, "/inventory/attributes"),
		strings.HasSuffix(p, "/inventory/device/attributes"):
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(p, "/status"), strings.HasSuffix(p, "/log"):
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(p, "/control-map"),
		strings.HasSuffix(p, "/update_control_map"):
		_, _ = w.Write([]byte(`{"update_control_map":{"id":"11111111-1111-1111-1111-111111111111",` +
			`"priority":0,"states":{}}}`))
	case strings.HasSuffix(p, "/deployments/next"):
		ws.mu.Lock()
		ws.consumed++
		ws.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type fixedAuth struct{}

func (fixedAuth) MakeAuthRequest() (*AuthRequest, error) {
	return &AuthRequest{Data: []byte("body"), Token: "tok", Signature: []byte("sig")}, nil
}

func newWireAPI(t *testing.T) *ApiClient {
	resetWireChoice()
	t.Cleanup(resetWireChoice)
	ac, err := NewApiClient(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)
	return ac
}

func nativeHits(hits []string) (n, legacy int) {
	for _, h := range hits {
		if strings.Contains(h, "/v1/otapulse/") {
			n++
		} else {
			legacy++
		}
	}
	return
}

func TestWireAuthNativeFirst(t *testing.T) {
	ac := newWireAPI(t)
	ws := newWireServer(t, true, true)

	rsp, err := NewAuth().Request(ac, ws.URL, fixedAuth{})
	require.NoError(t, err)
	assert.Equal(t, "jwt-token", string(rsp))
	assert.Equal(t, []string{"POST /api/devices/v1/otapulse/auth/requests"}, ws.hitList())
}

func TestWireAuthLegacyOnlyServerFallsBackOn404(t *testing.T) {
	ac := newWireAPI(t)
	ws := newWireServer(t, false, true)

	rsp, err := NewAuth().Request(ac, ws.URL, fixedAuth{})
	require.NoError(t, err)
	assert.Equal(t, "jwt-token", string(rsp))
	assert.Equal(t, []string{
		"POST /api/devices/v1/otapulse/auth/requests",
		"POST /api/devices/v1/authentication/auth_requests",
	}, ws.hitList())
}

func TestWireAuth401DoesNotFallBack(t *testing.T) {
	ac := newWireAPI(t)
	ws := newWireServer(t, true, true)
	ws.nativeStatus = http.StatusUnauthorized

	_, err := NewAuth().Request(ac, ws.URL, fixedAuth{})
	require.Error(t, err)
	assert.Equal(t, []string{"POST /api/devices/v1/otapulse/auth/requests"}, ws.hitList(),
		"a 401 on the native route must never be retried on the legacy route")
}

func TestWire403And5xxDoNotFallBack(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError,
		http.StatusServiceUnavailable, http.StatusBadGateway} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			ac := newWireAPI(t)
			ws := newWireServer(t, true, true)
			ws.nativeStatus = code

			_, err := NewAuth().Request(ac, ws.URL, fixedAuth{})
			require.Error(t, err)
			assert.Equal(t, 1, len(ws.hitList()))

			ws.reset()
			_, err = NewUpdate().GetScheduledUpdate(ac, ws.URL, &CurrentUpdate{})
			require.Error(t, err)
			n, legacy := nativeHits(ws.hitList())
			assert.Equal(t, 1, n)
			assert.Equal(t, 0, legacy)
			assert.Equal(t, 0, ws.consumed)
		})
	}
}

func TestWireStickyLegacyAndReprobe(t *testing.T) {
	ac := newWireAPI(t)
	ws := newWireServer(t, false, true)

	// First poll: native 404 -> legacy.
	require.NoError(t, NewStatus().Report(ac, ws.URL,
		StatusReport{DeploymentID: "d1", Status: StatusInstalling}))
	assert.Equal(t, []string{
		"PUT /api/devices/v1/otapulse/deployments/d1/status",
		"PUT /api/devices/v1/deployments/device/deployments/d1/status",
	}, ws.hitList())

	// Sticky: the next calls (any endpoint) go straight to legacy.
	ws.reset()
	require.NoError(t, NewStatus().Report(ac, ws.URL,
		StatusReport{DeploymentID: "d1", Status: StatusSuccess}))
	require.NoError(t, NewLog().Upload(ac, ws.URL,
		LogData{DeploymentID: "d1", Messages: []byte(`{"messages":[]}`)}))
	assert.Equal(t, []string{
		"PUT /api/devices/v1/deployments/device/deployments/d1/status",
		"PUT /api/devices/v1/deployments/device/deployments/d1/log",
	}, ws.hitList())

	// After the re-probe interval the native routes are tried again; the
	// server has been upgraded in the meantime, so it now sticks to native.
	ws.reset()
	ws.native = true
	realNow := wireNow
	t.Cleanup(func() { wireNow = realNow })
	wireNow = func() time.Time { return realNow().Add(legacyReprobeInterval + time.Minute) }
	require.NoError(t, NewStatus().Report(ac, ws.URL,
		StatusReport{DeploymentID: "d1", Status: StatusSuccess}))
	assert.Equal(t, []string{"PUT /api/devices/v1/otapulse/deployments/d1/status"}, ws.hitList())
	ws.reset()
	require.NoError(t, NewStatus().Report(ac, ws.URL,
		StatusReport{DeploymentID: "d1", Status: StatusSuccess}))
	assert.Equal(t, []string{"PUT /api/devices/v1/otapulse/deployments/d1/status"}, ws.hitList())
}

func TestWireStickyNativeSurvivesAndIsPerServer(t *testing.T) {
	ac := newWireAPI(t)
	nativeSrv := newWireServer(t, true, true)
	legacySrv := newWireServer(t, false, true)

	require.NoError(t, NewStatus().Report(ac, nativeSrv.URL,
		StatusReport{DeploymentID: "d1", Status: StatusInstalling}))
	require.NoError(t, NewStatus().Report(ac, legacySrv.URL,
		StatusReport{DeploymentID: "d1", Status: StatusInstalling}))

	// Choices are independent per server URL.
	assert.True(t, preferNativeWire(nativeSrv.URL))
	assert.False(t, preferNativeWire(legacySrv.URL))

	nativeSrv.reset()
	legacySrv.reset()
	require.NoError(t, NewStatus().Report(ac, nativeSrv.URL,
		StatusReport{DeploymentID: "d1", Status: StatusSuccess}))
	require.NoError(t, NewStatus().Report(ac, legacySrv.URL,
		StatusReport{DeploymentID: "d1", Status: StatusSuccess}))
	assert.Equal(t, 1, len(nativeSrv.hitList()))
	assert.Equal(t, 1, len(legacySrv.hitList()))
}

func TestWireUpdateCheckLegacyServerBurnsNothing(t *testing.T) {
	ac := newWireAPI(t)
	ws := newWireServer(t, false, true)

	_, err := NewUpdate().GetScheduledUpdate(ac, ws.URL,
		&CurrentUpdate{Artifact: "a1", DeviceType: "qemu"})
	assert.Equal(t, ErrNoDeploymentAvailable, causeOf(err))
	// Only the legacy route actually served (= consumed) the check. The two
	// native variants answered 404 without side effects, then the legacy
	// POST v2 served it and the chain stopped.
	assert.Equal(t, 1, ws.consumed)
	assert.Equal(t, []string{
		"POST /api/devices/v1/otapulse/deployments/next",
		"GET /api/devices/v1/otapulse/deployments/next",
		"POST /api/devices/v2/deployments/device/deployments/next",
	}, ws.hitList())

	// Sticky: the next poll starts on legacy and costs one request.
	ws.reset()
	_, err = NewUpdate().GetScheduledUpdate(ac, ws.URL,
		&CurrentUpdate{Artifact: "a1", DeviceType: "qemu"})
	assert.Equal(t, ErrNoDeploymentAvailable, causeOf(err))
	assert.Equal(t, []string{"POST /api/devices/v2/deployments/device/deployments/next"},
		ws.hitList())
	assert.Equal(t, 2, ws.consumed)
}

func TestWireUpdateCheckNativeServer(t *testing.T) {
	ac := newWireAPI(t)
	ws := newWireServer(t, true, true)

	_, err := NewUpdate().GetScheduledUpdate(ac, ws.URL, &CurrentUpdate{Artifact: "a1"})
	assert.Equal(t, ErrNoDeploymentAvailable, causeOf(err))
	assert.Equal(t, []string{"POST /api/devices/v1/otapulse/deployments/next"}, ws.hitList())
	assert.Equal(t, 1, ws.consumed)
}

func TestWireUpdateCheckNativeGetOnly405ThenGet(t *testing.T) {
	ac := newWireAPI(t)
	ws := newWireServer(t, true, true)
	ws.nativePostStatus = http.StatusMethodNotAllowed

	_, err := NewUpdate().GetScheduledUpdate(ac, ws.URL,
		&CurrentUpdate{Artifact: "a1", DeviceType: "qemu"})
	assert.Equal(t, ErrNoDeploymentAvailable, causeOf(err))
	assert.Equal(t, []string{
		"POST /api/devices/v1/otapulse/deployments/next",
		"GET /api/devices/v1/otapulse/deployments/next",
	}, ws.hitList())
	assert.Equal(t, 1, ws.consumed)
	assert.True(t, preferNativeWire(ws.URL))
}

func TestWireUpdateCheck401DoesNotFallBack(t *testing.T) {
	ac := newWireAPI(t)
	ws := newWireServer(t, true, true)
	ws.nativeStatus = http.StatusUnauthorized

	_, err := NewUpdate().GetScheduledUpdate(ac, ws.URL, &CurrentUpdate{})
	assert.Equal(t, ErrNotAuthorized, causeOf(err))
	assert.Equal(t, []string{"POST /api/devices/v1/otapulse/deployments/next"}, ws.hitList())
	assert.Equal(t, 0, ws.consumed)
}

func TestWireInventoryNativePatchOnlyAndLegacyFallback(t *testing.T) {
	ac := newWireAPI(t)

	// Native route that only allows PATCH: PUT -> 405 -> PATCH, still native.
	var hits []string
	var mu sync.Mutex
	ts := startTestHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method != http.MethodPatch {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), localhostCert, localhostKey)
	defer ts.Close()

	require.NoError(t, NewInventory().Submit(ac, ts.URL, InventoryData{{"foo", "bar"}}))
	assert.Equal(t, []string{
		"PUT /api/devices/v1/otapulse/inventory/attributes",
		"PATCH /api/devices/v1/otapulse/inventory/attributes",
	}, hits)

	// Legacy-only server.
	ws := newWireServer(t, false, true)
	require.NoError(t, NewInventory().Submit(ac, ws.URL, InventoryData{{"foo", "bar"}}))
	assert.Equal(t, []string{
		"PUT /api/devices/v1/otapulse/inventory/attributes",
		"PUT /api/devices/v1/inventory/device/attributes",
	}, ws.hitList())
}

func TestWireControlMap(t *testing.T) {
	ac := newWireAPI(t)
	id := "11111111-1111-1111-1111-111111111111"

	legacy := newWireServer(t, false, true)
	cm, err := GetUpdateControlMap(ac, legacy.URL, id)
	require.NoError(t, err)
	assert.NotNil(t, cm)
	assert.Equal(t, []string{
		"GET /api/devices/v1/otapulse/deployments/" + id + "/control-map",
		"GET /api/devices/v2/deployments/device/deployments/" + id + "/update_control_map",
	}, legacy.hitList())

	native := newWireServer(t, true, true)
	cm, err = GetUpdateControlMap(ac, native.URL, id)
	require.NoError(t, err)
	assert.NotNil(t, cm)
	assert.Equal(t, []string{
		"GET /api/devices/v1/otapulse/deployments/" + id + "/control-map",
	}, native.hitList())

	// Neither route set serves it: both 404 -> ErrNoDeploymentAvailable
	// (the pre-existing semantics of a 404 here).
	none := newWireServer(t, false, false)
	_, err = GetUpdateControlMap(ac, none.URL, id)
	assert.Equal(t, ErrNoDeploymentAvailable, causeOf(err))
}

func TestWireStatusGoneStillAborts(t *testing.T) {
	// A deployment that is gone answers 404 on BOTH route sets: the agent
	// must still classify that as an abort (BUG-283), after trying both.
	ac := newWireAPI(t)
	ws := newWireServer(t, false, false)
	err := NewStatus().Report(ac, ws.URL,
		StatusReport{DeploymentID: "gone", Status: StatusSuccess})
	require.Error(t, err)
	assert.Equal(t, ErrDeploymentAborted, causeOf(err))
	assert.Equal(t, 2, len(ws.hitList()))
}

func causeOf(err error) error {
	type causer interface{ Cause() error }
	for err != nil {
		c, ok := err.(causer)
		if !ok {
			return err
		}
		err = c.Cause()
	}
	return err
}
