// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"testing"
	"time"

	"github.com/Serraniel/neptide/pkg/engine"
	"github.com/Serraniel/neptide/pkg/version"
)

func TestContainerConfigDropsImageDefaults(t *testing.T) {
	old := &engine.Container{
		ID: "0123456789abcdef",
		Config: map[string]any{
			"Image":    "app:1",
			"Hostname": "0123456789ab",
			"Cmd":      []any{"serve"},            // image default
			"User":     "1000",                    // set by user
			"Env":      []any{"PATH=/bin", "A=1"}, // PATH from image
			"Labels":   map[string]any{"org.opencontainers.image.version": "1", "neptide.enable": "true"},
		},
	}
	img := &engine.Image{Config: map[string]any{
		"Cmd":    []any{"serve"},
		"Env":    []any{"PATH=/bin"},
		"Labels": map[string]any{"org.opencontainers.image.version": "1"},
	}}
	cfg := containerConfig(old, img, "app:2")
	if cfg["Image"] != "app:2" {
		t.Errorf("image = %v", cfg["Image"])
	}
	if _, ok := cfg["Cmd"]; ok {
		t.Error("image default Cmd was kept; the new image's CMD would be ignored")
	}
	if _, ok := cfg["Hostname"]; ok {
		t.Error("generated hostname was kept")
	}
	if cfg["User"] != "1000" {
		t.Error("user setting lost")
	}
	if env := cfg["Env"].([]any); len(env) != 1 || env[0] != "A=1" {
		t.Errorf("env = %v", env)
	}
	if l := cfg["Labels"].(map[string]any); len(l) != 1 || l["neptide.enable"] != "true" {
		t.Errorf("labels = %v", l)
	}
}

func TestEndpointsSkipSpecialNetworkModes(t *testing.T) {
	ct := &engine.Container{ID: "0123456789abcdef", HostConfig: map[string]any{"NetworkMode": "host"}}
	if endpoints(ct) != nil {
		t.Error("host networking must not get endpoints")
	}
	ct.HostConfig["NetworkMode"] = "app_default"
	ct.NetworkSettings.Networks = map[string]map[string]any{
		"app_default": {"Aliases": []any{"web", "0123456789ab"}, "IPAddress": "172.18.0.2"},
	}
	ep := endpoints(ct)["app_default"]
	if a := ep["Aliases"].([]any); len(a) != 1 || a[0] != "web" {
		t.Errorf("aliases = %v", a)
	}
	if _, ok := ep["IPAddress"]; ok {
		t.Error("runtime IP must not be copied")
	}
}

func TestPolicyFrom(t *testing.T) {
	def := Policy{Track: version.Digest, Mode: ModeUpdate, HealthTimeout: time.Minute}
	p, err := PolicyFrom(map[string]string{LabelEnable: "true", LabelTrack: "minor", LabelMode: "notify", LabelHealthTimeout: "5m"}, def)
	if err != nil || !p.Enabled || p.Track != version.Minor || p.Mode != ModeNotify || p.HealthTimeout != 5*time.Minute {
		t.Fatalf("got %+v, %v", p, err)
	}
	for _, bad := range []map[string]string{{LabelEnable: "ja"}, {LabelTrack: "all"}, {LabelMode: "yolo"}, {LabelHealthTimeout: "-1s"}} {
		if _, err := PolicyFrom(bad, def); err == nil {
			t.Errorf("%v: expected error", bad)
		}
	}
}
