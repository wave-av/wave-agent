package main

// SEC-HARDENING (2026-09-14, HIGH): the LAN management API bound all interfaces
// with zero auth (root daemon). Reads stay open for the on-device dashboard,
// but the cloud token is never serialized and every mutating route requires
// the device Bearer token.
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testAgent() *Agent {
	return &Agent{
		config: DeviceConfig{
			DeviceID:   "dev-1",
			DeviceName: "test",
			Platform:   "linux",
			Profile:    "default",
			CloudToken: "wvtok_test_secret",
		},
		modules: map[string]*ModuleState{},
	}
}

func TestConfigRedactsCloudToken(t *testing.T) {
	srv := httptest.NewServer(testAgent().buildMux())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if tok, _ := body["cloud_token"].(string); tok != "" {
		t.Fatalf("cloud token leaked in /api/config: %q", tok)
	}
	if body["device_id"] != "dev-1" {
		t.Fatalf("expected device fields to survive redaction, got %v", body)
	}
}

func TestMutatingRoutesRequireDeviceAuth(t *testing.T) {
	srv := httptest.NewServer(testAgent().buildMux())
	defer srv.Close()

	// no credentials → 401
	res, err := http.Post(srv.URL+"/api/modules/stop", "application/json", strings.NewReader(`{"name":"camera"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("stop without auth = %d, want 401", res.StatusCode)
	}

	// wrong credentials → 403 (proves the handler reached auth, not the module)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/modules/stop", strings.NewReader(`{"name":"camera"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != 403 {
		t.Fatalf("stop with wrong auth = %d, want 403", res2.StatusCode)
	}

	// PUT config without auth → 401
	req3, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/config", strings.NewReader(`{"device_name":"x"}`))
	res3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != 401 {
		t.Fatalf("config PUT without auth = %d, want 401", res3.StatusCode)
	}
}

func TestReadsStayOpen(t *testing.T) {
	srv := httptest.NewServer(testAgent().buildMux())
	defer srv.Close()
	for _, p := range []string{"/health", "/api/system", "/api/modules", "/metrics"} {
		res, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("GET %s = %d, want 200 (dashboard reads must stay open)", p, res.StatusCode)
		}
	}
}
