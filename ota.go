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
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	UpdateDir     = "/var/lib/wave/updates"
	UpdateBaseURL = "https://releases.wave.online/edge"
	RollbackFile  = "/var/lib/wave/rollback-version"

	// maxUpdateBytes caps any single download. The agent runs on devices with
	// small disks, and the size field in a manifest is supplied by the same
	// party as the URL, so the bound has to be a constant we control.
	maxUpdateBytes = 512 << 20 // 512 MiB

	// updateHTTPTimeout bounds a whole request including body transfer, so a
	// server that stalls mid-stream cannot park the update loop forever.
	updateHTTPTimeout = 10 * time.Minute

	maxUpdateRedirects = 5
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

	q := url.Values{}
	q.Set("platform", ota.agent.config.Platform)
	q.Set("channel", ota.channel)
	q.Set("current", Version)
	manifestURL := fmt.Sprintf("%s/manifest.json?%s", UpdateBaseURL, q.Encode())

	resp, err := getFromUpdateOrigin(manifestURL)
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

// updateHTTPClient is the only client the update path uses. Every redirect hop
// is re-checked against validateUpdateURL: the origin check on the initial URL
// is worth nothing if a 302 can walk the download somewhere else.
var updateHTTPClient = &http.Client{
	Timeout: updateHTTPTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxUpdateRedirects {
			return fmt.Errorf("stopped after %d redirects", maxUpdateRedirects)
		}
		if err := validateUpdateURL(req.URL.String()); err != nil {
			return fmt.Errorf("redirect target rejected: %w", err)
		}
		return nil
	},
}

// normalizeUpdateHost lower-cases a host and strips the pieces that make two
// spellings of the same name compare unequal: the root label's trailing dot and
// an explicitly-written default port.
func normalizeUpdateHost(host string) string {
	h := strings.ToLower(host)
	h = strings.TrimSuffix(h, ":443")
	if i := strings.Index(h, ":"); i >= 0 {
		// A non-default port is part of the identity — keep it, but still trim
		// a trailing dot from the name portion.
		return strings.TrimSuffix(h[:i], ".") + h[i:]
	}
	return strings.TrimSuffix(h, ".")
}

// validateUpdateURL confines a download to the release origin over TLS.
//
// The URLs on this path arrive inside the update manifest and inside cloud
// commands, i.e. from the network, and what they select is a file this process
// installs and then executes as root. Matching is exact rather than
// prefix-based on purpose: "releases.wave.online.example.com" and
// "notreleases.wave.online" both share a substring with the real host and
// neither is it.
func validateUpdateURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("empty update URL")
	}

	base, err := url.Parse(UpdateBaseURL)
	if err != nil {
		return fmt.Errorf("invalid UpdateBaseURL %q: %w", UpdateBaseURL, err)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid update URL: %w", err)
	}
	if u.Opaque != "" {
		return fmt.Errorf("update URL must not be opaque")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("update URL must use https, got %q", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("update URL must not carry credentials")
	}
	if normalizeUpdateHost(u.Host) != normalizeUpdateHost(base.Host) {
		return fmt.Errorf("update URL host %q is not the release origin", u.Host)
	}

	// u.Path is already percent-decoded, so cleaning it here also collapses
	// encoded traversal such as %2e%2e%2f.
	basePath := path.Clean("/" + strings.Trim(base.Path, "/"))
	got := path.Clean("/" + strings.TrimPrefix(u.Path, "/"))
	if got != basePath && !strings.HasPrefix(got, basePath+"/") {
		return fmt.Errorf("update URL path %q is outside %q", u.Path, basePath)
	}

	return nil
}

// getFromUpdateOrigin validates a URL and fetches it with the guarded client.
// The caller closes the body.
func getFromUpdateOrigin(rawURL string) (*http.Response, error) {
	if err := validateUpdateURL(rawURL); err != nil {
		return nil, err
	}
	return updateHTTPClient.Get(rawURL)
}

func downloadFile(rawURL string, dest string) error {
	resp, err := getFromUpdateOrigin(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}

	// LimitReader is given one byte of headroom so that hitting the cap is
	// distinguishable from a download that is exactly maxUpdateBytes long.
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxUpdateBytes+1))
	closeErr := f.Close()

	if copyErr == nil && closeErr == nil && n <= maxUpdateBytes {
		return nil
	}

	os.Remove(dest)
	switch {
	case copyErr != nil:
		return copyErr
	case closeErr != nil:
		return closeErr
	default:
		return fmt.Errorf("update exceeds the %d byte limit", int64(maxUpdateBytes))
	}
}

// verifySHA256 checks a downloaded file against an expected digest.
//
// It fails closed. An absent or malformed digest is an error, never a skip: the
// digest travels in the same manifest as the URL, so treating "" as "no check
// required" hands whoever writes that manifest an integrity opt-out.
//
// Note the limit of what this proves. A digest that ships alongside the artifact
// establishes that the bytes were not altered in transit; it does not establish
// who authored them. Signing the manifest is tracked separately.
func verifySHA256(filePath string, expected string) error {
	if expected == "" {
		return fmt.Errorf("refusing to install without an expected sha256")
	}
	if len(expected) != hex.EncodedLen(sha256.Size) {
		return fmt.Errorf("expected sha256 must be %d hex characters, got %d",
			hex.EncodedLen(sha256.Size), len(expected))
	}
	expectedLower := strings.ToLower(expected)
	if _, err := hex.DecodeString(expectedLower); err != nil {
		return fmt.Errorf("expected sha256 is not valid hex: %w", err)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expectedLower {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", expectedLower, actual)
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

// updateAgent downloads and installs a new agent version (called from cloud command).
//
// sha256Hex is required. This path replaces /usr/local/bin/wave-agent and
// restarts into it, so it is the shortest route from a cloud message to code
// running as root, and it previously performed no integrity check at all. An
// update_agent command that carries no digest is now refused: the device keeps
// running the version it has, which is the safe outcome of the two.
func updateAgent(version string, rawURL string, sha256Hex string) error {
	if rawURL == "" {
		rawURL = fmt.Sprintf("%s/agent/wave-agent-%s-linux-arm64", UpdateBaseURL, version)
	}

	localPath := filepath.Join(UpdateDir, "wave-agent-"+version)
	if err := downloadFile(rawURL, localPath); err != nil {
		return err
	}

	if err := verifySHA256(localPath, sha256Hex); err != nil {
		os.Remove(localPath)
		return fmt.Errorf("verify agent %s: %w", version, err)
	}

	if err := replaceAgentBinary(localPath); err != nil {
		return err
	}

	return restartAgent()
}
