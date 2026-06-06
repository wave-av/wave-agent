// WAVE Agent HTTP API
// REST endpoints for device management, module control, and Prometheus metrics
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

func (a *Agent) startHTTPServer(port int) *http.Server {
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

	// System info
	mux.HandleFunc("/api/system", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.SystemInfo())
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

	// Module install
	mux.HandleFunc("/api/modules/install", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", 405)
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

	// Module stop
	mux.HandleFunc("/api/modules/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", 405)
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

	// Device config
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(a.config)
		case http.MethodPut:
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
			json.NewEncoder(w).Encode(a.config)
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

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
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
