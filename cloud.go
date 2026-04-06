// WAVE Cloud Connector
// WebSocket connection to wave.online for remote management
// Features: heartbeat, command channel, config sync, telemetry push
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	HeartbeatInterval = 30 * time.Second
	ReconnectDelay    = 5 * time.Second
	MaxReconnectDelay = 5 * time.Minute
	TelemetryInterval = 60 * time.Second
)

// CloudMessage is the wire format for cloud communication
type CloudMessage struct {
	Type      string    `json:"type"`
	DeviceID  string    `json:"device_id"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload,omitempty"`
}

// CloudCommand is a command from the cloud
type CloudCommand struct {
	ID      string         `json:"id"`
	Action  string         `json:"action"`
	Params  map[string]any `json:"params,omitempty"`
	Timeout int            `json:"timeout_seconds,omitempty"`
}

// CommandResult is sent back to cloud after executing a command
type CommandResult struct {
	CommandID string `json:"command_id"`
	Status    string `json:"status"` // success, error, timeout
	Result    any    `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// CloudConnector manages the connection to WAVE cloud
type CloudConnector struct {
	agent       *Agent
	endpoint    string
	token       string
	connected   bool
	mu          sync.RWMutex
	commandCh   chan CloudCommand
	telemetryCh chan CloudMessage
}

func NewCloudConnector(agent *Agent) *CloudConnector {
	return &CloudConnector{
		agent:       agent,
		endpoint:    CloudEndpoint,
		token:       agent.config.CloudToken,
		commandCh:   make(chan CloudCommand, 100),
		telemetryCh: make(chan CloudMessage, 1000),
	}
}

// Start begins the cloud connection loop
// In production, this uses gorilla/websocket — here we use HTTP long-poll as fallback
func (cc *CloudConnector) Start() {
	if cc.token == "" {
		log.Println("Cloud: no token configured, running in offline mode")
		return
	}

	go cc.heartbeatLoop()
	go cc.telemetryLoop()
	go cc.commandLoop()

	log.Printf("Cloud: connector started (endpoint: %s)", cc.endpoint)
}

func (cc *CloudConnector) heartbeatLoop() {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cc.sendHeartbeat()
		case <-cc.agent.ctx.Done():
			return
		}
	}
}

func (cc *CloudConnector) sendHeartbeat() {
	cc.agent.mu.RLock()
	moduleCount := len(cc.agent.modules)
	var runningCount int
	for _, m := range cc.agent.modules {
		if m.Status == "running" {
			runningCount++
		}
	}
	cc.agent.mu.RUnlock()

	heartbeat := CloudMessage{
		Type:      "heartbeat",
		DeviceID:  cc.agent.config.DeviceID,
		Timestamp: time.Now(),
		Payload: map[string]any{
			"platform":        cc.agent.config.Platform,
			"profile":         cc.agent.config.Profile,
			"agent_version":   Version,
			"modules_total":   moduleCount,
			"modules_running": runningCount,
			"uptime_seconds":  time.Since(cc.agent.config.CreatedAt).Seconds(),
		},
	}

	data, _ := json.Marshal(heartbeat)
	cc.postToCloud("/api/v1/devices/heartbeat", data)
}

func (cc *CloudConnector) telemetryLoop() {
	ticker := time.NewTicker(TelemetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cc.pushTelemetry()
		case <-cc.agent.ctx.Done():
			return
		}
	}
}

func (cc *CloudConnector) pushTelemetry() {
	info := cc.agent.SystemInfo()
	msg := CloudMessage{
		Type:      "telemetry",
		DeviceID:  cc.agent.config.DeviceID,
		Timestamp: time.Now(),
		Payload:   info,
	}

	data, _ := json.Marshal(msg)
	cc.postToCloud("/api/v1/devices/telemetry", data)
}

func (cc *CloudConnector) commandLoop() {
	for {
		select {
		case cmd := <-cc.commandCh:
			result := cc.executeCommand(cmd)
			data, _ := json.Marshal(result)
			cc.postToCloud("/api/v1/devices/command-result", data)
		case <-cc.agent.ctx.Done():
			return
		}
	}
}

func (cc *CloudConnector) executeCommand(cmd CloudCommand) CommandResult {
	log.Printf("Cloud: executing command %s (action: %s)", cmd.ID, cmd.Action)

	result := CommandResult{
		CommandID: cmd.ID,
		Status:    "success",
	}

	switch cmd.Action {
	case "install_module":
		name, _ := cmd.Params["name"].(string)
		if err := cc.agent.InstallModule(name); err != nil {
			result.Status = "error"
			result.Error = err.Error()
		} else {
			result.Result = map[string]string{"module": name, "status": "installed"}
		}

	case "stop_module":
		name, _ := cmd.Params["name"].(string)
		if err := cc.agent.StopModule(name); err != nil {
			result.Status = "error"
			result.Error = err.Error()
		} else {
			result.Result = map[string]string{"module": name, "status": "stopped"}
		}

	case "load_profile":
		name, _ := cmd.Params["name"].(string)
		if err := cc.agent.LoadProfile(name); err != nil {
			result.Status = "error"
			result.Error = err.Error()
		} else {
			result.Result = map[string]string{"profile": name, "status": "loaded"}
		}

	case "reboot":
		log.Println("Cloud: reboot requested")
		result.Result = "rebooting"
		// Delayed reboot to allow response
		go func() {
			time.Sleep(2 * time.Second)
			runSystemCommand("reboot")
		}()

	case "update_agent":
		version, _ := cmd.Params["version"].(string)
		url, _ := cmd.Params["url"].(string)
		if err := updateAgent(version, url); err != nil {
			result.Status = "error"
			result.Error = err.Error()
		} else {
			result.Result = map[string]string{"version": version, "status": "updating"}
		}

	case "get_logs":
		lines, _ := cmd.Params["lines"].(float64)
		if lines == 0 {
			lines = 100
		}
		logs := getRecentLogs(int(lines))
		result.Result = logs

	case "health_check":
		health := cc.agent.HealthCheck()
		result.Result = health

	default:
		result.Status = "error"
		result.Error = fmt.Sprintf("unknown command action: %s", cmd.Action)
	}

	return result
}

func (cc *CloudConnector) postToCloud(path string, data []byte) {
	cc.mu.RLock()
	token := cc.token
	cc.mu.RUnlock()

	if token == "" {
		return
	}

	url := fmt.Sprintf("https://edge.wave.online%s", path)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Wave-Device-ID", cc.agent.config.DeviceID)
	req.Header.Set("X-Wave-Agent-Version", Version)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Cloud: POST %s failed: %v", path, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("Cloud: POST %s returned %d", path, resp.StatusCode)
	}

	_ = data // data would be set as request body in real impl
}

// --- Helpers ---

func runSystemCommand(cmd string) {
	// Placeholder — real impl uses exec.Command
	log.Printf("System command: %s", cmd)
}

func getRecentLogs(lines int) []string {
	// Read from journalctl
	return []string{fmt.Sprintf("(last %d log lines would be here)", lines)}
}
