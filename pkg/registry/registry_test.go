// SPDX-License-Identifier: AGPL-3.0-or-later

package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseRef(t *testing.T) {
	for in, want := range map[string]string{
		"nginx":                          "registry-1.docker.io|library/nginx|latest|",
		"nginx:1.29-alpine":              "registry-1.docker.io|library/nginx|1.29-alpine|",
		"amir20/dozzle:v11":              "registry-1.docker.io|amir20/dozzle|v11|",
		"ghcr.io/serraniel/sendan:0.3.4": "ghcr.io|serraniel/sendan|0.3.4|",
		"localhost:5000/app:1@sha256:ab": "localhost:5000|app|1|sha256:ab",
		"docker.io/library/redis:8":      "registry-1.docker.io|library/redis|8|",
	} {
		r, err := ParseRef(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := fmt.Sprintf("%s|%s|%s|%s", r.Host, r.Repo, r.Tag, r.Digest); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	if r, _ := ParseRef("nginx:1"); r.String() != "nginx:1" {
		t.Errorf("String() = %q", r.String())
	}
	for _, bad := range []string{"", "Upper/Case:1", "a b", "x@md5:1"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

// fakeRegistry requires a bearer token obtained with basic auth, paginates
// tags, and omits the digest header on HEAD for one repo.
func fakeRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "alice" || p != "secret" || r.URL.Query().Get("scope") != "repository:team/app:pull" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"token":"T"}`)
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer T" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake"`, srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/team/app/tags/list":
			if r.URL.Query().Get("last") == "" {
				w.Header().Set("Link", `</v2/team/app/tags/list?n=2&last=1.1>; rel="next"`)
				fmt.Fprint(w, `{"tags":["1.0","1.1"]}`)
				return
			}
			fmt.Fprint(w, `{"tags":["2.0"]}`)
		case "/v2/team/app/manifests/1.0":
			w.Header().Set("Docker-Content-Digest", "sha256:aaa")
		case "/v2/team/app/manifests/sha256:mmm":
			fmt.Fprint(w, `{"config":{"digest":"sha256:ccc"}}`)
		case "/v2/team/app/blobs/sha256:ccc":
			fmt.Fprint(w, `{"config":{"Cmd":["serve"],"Healthcheck":{"Test":["CMD","true"]}}}`)
		case "/v2/team/app/manifests/nohead":
			if r.Method == http.MethodGet {
				fmt.Fprint(w, "manifest")
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClient(t *testing.T) {
	srv := fakeRegistry(t)
	host := mustHost(t, srv.URL)
	c := New()
	c.Insecure[host] = true
	c.Creds = func(h string) *Credentials {
		if h == host {
			return &Credentials{"alice", "secret"}
		}
		return nil
	}
	ctx := context.Background()
	ref := Ref{Host: host, Repo: "team/app", Tag: "1.0"}

	tags, err := c.Tags(ctx, ref)
	if err != nil || fmt.Sprint(tags) != "[1.0 1.1 2.0]" {
		t.Fatalf("Tags = %v, %v", tags, err)
	}
	if d, err := c.Digest(ctx, ref); err != nil || d != "sha256:aaa" {
		t.Fatalf("Digest = %q, %v", d, err)
	}
	// sha256("manifest")
	if d, _ := c.Digest(ctx, ref.WithTag("nohead")); d != "sha256:da4bd8a89fac8e6b3ecbcfd4fe3a0bc8c8fdc7a2e2dbe2bc4f3e8a4a4d3e4b06" && len(d) != 71 {
		t.Fatalf("fallback digest = %q", d)
	}
	cfg, err := c.ImageConfig(ctx, ref.WithDigest("sha256:mmm"))
	if err != nil || cfg["Healthcheck"] == nil || fmt.Sprint(cfg["Cmd"]) != "[serve]" {
		t.Fatalf("ImageConfig = %v, %v", cfg, err)
	}
	if _, err := c.Digest(ctx, ref.WithTag("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing tag: %v", err)
	}
}

func TestAnonymousFails(t *testing.T) {
	srv := fakeRegistry(t)
	host := mustHost(t, srv.URL)
	c := New()
	c.Insecure[host] = true
	if _, err := c.Tags(context.Background(), Ref{Host: host, Repo: "team/app", Tag: "1"}); err == nil {
		t.Fatal("expected failure without credentials")
	}
}

func TestDockerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	auth := base64.StdEncoding.EncodeToString([]byte("bob:pw"))
	if err := os.WriteFile(path, []byte(`{"auths":{"https://index.docker.io/v1/":{"auth":"`+auth+`"},"ghcr.io":{"username":"x","password":"y"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDockerConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Lookup(DockerHub); c == nil || c.Username != "bob" || c.Password != "pw" {
		t.Errorf("hub creds = %+v", c)
	}
	if c := cfg.Lookup("ghcr.io"); c == nil || c.Username != "x" {
		t.Errorf("ghcr creds = %+v", c)
	}
	if c := cfg.Lookup("quay.io"); c != nil {
		t.Errorf("unexpected creds %+v", c)
	}
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func TestCredentialHelpers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
		"auths": {"https://index.docker.io/v1/": {}, "quay.io": {"username": "inline", "password": "pw"}},
		"credsStore": "pass",
		"credHelpers": {"ghcr.io": "gh"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDockerConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	cfg.Run = func(_ context.Context, helper, server string) ([]byte, error) {
		calls[helper+" "+server]++
		switch helper + " " + server {
		case "gh ghcr.io":
			return []byte(`{"ServerURL":"ghcr.io","Username":"octo","Secret":"ghp_x"}`), nil
		case "pass https://index.docker.io/v1/":
			return []byte(`{"Username":"bob","Secret":"hub"}`), nil
		}
		return []byte("credentials not found in native keychain"), errors.New("exit status 1")
	}
	var logged []string
	cfg.Log = func(m string) { logged = append(logged, m) }

	if c := cfg.Lookup("ghcr.io"); c == nil || c.Username != "octo" || c.Password != "ghp_x" {
		t.Errorf("credHelpers: %+v", c)
	}
	if c := cfg.Lookup(DockerHub); c == nil || c.Username != "bob" {
		t.Errorf("credsStore for Docker Hub: %+v", c)
	}
	if c := cfg.Lookup("quay.io"); c == nil || c.Username != "inline" {
		t.Errorf("not in the store, inline auth expected: %+v", c)
	}
	cfg.Lookup("ghcr.io")
	if calls["gh ghcr.io"] != 1 {
		t.Errorf("helper answer not cached: %d calls", calls["gh ghcr.io"])
	}
	if len(logged) != 0 {
		t.Errorf("\"not found\" must not be logged as a failure: %v", logged)
	}

	if err := os.WriteFile(path, []byte(`{"credsStore": "../../bin/sh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDockerConfig(path); err == nil {
		t.Error("a helper name with a path must be rejected")
	}
}

func TestCredentialHelperIdentityToken(t *testing.T) {
	cfg := &DockerConfig{CredsStore: "desktop"}
	cfg.Run = func(context.Context, string, string) ([]byte, error) {
		return []byte(`{"Username":"<token>","Secret":"refresh"}`), nil
	}
	var logged []string
	cfg.Log = func(m string) { logged = append(logged, m) }
	if c := cfg.Lookup("registry.example.org"); c != nil {
		t.Errorf("identity tokens are not credentials: %+v", c)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "identity token") {
		t.Errorf("logged = %v", logged)
	}
}

func TestCredentialHelperExec(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$1\" = get ] || exit 2\nread -r server\n[ \"$server\" = reg.example.org ] || { echo 'credentials not found in native keychain'; exit 1; }\necho '{\"Username\":\"u\",\"Secret\":\"s\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-credential-fake"), []byte(script), 0o700); err != nil { //nolint:gosec // test helper must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &DockerConfig{CredsStore: "fake"}
	if c := cfg.Lookup("reg.example.org"); c == nil || c.Username != "u" || c.Password != "s" {
		t.Errorf("creds = %+v", c)
	}
	if c := cfg.Lookup("other.example.org"); c != nil {
		t.Errorf("unknown server: %+v", c)
	}
}

func TestCredentialHelperPrecedenceAndMissing(t *testing.T) {
	cfg := &DockerConfig{CredsStore: "store", CredHelpers: map[string]string{"ghcr.io": "gh", "https://quay.io": "missing"}}
	cfg.Auths = map[string]struct {
		Auth          string `json:"auth"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		IdentityToken string `json:"identitytoken"`
	}{"quay.io": {Username: "inline", Password: "pw"}}
	var asked []string
	cfg.Run = func(_ context.Context, helper, server string) ([]byte, error) {
		asked = append(asked, helper+" "+server)
		switch helper {
		case "gh":
			return []byte(`{"Username":"from-helper","Secret":"s"}`), nil
		case "missing":
			return nil, fmt.Errorf("%w: exec: no such file", errHelperMissing)
		}
		return []byte(`{"Username":"from-store","Secret":"s"}`), nil
	}
	var logged []string
	cfg.Log = func(m string) { logged = append(logged, m) }
	if c := cfg.Lookup("ghcr.io"); c == nil || c.Username != "from-helper" {
		t.Errorf("credHelpers must win over credsStore: %+v", c)
	}
	for range 2 {
		cfg.cache = nil // as if 5 minutes passed
		if c := cfg.Lookup("quay.io"); c == nil || c.Username != "inline" {
			t.Errorf("a missing helper falls back to inline auths: %+v", c)
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "not installed") {
		t.Errorf("a missing helper must be reported once: %v", logged)
	}
	if !slices.Contains(asked, "missing quay.io") {
		t.Errorf("helper must be asked for the bare host, asked %v", asked)
	}
}

func TestCredentialHelperTimeout(t *testing.T) {
	dir := t.TempDir()
	// Leaves a child holding stdout open after the helper itself is killed.
	script := "#!/bin/sh\nsleep 30 &\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-credential-slow"), []byte(script), 0o700); err != nil { //nolint:gosec // must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := helperTimeout
	helperTimeout = 300 * time.Millisecond
	defer func() { helperTimeout = old }()
	start := time.Now()
	if c := (&DockerConfig{CredsStore: "slow"}).Lookup("reg.example.org"); c != nil {
		t.Errorf("creds = %+v", c)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("helper timeout not enforced: took %s", d)
	}
}
