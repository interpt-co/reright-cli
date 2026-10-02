package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func exec(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, home, &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsageAndUnknownCommand(t *testing.T) {
	if code, _, errs := exec(t, t.TempDir()); code != 2 || !strings.Contains(errs, "usage") {
		t.Fatalf("no args: %d %q", code, errs)
	}
	if code, _, errs := exec(t, t.TempDir(), "frobnicate"); code != 2 || !strings.Contains(errs, "frobnicate") {
		t.Fatalf("unknown: %d %q", code, errs)
	}
	if code, _, _ := exec(t, t.TempDir(), "doctor", "extra"); code != 2 {
		t.Fatalf("doctor with args: %d", code)
	}
}

func TestInstallNeedsACode(t *testing.T) {
	code, _, errs := exec(t, t.TempDir(), "install")
	if code != 1 || !strings.Contains(errs, "setup code is required") {
		t.Fatalf("%d %q", code, errs)
	}
}

func TestInstallWithoutCompiledInKeyRefuses(t *testing.T) {
	home := t.TempDir()
	code, _, errs := exec(t, home, "install", "--code", "rrs_abcdefghijkl", "--server", "http://127.0.0.1:1")
	if code != 1 || !strings.Contains(errs, "release key") {
		t.Fatalf("%d %q", code, errs)
	}
}

func TestInstallRejectsBadKeyAtBuild(t *testing.T) {
	old := releasePublicKey
	releasePublicKey = "not a key"
	defer func() { releasePublicKey = old }()
	code, _, errs := exec(t, t.TempDir(), "install", "rrs_abcdefghijkl")
	if code != 1 || !strings.Contains(errs, "public key") {
		t.Fatalf("%d %q", code, errs)
	}
}

func TestDoctorExitCodeReflectsFailures(t *testing.T) {
	code, out, _ := exec(t, t.TempDir(), "doctor")
	if code != 1 || !strings.Contains(out, "FAIL  config") || !strings.Contains(out, "failed.") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestUninstallOnCleanHome(t *testing.T) {
	code, out, _ := exec(t, t.TempDir(), "uninstall")
	if code != 0 || !strings.Contains(out, "No install record") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestDisableEnableStatus(t *testing.T) {
	home := t.TempDir()
	if code, out, _ := exec(t, home, "status"); code != 0 || strings.Contains(out, "OFF") {
		t.Fatalf("fresh status: %d %q", code, out)
	}
	if code, out, errs := exec(t, home, "disable", "commit", "--for", "2h"); code != 0 || !strings.Contains(out, "Switched off: commit") {
		t.Fatalf("disable: %d %q %q", code, out, errs)
	}
	_, out, _ := exec(t, home, "status")
	if !strings.Contains(out, "commit   OFF") || !strings.Contains(out, "gh       on") {
		t.Fatalf("status after disable:\n%s", out)
	}
	if code, _, errs := exec(t, home, "enable", "commit"); code != 0 {
		t.Fatalf("enable: %d %q", code, errs)
	}
	if _, out, _ = exec(t, home, "status"); strings.Contains(out, "OFF") {
		t.Fatalf("status after enable:\n%s", out)
	}
}

func TestDisableFlagsAndErrors(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"disable"},
		{"disable", "commits"},
		{"disable", "commit", "--for", "soon"},
		{"disable", "commit", "--for"},
		{"disable", "commit", "--bogus"},
		{"enable", "commit", "--for", "1h"},
	} {
		if code, _, errs := exec(t, home, args...); code != 1 || errs == "" {
			t.Errorf("%v: code %d, stderr %q", args, code, errs)
		}
	}
	if code, out, errs := exec(t, home, "disable", "--dir", "/work/internal", "all", "--for=30m"); code != 0 {
		t.Fatalf("flags before and after the kind: %d %q %q", code, out, errs)
	}
	if _, out, _ := exec(t, home, "status"); !strings.Contains(out, "only in /work/internal") || strings.Contains(out, "email    on") {
		t.Fatalf("scope missing:\n%s", out)
	}
}

func TestVersionCommandAndFlag(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		code, out, _ := exec(t, t.TempDir(), arg)
		if code != 0 || !strings.HasPrefix(out, "reright "+version+" (") {
			t.Fatalf("%s: %d %q", arg, code, out)
		}
	}
}

func TestUpgradeNeedsACompiledInKeyAndRejectsStrayArguments(t *testing.T) {
	old := releasePublicKey
	releasePublicKey = ""
	defer func() { releasePublicKey = old }()
	if code, _, errs := exec(t, t.TempDir(), "upgrade"); code != 1 || !strings.Contains(errs, "no release key") {
		t.Fatalf("no key: %d %q", code, errs)
	}
	if code, _, _ := exec(t, t.TempDir(), "upgrade", "extra"); code == 0 {
		t.Fatal("stray argument accepted")
	}
}
