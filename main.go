// WAVE Agent — Edge device management daemon
// Single binary for Raspberry Pi, RK3328 SBC, x86_64 servers
// Handles: device identity, module lifecycle, config, health, cloud sync
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	Version       = "0.1.0"
	ConfigDir     = "/etc/wave"
	StateDir      = "/var/lib/wave"
	LogDir        = "/var/log/wave"
	ModuleDir     = "/opt/wave/modules"
	ProfileDir    = "/opt/wave/profiles"
	HealthPort    = 9090
	WebUIPort     = 8080
	CloudEndpoint = "wss://edge.wave.online/v1/agent"
)

// DeviceConfig persists across reboots
type DeviceConfig struct {
	DeviceID   string            `json:"device_id"`
	DeviceName string            `json:"device_name"`
	Platform   string            `json:"platform"`
	Profile    string            `json:"profile"`
	CloudToken string            `json:"cloud_token,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// ModuleState tracks running modules
type ModuleState struct {
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	Status    string         `json:"status"` // running, stopped, error, installing
	PID       int            `json:"pid,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
	Health    ModuleHealth   `json:"health"`
	StartedAt *time.Time     `json:"started_at,omitempty"`
}

type ModuleHealth struct {
	Status    string         `json:"status"` // healthy, degraded, unhealthy
	LastCheck time.Time      `json:"last_check"`
	Metrics   map[string]any `json:"metrics,omitempty"`
	Errors    []string       `json:"errors,omitempty"`
}

// Agent is the main daemon
type Agent struct {
	config  DeviceConfig
	modules map[string]*ModuleState
	mu      sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc
}

func NewAgent() *Agent {
	ctx, cancel := context.WithCancel(context.Background())
	return &Agent{
		modules: make(map[string]*ModuleState),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Init loads or creates device config
func (a *Agent) Init() error {
	if err := os.MkdirAll(ConfigDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.MkdirAll(StateDir, 0755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(LogDir, 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	configPath := filepath.Join(ConfigDir, "device.json")
	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, &a.config); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
		log.Printf("Loaded device config: %s (%s)", a.config.DeviceID, a.config.Platform)
	} else {
		a.config = DeviceConfig{
			DeviceID:   generateDeviceID(),
			DeviceName: generateDeviceName(),
			Platform:   detectPlatform(),
			Tags:       make(map[string]string),
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		if err := a.saveConfig(); err != nil {
			return fmt.Errorf("save initial config: %w", err)
		}
		log.Printf("Created new device: %s (%s)", a.config.DeviceID, a.config.Platform)
	}

	return nil
}

func (a *Agent) saveConfig() error {
	a.config.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(a.config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ConfigDir, "device.json"), data, 0644)
}

// safeNamePattern is the allowlist for caller-supplied module and profile
// names. Module/profile names arrive from untrusted sources (the cloud
// WebSocket command channel and the local HTTP API), and are used to build
// filesystem paths that get executed and systemd unit names. Anything outside
// this character set — path separators, "..", whitespace, NUL, or shell
// metacharacters — is rejected before the value can reach os/exec.
var safeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// validateName enforces safeNamePattern on an untrusted identifier.
func validateName(kind, name string) error {
	if !safeNamePattern.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid %s name %q: must match %s", kind, name, safeNamePattern.String())
	}
	return nil
}

// resolveUnder joins name onto dir and guarantees the result stays inside dir.
// Defence in depth behind validateName: even if the allowlist is ever widened,
// a traversal cannot escape the intended directory.
func resolveUnder(dir, name string) (string, error) {
	root := filepath.Clean(dir)
	p := filepath.Join(root, name)
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("resolved path %q escapes %q", p, root)
	}
	return p, nil
}

// LoadProfile loads a module profile and starts its modules
func (a *Agent) LoadProfile(profileName string) error {
	if err := validateName("profile", profileName); err != nil {
		return err
	}

	profilePath, err := resolveUnder(ProfileDir, profileName+".yaml")
	if err != nil {
		return err
	}
	if _, err := os.Stat(profilePath); err != nil {
		return fmt.Errorf("profile not found: %s", profilePath)
	}

	a.config.Profile = profileName
	if err := a.saveConfig(); err != nil {
		return err
	}

	log.Printf("Loading profile: %s", profileName)

	// Parse YAML profile (simplified — real impl uses go-yaml)
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return err
	}

	// Extract module names from YAML (basic parsing)
	var modules []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- name:") {
			name := strings.TrimPrefix(line, "- name:")
			name = strings.TrimSpace(name)
			modules = append(modules, name)
		}
	}

	for _, mod := range modules {
		if err := a.InstallModule(mod); err != nil {
			log.Printf("Warning: failed to install module %s: %v", mod, err)
		}
	}

	return nil
}

