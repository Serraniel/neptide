// SPDX-License-Identifier: AGPL-3.0-or-later

package registry

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
)

// DockerConfig holds the `auths` section of a Docker config.json.
// Credential helpers are not supported; use inline auths.
type DockerConfig struct {
	Auths map[string]struct {
		Auth          string `json:"auth"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		IdentityToken string `json:"identitytoken"`
	} `json:"auths"`
}

// LoadDockerConfig reads a config.json. A missing file is an empty config.
func LoadDockerConfig(path string) (*DockerConfig, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &DockerConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg DockerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Lookup returns credentials for host, accepting the key spellings Docker
// writes (https://index.docker.io/v1/ for Docker Hub, bare host, URL).
func (c *DockerConfig) Lookup(host string) *Credentials {
	if c == nil {
		return nil
	}
	want := []string{host, "https://" + host, "http://" + host}
	if host == DockerHub {
		want = append(want, "https://index.docker.io/v1/", "index.docker.io", "docker.io")
	}
	for key, a := range c.Auths {
		k := strings.TrimSuffix(key, "/")
		for _, w := range want {
			if k != strings.TrimSuffix(w, "/") {
				continue
			}
			if a.Username != "" {
				return &Credentials{a.Username, a.Password}
			}
			if dec, err := base64.StdEncoding.DecodeString(a.Auth); err == nil {
				if u, p, ok := strings.Cut(string(dec), ":"); ok {
					return &Credentials{u, p}
				}
			}
		}
	}
	return nil
}
