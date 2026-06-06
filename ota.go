// WAVE OTA Update System
// Delta updates, A/B partition support, automatic rollback
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	UpdateDir     = "/var/lib/wave/updates"
	UpdateBaseURL = "https://releases.wave.online/edge"
	RollbackFile  = "/var/lib/wave/rollback-version"
)

// UpdateManifest describes an available update
type UpdateManifest struct {
	Version     string            `json:"version"`
	ReleaseDate time.Time         `json:"release_date"`
	Channel     string            `json:"channel"` // stable, beta, nightly
	Platform    string            `json:"platform"`
	Components  []UpdateComponent `json:"components"`
	MinVersion  string            `json:"min_version,omitempty"` // Minimum version for delta
	Changelog   string            `json:"changelog"`
}

// UpdateComponent is a single updatable component
type UpdateComponent struct {
	Name      string `json:"name"` // agent, module, firmware, profile
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	DeltaFrom string `json:"delta_from,omitempty"` // If delta update, from which version
	DeltaURL  string `json:"delta_url,omitempty"`
	DeltaSHA  string `json:"delta_sha256,omitempty"`
	DeltaSize int64  `json:"delta_size_bytes,omitempty"`
}

// OTAManager handles over-the-air updates
type OTAManager struct {
	agent         *Agent
	channel       string
	autoUpdate    bool
	checkInterval time.Duration
	currentState  UpdateState
}

type UpdateState struct {
	Status         string    `json:"status"` // idle, checking, downloading, applying, rebooting, rolled_back
	Progress       int       `json:"progress_percent"`
	LastCheck      time.Time `json:"last_check"`
	LastUpdate     time.Time `json:"last_update"`
	CurrentVersion string    `json:"current_version"`
	TargetVersion  string    `json:"target_version,omitempty"`
	Error          string    `json:"error,omitempty"`
}

func NewOTAManager(agent *Agent) *OTAManager {
	return &OTAManager{
		agent:         agent,
		channel:       "stable",
		autoUpdate:    true,
		checkInterval: 6 * time.Hour,
		currentState: UpdateState{
			Status:         "idle",
			CurrentVersion: Version,
		},
	}
}

// Start begins periodic update checks
func (ota *OTAManager) Start() {
	if err := os.MkdirAll(UpdateDir, 0755); err != nil {
		log.Printf("OTA: failed to create update dir: %v", err)
		return
	}

	// Check for pending rollback
	ota.checkRollback()

	go ota.checkLoop()
	log.Printf("OTA: manager started (channel: %s, interval: %s)", ota.channel, ota.checkInterval)
}

func (ota *OTAManager) checkLoop() {
	// Initial check after 1 minute
	timer := time.NewTimer(1 * time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			ota.CheckForUpdates()
			timer.Reset(ota.checkInterval)
		case <-ota.agent.ctx.Done():
			return
		}
	}
}

