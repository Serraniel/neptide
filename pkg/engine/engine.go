// SPDX-License-Identifier: AGPL-3.0-or-later

// Package engine is a minimal Docker Engine API client: just what updating
// containers and swarm services needs, over the unix socket or DOCKER_HOST.
//
// Container and service specs are kept as raw JSON maps, so fields this
// package does not know about survive a recreate or update unchanged.
package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// APIVersion is the Engine API version used; Docker 25 and newer support it.
const APIVersion = "v1.44"

// Client talks to one Docker daemon.
type Client struct {
	http *http.Client
	base string
}

// NotFoundError is returned for 404 responses.
type NotFoundError struct{ What string }

func (e NotFoundError) Error() string { return e.What + ": not found" }

// IsNotFound reports whether err is a NotFoundError.
func IsNotFound(err error) bool {
	var nf NotFoundError
	return errors.As(err, &nf)
}

// New connects to host: "unix:///var/run/docker.sock", "tcp://h:2375", or ""
// for DOCKER_HOST with the default socket as fallback.
func New(host string) (*Client, error) {
	if host == "" {
		host = os.Getenv("DOCKER_HOST")
	}
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{ResponseHeaderTimeout: 0, IdleConnTimeout: 30 * time.Second}
	c := &Client{http: &http.Client{Transport: tr}}
	switch u.Scheme {
	case "unix":
		path := u.Path
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}
		c.base = "http://docker/" + APIVersion
	case "tcp", "http":
		c.base = "http://" + u.Host + "/" + APIVersion
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST scheme %q (TLS hosts are not supported yet)", u.Scheme)
	}
	return c, nil
}

// RegistryAuth is the X-Registry-Auth payload.
type RegistryAuth struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	ServerAddress string `json:"serveraddress,omitempty"`
}

func (a *RegistryAuth) header() string {
	if a == nil {
		return ""
	}
	b, _ := json.Marshal(a) //nolint:gosec // G117: the daemon needs the password to pull; sent only over the local socket

	return base64.URLEncoding.EncodeToString(b)
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, h http.Header) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 && res.StatusCode != http.StatusNotModified {
		defer func() { _ = res.Body.Close() }()
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(msg, &e) == nil && e.Message != "" {
			msg = []byte(e.Message)
		}
		if res.StatusCode == http.StatusNotFound {
			return nil, NotFoundError{What: fmt.Sprintf("%s %s (%s)", method, path, strings.TrimSpace(string(msg)))}
		}
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return res, nil
}

func (c *Client) json(ctx context.Context, method, path string, q url.Values, body, out any) error {
	res, err := c.do(ctx, method, path, q, body, nil)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// SwarmActive reports whether this daemon is a swarm manager.
func (c *Client) SwarmActive(ctx context.Context) (manager bool, err error) {
	var info struct {
		Swarm struct {
			LocalNodeState   string `json:"LocalNodeState"`
			ControlAvailable bool   `json:"ControlAvailable"`
		} `json:"Swarm"`
	}
	if err := c.json(ctx, http.MethodGet, "/info", nil, nil, &info); err != nil {
		return false, err
	}
	return info.Swarm.LocalNodeState == "active" && info.Swarm.ControlAvailable, nil
}

func labelFilter(labels ...string) url.Values {
	f, _ := json.Marshal(map[string][]string{"label": labels})
	return url.Values{"filters": {string(f)}}
}

// DockerRoot returns the daemon's data directory.
func (c *Client) DockerRoot(ctx context.Context) (string, error) {
	var info struct {
		DockerRootDir string `json:"DockerRootDir"`
	}
	err := c.json(ctx, http.MethodGet, "/info", nil, nil, &info)
	return info.DockerRootDir, err
}

// ContainerSummary is one entry of GET /containers/json.
type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	// HostConfig.NetworkMode is "container:<id>" for a container that
	// shares another one's network namespace.
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
}

// Containers lists running containers carrying all given labels (k or k=v).
func (c *Client) Containers(ctx context.Context, labels ...string) ([]ContainerSummary, error) {
	var out []ContainerSummary
	q := labelFilter(labels...)
	return out, c.json(ctx, http.MethodGet, "/containers/json", q, nil, &out)
}

