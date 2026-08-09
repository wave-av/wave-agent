package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateNameRejectsInjection covers the inputs that reached os/exec via
// filepath.Join before the fix (GHAS go/command-injection, main.go:194).
func TestValidateNameRejectsInjection(t *testing.T) {
	bad := []string{
		"",
		"../../tmp/evil",
		"..",
		"foo/bar",
		"/etc/wave",
		"foo;rm -rf /",
		"foo$(id)",
		"foo`id`",
		"foo bar",
		"foo\nbar",
		"-rf",
		"foo\x00bar",
	}
	for _, name := range bad {
		if err := validateName("module", name); err == nil {
			t.Errorf("validateName accepted unsafe name %q", name)
		}
	}

	good := []string{"camera", "thermal-cam", "audio_v2", "mod.1"}
	for _, name := range good {
		if err := validateName("module", name); err != nil {
			t.Errorf("validateName rejected legitimate name %q: %v", name, err)
		}
	}
}

// TestResolveUnderContainsPaths is the defence-in-depth layer behind the allowlist.
func TestResolveUnderContainsPaths(t *testing.T) {
	if _, err := resolveUnder(ModuleDir, "../../etc/wave"); err == nil {
		t.Error("resolveUnder allowed a path escaping ModuleDir")
	}
	got, err := resolveUnder(ModuleDir, "camera")
	if err != nil {
		t.Fatalf("resolveUnder rejected a legitimate name: %v", err)
	}
	if want := ModuleDir + "/camera"; got != want {
		t.Errorf("resolveUnder = %q, want %q", got, want)
	}
}

// writeTempFile returns the path to a file holding body, plus its sha256.
func writeTempFile(t *testing.T, body string) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	sum := sha256.Sum256([]byte(body))
	return p, hex.EncodeToString(sum[:])
}

// TestVerifySHA256FailsClosed pins the property that an absent or unusable
// expected digest is an ERROR. It used to return nil, which meant the manifest
// that supplies the download URL could also switch the integrity check off.
func TestVerifySHA256FailsClosed(t *testing.T) {
	file, good := writeTempFile(t, "wave-agent binary payload")

	if err := verifySHA256(file, ""); err == nil {
		t.Error("verifySHA256 accepted an empty expected digest")
	}

	bad := map[string]string{
		"too short":     strings.Repeat("a", 63),
		"too long":      strings.Repeat("a", 65),
		"not hex":       strings.Repeat("z", 64),
		"wrong digest":  strings.Repeat("0", 64),
		"whitespace":    " " + good[1:],
		"0x prefixed":   "0x" + good[2:],
		"hex of no len": "abc",
	}
	for name, expected := range bad {
		if err := verifySHA256(file, expected); err == nil {
			t.Errorf("verifySHA256 accepted %s digest %q", name, expected)
		}
	}

	if err := verifySHA256(file, good); err != nil {
		t.Errorf("verifySHA256 rejected the correct digest: %v", err)
	}
	if err := verifySHA256(file, strings.ToUpper(good)); err != nil {
		t.Errorf("verifySHA256 rejected the correct digest in uppercase: %v", err)
	}
}