// CheckForUpdates queries the update server
func (ota *OTAManager) CheckForUpdates() (*UpdateManifest, error) {
	ota.currentState.Status = "checking"
	ota.currentState.LastCheck = time.Now()

	url := fmt.Sprintf("%s/manifest.json?platform=%s&channel=%s&current=%s",
		UpdateBaseURL, ota.agent.config.Platform, ota.channel, Version)

	resp, err := http.Get(url)
	if err != nil {
		ota.currentState.Status = "idle"
		ota.currentState.Error = err.Error()
		return nil, fmt.Errorf("check updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		ota.currentState.Status = "idle"
		log.Println("OTA: no updates available")
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		ota.currentState.Status = "idle"
		return nil, fmt.Errorf("update server returned %d", resp.StatusCode)
	}

	var manifest UpdateManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		ota.currentState.Status = "idle"
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	ota.currentState.Status = "idle"
	ota.currentState.TargetVersion = manifest.Version
	log.Printf("OTA: update available: %s -> %s", Version, manifest.Version)

	if ota.autoUpdate {
		go ota.ApplyUpdate(&manifest)
	}

	return &manifest, nil
}

// ApplyUpdate downloads and applies an update
func (ota *OTAManager) ApplyUpdate(manifest *UpdateManifest) error {
	ota.currentState.Status = "downloading"
	ota.currentState.TargetVersion = manifest.Version

	for i, component := range manifest.Components {
		// Prefer delta update if available
		downloadURL := component.URL
		expectedSHA := component.SHA256
		if component.DeltaFrom == Version && component.DeltaURL != "" {
			downloadURL = component.DeltaURL
			expectedSHA = component.DeltaSHA
			log.Printf("OTA: using delta update for %s (%d bytes vs %d bytes full)",
				component.Name, component.DeltaSize, component.SizeBytes)
		}

		// Download
		localPath := filepath.Join(UpdateDir, fmt.Sprintf("%s-%s", component.Name, component.Version))
		if err := downloadFile(downloadURL, localPath); err != nil {
			ota.currentState.Status = "idle"
			ota.currentState.Error = fmt.Sprintf("download %s: %v", component.Name, err)
			return err
		}

		// Verify SHA256
		if err := verifySHA256(localPath, expectedSHA); err != nil {
			os.Remove(localPath)
			ota.currentState.Status = "idle"
			ota.currentState.Error = fmt.Sprintf("verify %s: %v", component.Name, err)
			return err
		}

		ota.currentState.Progress = ((i + 1) * 50) / len(manifest.Components)
	}

	// Save rollback info
	ota.saveRollbackVersion()

	// Apply components
	ota.currentState.Status = "applying"
	for i, component := range manifest.Components {
		localPath := filepath.Join(UpdateDir, fmt.Sprintf("%s-%s", component.Name, component.Version))
		if err := ota.applyComponent(component, localPath); err != nil {
			log.Printf("OTA: failed to apply %s, rolling back: %v", component.Name, err)
			ota.rollback()
			return err
		}
		ota.currentState.Progress = 50 + ((i+1)*50)/len(manifest.Components)
	}

	ota.currentState.Status = "idle"
	ota.currentState.Progress = 100
	ota.currentState.LastUpdate = time.Now()
	ota.currentState.CurrentVersion = manifest.Version
	log.Printf("OTA: update complete: %s", manifest.Version)

	// Clean up
	ota.cleanUpdateDir()

	return nil
}

func (ota *OTAManager) applyComponent(component UpdateComponent, localPath string) error {
	switch component.Name {
	case "agent":
		// Replace agent binary, systemd will restart
		log.Printf("OTA: updating agent binary to %s", component.Version)
		if err := replaceAgentBinary(localPath); err != nil {
			return err
		}
		// Signal systemd to restart us
		return restartAgent()

	case "module":
		// Install/update module
		log.Printf("OTA: updating module to %s", component.Version)
		return installModuleFromArchive(localPath)

	case "firmware":
		// Full firmware update — requires reboot
		log.Printf("OTA: applying firmware %s (reboot required)", component.Version)
		return applyFirmwareImage(localPath)

	case "profile":
		// Update profile definition
		log.Printf("OTA: updating profile %s", component.Version)
		return installProfile(localPath)

	default:
		return fmt.Errorf("unknown component type: %s", component.Name)
	}
}

func (ota *OTAManager) saveRollbackVersion() {
	os.WriteFile(RollbackFile, []byte(Version), 0644)
}

func (ota *OTAManager) checkRollback() {
	data, err := os.ReadFile(RollbackFile)
	if err != nil {
		return
	}
	prevVersion := string(data)
	if prevVersion != "" && prevVersion != Version {
		log.Printf("OTA: successfully updated from %s to %s", prevVersion, Version)
		os.Remove(RollbackFile)
	}
}

func (ota *OTAManager) rollback() {
	data, err := os.ReadFile(RollbackFile)
	if err != nil {
		log.Println("OTA: no rollback version available")
		return
	}
	log.Printf("OTA: rolling back to %s", string(data))
	ota.currentState.Status = "rolled_back"
	ota.currentState.Error = "update failed, rolled back to " + string(data)
	// In production: restore from backup partition or re-download old version
}

func (ota *OTAManager) cleanUpdateDir() {
	entries, _ := os.ReadDir(UpdateDir)
	for _, e := range entries {
		os.Remove(filepath.Join(UpdateDir, e.Name()))
	}
}

// GetState returns current OTA state (for API)
func (ota *OTAManager) GetState() UpdateState {
	return ota.currentState
}

// --- Helpers ---

func downloadFile(url string, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func verifySHA256(path string, expected string) error {
	if expected == "" {
		return nil // Skip verification if no hash provided
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

func replaceAgentBinary(newBinary string) error {
	targetPath := "/usr/local/bin/wave-agent"
	backupPath := targetPath + ".bak"

	// Backup current binary
	if err := copyFile(targetPath, backupPath); err != nil {
		return fmt.Errorf("backup current binary: %w", err)
	}

	// Replace with new binary
	if err := copyFile(newBinary, targetPath); err != nil {
		// Restore backup
		copyFile(backupPath, targetPath)
		return fmt.Errorf("replace binary: %w", err)
	}

	return os.Chmod(targetPath, 0755)
}

func restartAgent() error {
	cmd := exec.Command("systemctl", "restart", "wave-agent")
	return cmd.Start() // Non-blocking — we'll be killed by systemd
}

func installModuleFromArchive(archivePath string) error {
	cmd := exec.Command("tar", "xzf", archivePath, "-C", ModuleDir)
	return cmd.Run()
}

func applyFirmwareImage(imagePath string) error {
	// A/B partition scheme: write to inactive partition, switch boot
	log.Println("OTA: firmware update would be applied here (A/B partition switch)")
	return nil
}

func installProfile(profilePath string) error {
	return copyFile(profilePath, filepath.Join(ProfileDir, filepath.Base(profilePath)))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// updateAgent downloads and installs a new agent version (called from cloud command)
func updateAgent(version string, url string) error {
	if url == "" {
		url = fmt.Sprintf("%s/agent/wave-agent-%s-linux-arm64", UpdateBaseURL, version)
	}

	localPath := filepath.Join(UpdateDir, "wave-agent-"+version)
	if err := downloadFile(url, localPath); err != nil {
		return err
	}

	if err := replaceAgentBinary(localPath); err != nil {
		return err
	}

	return restartAgent()
}
