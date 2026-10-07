package client

// OTA-Pulse-native device wire (TODO-011 / TASK-S102-007).
//
// The device API is served under two route sets that are byte-identical
// aliases of one another:
//
//   - the OTA-Pulse-native set under  /api/devices/v1/otapulse/...
//   - the legacy (Mender-compatible)  /api/devices/v{1,2}/...
//
// The agent speaks the native set first and falls back to the legacy set
// ONLY when the server answers HTTP 404 for the native path WITHOUT the
// X-OTAPulse-Wire response header (a server that predates the native routes;
// a current backend sets that header on every native response, including its
// own legitimate 404s). Any other status — 401, 403, 5xx, and also
// every 2xx/4xx that the native route legitimately produced — is final and
// never triggers a fallback, so a fallback can never replay a request that
// the server already acted on.
//
// The choice is remembered per server URL ("sticky"): after a server is
// seen to serve the native routes the agent keeps preferring them; after it
// is seen to serve only the legacy routes the agent goes straight to the
// legacy paths (no 404 on every poll) and re-probes the native set once per
// legacyReprobeInterval so a server upgrade is noticed without an agent
// restart.

import (
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// wirePrefix is the path (below /api/devices/) of the native route set.
const wirePrefix = "/v1/otapulse"

// Native route paths, relative to apiPrefix.
const (
	wireAuthPath          = wirePrefix + "/auth/requests"
	wireNextPath          = wirePrefix + "/deployments/next"
	wireInventoryPath     = wirePrefix + "/inventory/attributes"
	wireStatusPathFmt     = wirePrefix + "/deployments/%s/status"
	wireLogPathFmt        = wirePrefix + "/deployments/%s/log"
	wireControlMapPathFmt = wirePrefix + "/deployments/%s/control-map"
)

// legacyReprobeInterval bounds how long a "legacy only" verdict is trusted
// before the native routes are tried again.
var legacyReprobeInterval = time.Hour

// wireNow is the clock used for the re-probe; replaced in tests.
var wireNow = time.Now

type wireMode int

const (
	wireUnknown wireMode = iota
	wireNative
	wireLegacy
)

type wireEntry struct {
	mode  wireMode
	since time.Time
}

var wireChoice = struct {
	sync.Mutex
	m map[string]wireEntry
}{m: map[string]wireEntry{}}

func wireKey(server string) string {
	return strings.TrimRight(buildURL(server), "/")
}

// preferNativeWire reports whether the native routes should be tried first
// for the given server.
func preferNativeWire(server string) bool {
	wireChoice.Lock()
	defer wireChoice.Unlock()
	e, ok := wireChoice.m[wireKey(server)]
	if !ok || e.mode != wireLegacy {
		return true
	}
	return wireNow().Sub(e.since) >= legacyReprobeInterval
}

func rememberWire(server string, mode wireMode) {
	wireChoice.Lock()
	defer wireChoice.Unlock()
	key := wireKey(server)
	if prev, ok := wireChoice.m[key]; ok && prev.mode == mode {
		if mode == wireNative {
			return
		}
		// Legacy verdict: keep the original timestamp unless the
		// re-probe window has already elapsed, so the window is a
		// real bound rather than being renewed by every request.
		if wireNow().Sub(prev.since) < legacyReprobeInterval {
			return
		}
	}
	wireChoice.m[key] = wireEntry{mode: mode, since: wireNow()}
}

// resetWireChoice forgets every sticky decision (tests).
func resetWireChoice() {
	wireChoice.Lock()
	wireChoice.m = map[string]wireEntry{}
	wireChoice.Unlock()
}

// wireHeader is set by every OTA-Pulse-native route of the backend on every
// response (including its own legitimate 404s such as "Device not found").
// A native 404 that carries it comes from a server that speaks the native
// wire, so it is final and never triggers a legacy fallback.
const wireHeader = "X-OTAPulse-Wire"

// nativeNotServed reports whether a response to a NATIVE request means "this
// server has no native route": a 404 without the wire header (an old backend).
func nativeNotServed(r *http.Response) bool {
	return r.StatusCode == http.StatusNotFound && r.Header.Get(wireHeader) == ""
}

// legacyNotServed reports whether a response to a LEGACY request means "the
// legacy route is gone or disabled": 404, or 410 (LEGACY_DEVICE_WIRE_ENABLED
// =false answers 410 Gone). Neither ever records a "legacy only" verdict.
func legacyNotServed(r *http.Response) bool {
	return r.StatusCode == http.StatusNotFound || r.StatusCode == http.StatusGone
}

// wireDo sends the request produced by build(path) on the preferred route
// set and falls back to the other one only when the preferred one is "not
// served" (see nativeNotServed / legacyNotServed). build is called once per
// attempt so request bodies are never replayed from a consumed reader.
// nativePath and legacyPath are relative to apiPrefix (see buildApiURL).
//
// When the legacy retry of a native 404 fails too (404/410), the NATIVE 404
// is returned, so the caller sees the server's real answer.
//
// The returned response is the last one received; the caller owns its Body.
func wireDo(
	api ApiRequester,
	server, nativePath, legacyPath string,
	build func(path string) (*http.Request, error),
) (*http.Response, error) {
	order := []string{nativePath, legacyPath}
	if !preferNativeWire(server) {
		order = []string{legacyPath, nativePath}
	}
	var held *http.Response // first attempt's response, kept for hand-back
	for i, path := range order {
		isNative := path == nativePath
		req, err := build(path)
		if err != nil {
			closeBody(held)
			return nil, err
		}
		r, err := api.Do(req)
		if err != nil {
			closeBody(held)
			return r, err
		}
		last := i == len(order)-1
		notServed := (isNative && nativeNotServed(r)) || (!isNative && legacyNotServed(r))
		if notServed && !last {
			held = r
			logWireFallback(req, isNative, order[i+1])
			continue
		}
		if isNative {
			// A native answer is a verdict for the native wire unless it is
			// the "no such route" 404 of an old backend.
			if !nativeNotServed(r) {
				rememberWire(server, wireNative)
			}
			if held != nil {
				held.Body.Close()
			}
			return r, nil
		}
		// Legacy answer.
		if legacyNotServed(r) {
			// Legacy 404/410 after a native not-served 404: hand back the
			// native response, record nothing.
			if held != nil {
				r.Body.Close()
				return held, nil
			}
			return r, nil
		}
		rememberWire(server, wireLegacy)
		closeBody(held)
		return r, nil
	}
	return held, nil
}

func closeBody(r *http.Response) {
	if r != nil && r.Body != nil {
		r.Body.Close()
	}
}

func logWireFallback(req *http.Request, fromNative bool, to string) {
	if fromNative {
		log.Warnf("%s %s returned HTTP 404: server has no OTA-Pulse-native "+
			"device route, falling back to legacy path %s",
			req.Method, req.URL.Path, apiPrefix+strings.TrimPrefix(to, "/"))
		return
	}
	log.Infof("%s %s returned HTTP 404: trying native path %s",
		req.Method, req.URL.Path, apiPrefix+strings.TrimPrefix(to, "/"))
}
