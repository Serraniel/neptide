// SPDX-License-Identifier: AGPL-3.0-or-later

package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DockerConfig holds the credential parts of a Docker config.json: inline
// `auths`, a default `credsStore` and per-registry `credHelpers`.
type DockerConfig struct {
	Auths map[string]struct {
		Auth          string `json:"auth"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		IdentityToken string `json:"identitytoken"`
	} `json:"auths"`
	// CredsStore is the helper for every registry, e.g. "pass" runs
	// docker-credential-pass.
	CredsStore string `json:"credsStore"`
	// CredHelpers picks a helper per registry host, e.g. {"123.dkr.ecr.eu-central-1.amazonaws.com": "ecr-login"}.
	CredHelpers map[string]string `json:"credHelpers"`

	// Log receives helper failures (may be nil).
	Log func(msg string) `json:"-"`
	// Run executes a helper; tests replace it. Defaults to running
	// docker-credential-<name> get from PATH.
	Run func(ctx context.Context, helper, server string) ([]byte, error) `json:"-"`

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	c   *Credentials
	exp time.Time
}

// helperTTL bounds how long helper answers are reused. Helpers such as
// ecr-login hand out short-lived tokens, so do not keep them for a whole
// run, but do not exec a helper for every registry request either.
const helperTTL = 5 * time.Minute

var helperName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

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
	for host, h := range cfg.CredHelpers {
		if !helperName.MatchString(h) {
			return nil, fmt.Errorf("credHelpers[%s]: invalid helper name %q", host, h)
		}
	}
	if cfg.CredsStore != "" && !helperName.MatchString(cfg.CredsStore) {
		return nil, fmt.Errorf("credsStore: invalid helper name %q", cfg.CredsStore)
	}
	return &cfg, nil
}

// spellings are the keys Docker may store credentials for host under.
func spellings(host string) []string {
	want := []string{host, "https://" + host, "http://" + host}
	if host == DockerHub {
		want = append(want, "https://index.docker.io/v1/", "index.docker.io", "docker.io")
	}
	return want
}

// Lookup returns credentials for host, as Docker resolves them: a
// credHelpers entry for the host, else the credsStore, else inline auths.
func (c *DockerConfig) Lookup(host string) *Credentials {
	if c == nil {
		return nil
	}
	if helper, server := c.helperFor(host); helper != "" {
		if creds := c.fromHelper(helper, server); creds != nil {
			return creds
		}
	}
	for key, a := range c.Auths {
		k := strings.TrimSuffix(key, "/")
		for _, w := range spellings(host) {
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

// helperFor returns the helper for host and the server name to ask it for.
func (c *DockerConfig) helperFor(host string) (helper, server string) {
	server = host
	if host == DockerHub {
		server = "https://index.docker.io/v1/" // what docker login stores
	}
	for key, h := range c.CredHelpers {
		k := strings.TrimSuffix(key, "/")
		for _, w := range spellings(host) {
			if k == strings.TrimSuffix(w, "/") {
				return h, key
			}
		}
	}
	return c.CredsStore, server
}

func (c *DockerConfig) fromHelper(helper, server string) *Credentials {
	key := helper + "\x00" + server
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.cache[key]; ok && time.Now().Before(e.exp) {
		return e.c
	}
	creds, err := c.ask(helper, server)
	if err != nil && c.Log != nil {
		c.Log(fmt.Sprintf("credential helper docker-credential-%s for %s: %v", helper, server, err))
	}
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	c.cache[key] = cached{creds, time.Now().Add(helperTTL)}
	return creds
}

// errNotFound is the helper protocol's answer for unknown servers.
var errNotFound = errors.New("credentials not found in native keychain")

func (c *DockerConfig) ask(helper, server string) (*Credentials, error) {
	run := c.Run
	if run == nil {
		run = runHelper
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := run(ctx, helper, server)
	if err != nil {
		if strings.Contains(string(out), errNotFound.Error()) {
			return nil, nil // nothing stored: anonymous, as docker does
		}
		return nil, err
	}
	var resp struct {
		Username string `json:"Username"`
		Secret   string `json:"Secret"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("unreadable answer: %w", err)
	}
	if resp.Username == "<token>" {
		return nil, errors.New("the helper returned an identity token, which neptide does not support yet; use a username and password or token")
	}
	if resp.Secret == "" {
		return nil, nil
	}
	return &Credentials{resp.Username, resp.Secret}, nil
}

func runHelper(ctx context.Context, helper, server string) ([]byte, error) {
	bin, err := exec.LookPath("docker-credential-" + helper)
	if err != nil {
		return nil, fmt.Errorf("not found on PATH: %w", err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "get") //nolint:gosec // helper name is validated, as docker runs it
	cmd.Stdin = strings.NewReader(server)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stdout.String() + " " + stderr.String())
		return []byte(msg), fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), nil
}