// InstallModule installs and starts a module
func (a *Agent) InstallModule(name string) error {
	// Validate BEFORE the name is used to build any path or command argument.
	if err := validateName("module", name); err != nil {
		return err
	}

	modulePath, err := resolveUnder(ModuleDir, name)
	if err != nil {
		return err
	}
	installScript := filepath.Join(modulePath, "install.sh")

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.modules[name]; exists {
		return fmt.Errorf("module %s already installed", name)
	}

	state := &ModuleState{
		Name:   name,
		Status: "installing",
		Config: make(map[string]any),
		Health: ModuleHealth{
			Status:    "unknown",
			LastCheck: time.Now(),
		},
	}
	a.modules[name] = state

	// Run install script if it exists
	if _, err := os.Stat(installScript); err == nil {
		log.Printf("Installing module: %s", name)
		// Explicit argv — never a shell string. "--" stops bash from
		// interpreting a script path as an option.
		cmd := exec.CommandContext(a.ctx, "bash", "--", installScript)
		cmd.Dir = modulePath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			state.Status = "error"
			state.Health.Status = "unhealthy"
			state.Health.Errors = append(state.Health.Errors, err.Error())
			return fmt.Errorf("install %s: %w", name, err)
		}
	}

	state.Status = "running"
	state.Health.Status = "healthy"
	now := time.Now()
	state.StartedAt = &now
	log.Printf("Module started: %s", name)
	return nil
}

// StopModule stops a running module
func (a *Agent) StopModule(name string) error {
	if err := validateName("module", name); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	state, exists := a.modules[name]
	if !exists {
		return fmt.Errorf("module %s not found", name)
	}

	// Stop systemd service — explicit argv, validated unit name.
	cmd := exec.Command("systemctl", "stop", "--", "wave-"+name)
	_ = cmd.Run() // Ignore error if service doesn't exist

	state.Status = "stopped"
	state.PID = 0
	log.Printf("Module stopped: %s", name)
	return nil
}

// HealthCheck runs health checks on all modules
func (a *Agent) HealthCheck() map[string]ModuleHealth {
	a.mu.RLock()
	defer a.mu.RUnlock()

	results := make(map[string]ModuleHealth)
	for name, mod := range a.modules {
		health := ModuleHealth{
			Status:    "healthy",
			LastCheck: time.Now(),
			Metrics:   make(map[string]any),
		}

		// Check systemd service status
		// name is allowlisted at install time (validateName), so it cannot
		// carry separators or option-leading characters here.
		cmd := exec.Command("systemctl", "is-active", "--", "wave-"+name)
		if output, err := cmd.Output(); err != nil {
			health.Status = "unhealthy"
			health.Errors = append(health.Errors, "service not active")
		} else if strings.TrimSpace(string(output)) != "active" {
			health.Status = "degraded"
		}

		mod.Health = health
		results[name] = health
	}
	return results
}

// SystemInfo returns full device info
func (a *Agent) SystemInfo() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// Get HAL info
	halInfo := make(map[string]any)
	cmd := exec.Command("bash", "/opt/wave/hal/hal.sh", "info")
	if output, err := cmd.Output(); err == nil {
		_ = json.Unmarshal(output, &halInfo)
	}

	moduleList := make([]map[string]any, 0)
	for _, mod := range a.modules {
		moduleList = append(moduleList, map[string]any{
			"name":   mod.Name,
			"status": mod.Status,
			"health": mod.Health.Status,
		})
	}

	return map[string]any{
		"agent_version": Version,
		"device":        a.config,
		"hardware":      halInfo,
		"modules":       moduleList,
		"uptime":        time.Since(a.config.CreatedAt).String(),
	}
}

// --- Helpers ---

func generateDeviceID() string {
	// Use MAC address of primary interface for deterministic ID
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		mac := iface.HardwareAddr.String()
		if mac != "" {
			return "wave-" + strings.ReplaceAll(mac, ":", "")
		}
	}
	// Fallback: hostname + timestamp
	host, _ := os.Hostname()
	return fmt.Sprintf("wave-%s-%d", host, time.Now().Unix())
}

func generateDeviceName() string {
	host, _ := os.Hostname()
	if host != "" {
		return host
	}
	return "wave-edge"
}

func detectPlatform() string {
	// Read device tree model
	data, err := os.ReadFile("/proc/device-tree/model")
	if err == nil {
		model := strings.TrimRight(string(data), "\x00")
		switch {
		case strings.Contains(model, "Raspberry Pi 5"):
			return "rpi5"
		case strings.Contains(model, "Raspberry Pi Zero 2"):
			return "rpi-zero2w"
		case strings.Contains(strings.ToLower(model), "rk3328"):
			return "birddog-play"
		}
	}
	return "x86_64-server"
}

// --- Main ---

func main() {
	var (
		httpPort    = flag.Int("port", WebUIPort, "HTTP API port")
		profileName = flag.String("profile", "", "Module profile to load")
		showVersion = flag.Bool("version", false, "Show version")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("WAVE Agent v%s\n", Version)
		os.Exit(0)
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("WAVE Agent v%s starting...", Version)

	agent := NewAgent()

	if err := agent.Init(); err != nil {
		log.Fatalf("Init failed: %v", err)
	}

	// Load profile if specified (or from saved config)
	profile := *profileName
	if profile == "" {
		profile = agent.config.Profile
	}
	if profile != "" {
		if err := agent.LoadProfile(profile); err != nil {
			log.Printf("Warning: failed to load profile %s: %v", profile, err)
		}
	}

	// Start HTTP server
	server := agent.startHTTPServer(*httpPort)

	// Health check ticker
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				agent.HealthCheck()
			case <-agent.ctx.Done():
				return
			}
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")
	ticker.Stop()
	agent.cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)

	log.Println("WAVE Agent stopped.")
}
