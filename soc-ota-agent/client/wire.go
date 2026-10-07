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
// ONLY when the server answers HTTP 404 for the native path (a server that
// predates the native routes). Any other status — 401, 403, 5xx, and also
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

// wireDo sends the request produced by build(path) on the preferred route
// set and, only on HTTP 404, on the other one. build is called once per
// attempt so request bodies are never replayed from a consumed reader.
// nativePath and legacyPath are relative to apiPrefix (see buildApiURL).
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
	var r *http.Response
	for i, path := range order {
		req, err := build(path)
		if err != nil {
			return nil, err
		}
		r, err = api.Do(req)
		if err != nil {
			return r, err
		}
		if r.StatusCode == http.StatusNotFound && i < len(order)-1 {
			r.Body.Close()
			logWireFallback(req, path == nativePath, order[i+1])
			continue
		}
		if r.StatusCode != http.StatusNotFound {
			if path == nativePath {
				rememberWire(server, wireNative)
			} else {
				rememberWire(server, wireLegacy)
			}
		}
		return r, nil
	}
	return r, nil
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