// AllContainers lists every container, stopped ones included.
func (c *Client) AllContainers(ctx context.Context) ([]ContainerSummary, error) {
	var out []ContainerSummary
	return out, c.json(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"true"}}, nil, &out)
}

// Container is the subset of GET /containers/{id}/json we read, plus the raw
// Config and HostConfig maps we pass back on recreate.
type Container struct {
	ID              string         `json:"Id"`
	Name            string         `json:"Name"`
	Image           string         `json:"Image"` // image ID
	Config          map[string]any `json:"Config"`
	HostConfig      map[string]any `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]map[string]any `json:"Networks"`
	} `json:"NetworkSettings"`
	State struct {
		Status     string `json:"Status"`
		StartedAt  string `json:"StartedAt"`
		Running    bool   `json:"Running"`
		Restarting bool   `json:"Restarting"`
		ExitCode   int    `json:"ExitCode"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int     `json:"RestartCount"`
	Mounts       []Mount `json:"Mounts"`
}

// Mount is one entry of a container's Mounts.
type Mount struct {
	Type        string `json:"Type"` // volume, bind, tmpfs
	Name        string `json:"Name"` // volume name
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

// ConfigImage is the image reference the container was created from.
func (ct *Container) ConfigImage() string {
	s, _ := ct.Config["Image"].(string)
	return s
}

// Labels returns the container's labels.
func (ct *Container) Labels() map[string]string {
	out := map[string]string{}
	if m, ok := ct.Config["Labels"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

// ManifestDigest returns the platform manifest digest the container was
// created from (Engine API 1.48+, containerd image store), or "" if the
// daemon does not report it.
func (c *Client) ManifestDigest(ctx context.Context, id string) (string, error) {
	var ct struct {
		ImageManifestDescriptor *struct {
			Digest string `json:"digest"`
		} `json:"ImageManifestDescriptor"`
	}
	// Asked at 1.48 explicitly; older daemons reject the version and we report "".
	u := strings.Replace(c.base, "/"+APIVersion, "/v1.48", 1) + "/containers/" + url.PathEscape(id) + "/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&ct) != nil || ct.ImageManifestDescriptor == nil {
		return "", nil
	}
	return ct.ImageManifestDescriptor.Digest, nil
}

// Inspect reads one container.
func (c *Client) Inspect(ctx context.Context, id string) (*Container, error) {
	var ct Container
	return &ct, c.json(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &ct)
}

// Image is the subset of GET /images/{id}/json we read. Config is raw: on
// recreate it tells which container settings were only image defaults.
type Image struct {
	ID          string         `json:"Id"`
	RepoDigests []string       `json:"RepoDigests"`
	Config      map[string]any `json:"Config"`
}

// InspectImage reads one image by ID or reference.
func (c *Client) InspectImage(ctx context.Context, ref string) (*Image, error) {
	var im Image
	return &im, c.json(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, &im)
}

// Pull pulls ref (name:tag or name@digest) and waits for completion.
func (c *Client) Pull(ctx context.Context, ref string, auth *RegistryAuth) error {
	name, tag := ref, ""
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		name, tag = ref[:i], ref[i+1:]
	} else if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	q := url.Values{"fromImage": {name}}
	if tag != "" {
		q.Set("tag", tag)
	}
	h := http.Header{}
	if a := auth.header(); a != "" {
		h.Set("X-Registry-Auth", a)
	}
	res, err := c.do(ctx, http.MethodPost, "/images/create", q, nil, h)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	// Errors arrive in the progress stream, not as HTTP status.
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var msg struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, msg.Error)
		}
	}
	return sc.Err()
}

// Create creates a container from raw config parts and returns its ID.
func (c *Client) Create(ctx context.Context, name string, config, hostConfig map[string]any, networks map[string]map[string]any) (string, error) {
	body := map[string]any{}
	for k, v := range config {
		body[k] = v
	}
	body["HostConfig"] = hostConfig
	if len(networks) > 0 {
		body["NetworkingConfig"] = map[string]any{"EndpointsConfig": networks}
	}
	var out struct {
		ID string `json:"Id"`
	}
	err := c.json(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, body, &out)
	return out.ID, err
}

// Start starts a container.
func (c *Client) Start(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, nil)
}

