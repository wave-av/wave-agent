package main

import "testing"

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
