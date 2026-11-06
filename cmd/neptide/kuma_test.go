// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKumaTargets(t *testing.T) {
	if ts, err := kumaTargets("", "", ""); err != nil || ts != nil {
		t.Errorf("off: %v %v", ts, err)
	}
	if _, err := kumaTargets("", "u", ""); err == nil {
		t.Error("credentials without --kuma-url must be refused")
	}
	if _, err := kumaTargets("kuma:3001", "u", ""); err == nil {
		t.Error("a URL without scheme must be refused")
	}
	t.Setenv("NEPTIDE_KUMA_PASSWORD", "")
	if _, err := kumaTargets("http://kuma:3001", "u", ""); err == nil {
		t.Error("a missing password must be refused")
	}
	file := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(file, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts, err := kumaTargets("https://kuma.example.org/", "u", file)
	if err != nil || len(ts) != 1 || ts[0].Password != "s3cret" || ts[0].URL != "https://kuma.example.org" {
		t.Errorf("targets = %+v, %v", ts, err)
	}
	t.Setenv("NEPTIDE_KUMA_PASSWORD", "fromenv")
	if ts, err := kumaTargets("http://kuma:3001", "u", ""); err != nil || ts[0].Password != "fromenv" {
		t.Errorf("env password: %+v, %v", ts, err)
	}
}