// Stop stops a container, waiting up to timeout before killing it.
func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{"t": {fmt.Sprint(int(timeout.Seconds()))}}
	return c.json(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop", q, nil, nil)
}

// Rename renames a container.
func (c *Client) Rename(ctx context.Context, id, name string) error {
	return c.json(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/rename", url.Values{"name": {name}}, nil, nil)
}

// Wait blocks until the container stops and returns its exit code.
func (c *Client) Wait(ctx context.Context, id string) (int, error) {
	var out struct {
		StatusCode int `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := c.json(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/wait", nil, nil, &out); err != nil {
		return -1, err
	}
	if out.Error != nil && out.Error.Message != "" {
		return out.StatusCode, errors.New(out.Error.Message)
	}
	return out.StatusCode, nil
}

// Volume is a named volume.
type Volume struct {
	Name    string            `json:"Name"`
	Driver  string            `json:"Driver"`
	Labels  map[string]string `json:"Labels"`
	Options map[string]string `json:"Options"`
}

// InspectVolume reads one volume.
func (c *Client) InspectVolume(ctx context.Context, name string) (*Volume, error) {
	var v Volume
	return &v, c.json(ctx, http.MethodGet, "/volumes/"+url.PathEscape(name), nil, nil, &v)
}

// CreateVolume creates a local volume with labels.
func (c *Client) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	return c.json(ctx, http.MethodPost, "/volumes/create", nil, map[string]any{"Name": name, "Labels": labels}, nil)
}

// Volumes lists volumes carrying all given labels.
func (c *Client) Volumes(ctx context.Context, labels ...string) ([]Volume, error) {
	var out struct {
		Volumes []Volume `json:"Volumes"`
	}
	return out.Volumes, c.json(ctx, http.MethodGet, "/volumes", labelFilter(labels...), nil, &out)
}

// RemoveVolume deletes a volume.
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	return c.json(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, nil, nil)
}

// Remove force-removes a container (not its volumes).
func (c *Client) Remove(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {"true"}}, nil, nil)
}

// Service is the subset of a swarm service we read, with the raw spec.
type Service struct {
	ID      string `json:"ID"`
	Version struct {
		Index uint64 `json:"Index"`
	} `json:"Version"`
	Spec         map[string]any `json:"Spec"`
	UpdateStatus *struct {
		State   string `json:"State"`
		Message string `json:"Message"`
	} `json:"UpdateStatus"`
}

// Name returns the service name.
func (s *Service) Name() string {
	n, _ := s.Spec["Name"].(string)
	return n
}

// Labels returns the service labels.
func (s *Service) Labels() map[string]string {
	out := map[string]string{}
	if m, ok := s.Spec["Labels"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

func (s *Service) containerSpec() map[string]any {
	tt, _ := s.Spec["TaskTemplate"].(map[string]any)
	cs, _ := tt["ContainerSpec"].(map[string]any)
	return cs
}

// Image returns the service's image reference (usually name:tag@digest).
func (s *Service) Image() string {
	img, _ := s.containerSpec()["Image"].(string)
	return img
}

// SetImage changes the image in the raw spec.
func (s *Service) SetImage(ref string) {
	if cs := s.containerSpec(); cs != nil {
		cs["Image"] = ref
	}
}

// Services lists services carrying all given labels.
func (c *Client) Services(ctx context.Context, labels ...string) ([]Service, error) {
	var out []Service
	return out, c.json(ctx, http.MethodGet, "/services", labelFilter(labels...), nil, &out)
}

// InspectService reads one service.
func (c *Client) InspectService(ctx context.Context, id string) (*Service, error) {
	var s Service
	return &s, c.json(ctx, http.MethodGet, "/services/"+url.PathEscape(id), nil, nil, &s)
}

// UpdateService submits spec at the version it was read at.
func (c *Client) UpdateService(ctx context.Context, s *Service, auth *RegistryAuth) error {
	q := url.Values{"version": {fmt.Sprint(s.Version.Index)}}
	h := http.Header{}
	if a := auth.header(); a != "" {
		h.Set("X-Registry-Auth", a)
	}
	res, err := c.do(ctx, http.MethodPost, "/services/"+url.PathEscape(s.ID)+"/update", q, s.Spec, h)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	return nil
}
