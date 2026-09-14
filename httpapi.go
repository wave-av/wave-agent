// WAVE Agent HTTP API
// REST endpoints for device management, module control, and Prometheus metrics
package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// SEC-HARDENING (2026-09-14, HIGH): the LAN management API is reachable
// unauthenticated (binds all interfaces, runs as root). Reads stay open for
// the on-device dashboard, but (a) the cloud token is NEVER serialized and
// (b) every mutating route requires the device Bearer token.

// sanitizedConfig returns the device config with the cloud token stripped.
// The token authenticates the device toward edge.wave.online; serving it on
// an unauthenticated LAN endpoint leaks it to any network peer (and to every
// browser that opens the dashboard, which polls /api/system).
func (a *Agent) sanitizedConfig() DeviceConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	c := a.config
	c.CloudToken = ""
	return c
}

// requireDeviceAuth gates mutating routes on the device Bearer token.
// Fail-open ONLY while unprovisioned (no token exists yet, nothing to steal);
// once provisioned, missing/wrong credentials are rejected.
func (a *Agent) requireDeviceAuth(w http.ResponseWriter, r *http.Request) bool {
	a.mu.RLock()
	token := a.config.CloudToken
	a.mu.RUnlock()
	if token == "" {
		return true
	}
	got := r.Header.Get("Authorization")
	if got == "" {
		http.Error(w, "missing Authorization: Bearer <device-token>", 401)
		return false
	}
	want := "Bearer " + token
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		http.Error(w, "forbidden", 403)
		return false
	}
	return true
}

func (a *Agent) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Web UI (embedded dashboard)
	RegisterWebUI(mux)

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "ok",
			"version": Version,
			"device":  a.config.DeviceID,
		})
	})

	// System info (token-redacted; the dashboard polls this every 5s)
	mux.HandleFunc("/api/system", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		info := a.SystemInfo()
		if dev, ok := info["device"].(DeviceConfig); ok {
			dev.CloudToken = ""
			info["device"] = dev
		}
		json.NewEncoder(w).Encode(info)
	})

	// Module list
	mux.HandleFunc("/api/modules", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.modules)
	})

	// Module health
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.HealthCheck())
	})

	// Module install (privileged: executes install.sh as root — device auth required)
	mux.HandleFunc("/api/modules/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if !a.requireDeviceAuth(w, r) {
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := a.InstallModule(req.Name); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "installed", "module": req.Name})
	})

	// Module stop (privileged — device auth required)
	mux.HandleFunc("/api/modules/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if !a.requireDeviceAuth(w, r) {
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := a.StopModule(req.Name); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "stopped", "module": req.Name})
	})

	// Device config (reads: token-redacted; writes: device auth required)
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(a.sanitizedConfig())
		case http.MethodPut:
			if !a.requireDeviceAuth(w, r) {
				return
			}
			var updates map[string]string
			if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if name, ok := updates["device_name"]; ok {
				a.config.DeviceName = name
			}
			if profile, ok := updates["profile"]; ok {
				a.config.Profile = profile
			}
			if err := a.saveConfig(); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(a.sanitizedConfig())
		default:
			http.Error(w, "Method not allowed", 405)
		}
	})

	// Prometheus metrics
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		defer a.mu.RUnlock()

		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "# HELP wave_agent_info WAVE agent information\n")
		fmt.Fprintf(w, "# TYPE wave_agent_info gauge\n")
		fmt.Fprintf(w, "wave_agent_info{version=%q,device=%q,platform=%q} 1\n",
			Version, a.config.DeviceID, a.config.Platform)

		fmt.Fprintf(w, "# HELP wave_modules_total Number of installed modules\n")
		fmt.Fprintf(w, "# TYPE wave_modules_total gauge\n")
		fmt.Fprintf(w, "wave_modules_total %d\n", len(a.modules))

		for name, mod := range a.modules {
			running := 0
			if mod.Status == "running" {
				running = 1
			}
			fmt.Fprintf(w, "wave_module_running{module=%q} %d\n", name, running)
			healthy := 0
			if mod.Health.Status == "healthy" {
				healthy = 1
			}
			fmt.Fprintf(w, "wave_module_healthy{module=%q} %d\n", name, healthy)
		}
	})

	return mux
}

func (a *Agent) startHTTPServer(port int) *http.Server {
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      a.buildMux(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("HTTP API listening on :%d", port)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return server
}
