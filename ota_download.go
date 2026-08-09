// WAVE OTA Update System — hardened download stack.
//
// Everything in this file sits between the network and the filesystem on the
// update path: origin confinement, redirect re-validation, stall-bounded
// transfers, size caps, staging-path validation, and fail-closed integrity
// checking. The manager/state/apply logic lives in ota.go.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

const (
	// maxUpdateBytes caps any single download. The agent runs on devices with
	// small disks, and the size field in a manifest is supplied by the same
	// party as the URL, so the bound has to be a constant we control.
	maxUpdateBytes = 512 << 20 // 512 MiB

	// maxManifestBytes caps the manifest document itself. Unlike artifacts,
	// the manifest is buffered in memory while it is decoded, so an unbounded
	// body is an OOM lever rather than a disk-space one. A real manifest is a
	// few KiB; 1 MiB is generous.
	maxManifestBytes = 1 << 20 // 1 MiB

	// updateConnectTimeout bounds connection setup: dialing, the TLS
	// handshake, and the wait for response headers.
	updateConnectTimeout = 1 * time.Minute

	// updateStallTimeout aborts a transfer that makes no progress for this
	// long. The bound is on progress rather than the whole request on
	// purpose: a wall-clock cap sized against maxUpdateBytes would demand
	// ~7 Mbit/s sustained, so a device on a slow link could never finish and
	// would retry from zero forever, while a server that stalls mid-stream
	// still cannot park the update loop.
	updateStallTimeout = 2 * time.Minute

	maxUpdateRedirects = 5
)

// updateHTTPClient is the only client the update path uses. Every redirect hop
// is re-checked against validateUpdateURL: the origin check on the initial URL
// is worth nothing if a 302 can walk the download somewhere else.
//
// There is deliberately no Client.Timeout: that would bound the whole exchange
// including the body transfer. Connection setup is bounded here; the transfer
// itself is bounded by progress via stallGuardBody in getFromUpdateOrigin.
var updateHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: updateConnectTimeout}).DialContext,
		TLSHandshakeTimeout:   updateConnectTimeout,
		ResponseHeaderTimeout: updateConnectTimeout,
	},
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

// sameUpdateHost compares two authorities component-wise via url.Hostname and
// url.Port rather than as raw strings, so bracketed IPv6 literals and other
// unusual spellings are handled by the parser instead of ad hoc trimming. Only
// the root label's trailing dot still needs normalizing, and the port defaults
// to 443 because the scheme is already pinned to https.
func sameUpdateHost(u, base *url.URL) bool {
	uName := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	baseName := strings.TrimSuffix(strings.ToLower(base.Hostname()), ".")
	if uName == "" || uName != baseName {
		return false
	}
	uPort := u.Port()
	if uPort == "" {
		uPort = "443"
	}
	basePort := base.Port()
	if basePort == "" {
		basePort = "443"
	}
	return uPort == basePort
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
	if !sameUpdateHost(u, base) {
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

// updateStagingPath resolves the staging file for a component inside
// UpdateDir. Name and version arrive in the manifest or a cloud command, i.e.
// from the network, and become part of a path this process writes as root, so
// they go through the same validateName/resolveUnder pair as module and
// profile names before they can touch the filesystem.
func updateStagingPath(name, version string) (string, error) {
	if err := validateName("component", name); err != nil {
		return "", err
	}
	if err := validateName("version", version); err != nil {
		return "", err
	}
	return resolveUnder(UpdateDir, name+"-"+version)
}

// getFromUpdateOrigin validates a URL and fetches it with the guarded client.
// The caller closes the body. The returned body aborts the request if reads
// stop making progress for updateStallTimeout.
func getFromUpdateOrigin(rawURL string) (*http.Response, error) {
	if err := validateUpdateURL(rawURL); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := updateHTTPClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	guard := &stallGuardBody{body: resp.Body, cancel: cancel}
	guard.timer = time.AfterFunc(updateStallTimeout, cancel)
	resp.Body = guard
	return resp, nil
}

// stallGuardBody cancels its request when reads make no progress for
// updateStallTimeout. Each successful read pushes the deadline out, so a slow
// but progressing transfer runs to completion while a stalled one is cut off.
type stallGuardBody struct {
	body   io.ReadCloser
	timer  *time.Timer
	cancel context.CancelFunc
}

func (b *stallGuardBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.timer.Reset(updateStallTimeout)
	}
	return n, err
}

func (b *stallGuardBody) Close() error {
	b.timer.Stop()
	b.cancel()
	return b.body.Close()
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

	// Never follow an existing symlink at dest: remove whatever sits there
	// and insist on creating a fresh regular file. With O_EXCL the open fails
	// on anything planted at the path between the remove and the open, so
	// this root-owned write cannot be redirected to wherever a link points.
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