// TestValidateUpdateURLConfinesToReleaseOrigin covers the near-miss hosts that a
// prefix or suffix match would let through, along with scheme and path escapes.
func TestValidateUpdateURLConfinesToReleaseOrigin(t *testing.T) {
	bad := []string{
		"",
		"http://releases.wave.online/edge/agent", // not TLS
		"https://releases.wave.online.evil.tld/edge/agent",    // allowed host as a prefix
		"https://notreleases.wave.online/edge/agent",          // allowed host as a suffix
		"https://evil.tld/edge/agent",                         // unrelated host
		"https://user:pw@releases.wave.online/edge/agent",     // embedded credentials
		"https://releases.wave.online/other/agent",            // outside the base path
		"https://releases.wave.online/edgehog/agent",          // partial path segment
		"https://releases.wave.online/edge/../etc/passwd",     // traversal
		"https://releases.wave.online/edge/%2e%2e/etc/passwd", // encoded traversal
		"https://releases.wave.online:8443/edge/agent",        // non-default port
		"file:///etc/passwd",
		"mailto:ops@wave.online",
	}
	for _, raw := range bad {
		if err := validateUpdateURL(raw); err == nil {
			t.Errorf("validateUpdateURL accepted off-origin URL %q", raw)
		}
	}

	good := []string{
		"https://releases.wave.online/edge",
		"https://releases.wave.online/edge/agent/wave-agent-1.2.3-linux-arm64",
		"https://releases.wave.online/edge/manifest.json?platform=edge&channel=stable",
		"https://RELEASES.WAVE.ONLINE/edge/agent",  // host is case-insensitive
		"https://releases.wave.online./edge/agent", // root-label trailing dot
		"https://releases.wave.online:443/edge/agent",
	}
	for _, raw := range good {
		if err := validateUpdateURL(raw); err != nil {
			t.Errorf("validateUpdateURL rejected legitimate URL %q: %v", raw, err)
		}
	}
}

// TestUpdateStagingPathRejectsTraversal pins that manifest- and cloud-supplied
// component identity goes through the same allowlist as module and profile
// names before it becomes a filename written as root under UpdateDir.
func TestUpdateStagingPathRejectsTraversal(t *testing.T) {
	bad := [][2]string{
		{"../../usr/local/bin/wave-agent", "1.0.0"},
		{"agent", "../../etc/cron.d/evil"},
		{"agent/sub", "1.0.0"},
		{"", "1.0.0"},
		{"agent", ""},
		{"agent", "1.0.0;rm -rf /"},
	}
	for _, nv := range bad {
		if _, err := updateStagingPath(nv[0], nv[1]); err == nil {
			t.Errorf("updateStagingPath accepted name %q version %q", nv[0], nv[1])
		}
	}

	got, err := updateStagingPath("agent", "1.2.3")
	if err != nil {
		t.Fatalf("updateStagingPath rejected a legitimate component: %v", err)
	}
	if want := UpdateDir + "/agent-1.2.3"; got != want {
		t.Errorf("updateStagingPath = %q, want %q", got, want)
	}
}

// TestUpdateClientRevalidatesRedirects is the reason the origin check is not
// decorative: without it a 302 from the real origin walks the download anywhere.
func TestUpdateClientRevalidatesRedirects(t *testing.T) {
	mustRequest := func(raw string) *http.Request {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("build request for %q: %v", raw, err)
		}
		return req
	}

	offOrigin := mustRequest("https://evil.tld/edge/agent")
	if err := updateHTTPClient.CheckRedirect(offOrigin, nil); err == nil {
		t.Error("client followed a redirect that left the release origin")
	}

	onOrigin := mustRequest("https://releases.wave.online/edge/agent")
	if err := updateHTTPClient.CheckRedirect(onOrigin, nil); err != nil {
		t.Errorf("client refused an on-origin redirect: %v", err)
	}

	via := make([]*http.Request, maxUpdateRedirects)
	for i := range via {
		via[i] = onOrigin
	}
	if err := updateHTTPClient.CheckRedirect(onOrigin, via); err == nil {
		t.Errorf("client did not stop after %d redirects", maxUpdateRedirects)
	}
}

// TestDownloadFileRejectsOffOriginBeforeAnyRequest proves the guard sits in
// downloadFile itself, so every caller inherits it rather than each remembering.
func TestDownloadFileRejectsOffOriginBeforeAnyRequest(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "payload")
	if err := downloadFile("https://evil.tld/edge/agent", dest); err == nil {
		t.Error("downloadFile accepted an off-origin URL")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("downloadFile created a destination file for a rejected URL")
	}
}
