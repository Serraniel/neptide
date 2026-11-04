// SPDX-License-Identifier: AGPL-3.0-or-later

// Package registry is a small read-only client for OCI distribution
// registries: list tags and resolve a tag to its digest, with anonymous or
// credentialed token authentication.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Accept lists the manifest types whose digest Docker records when pulling,
// index types first so multi-arch images resolve to the index digest.
var Accept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// ErrNotFound is returned when a tag or repository does not exist.
var ErrNotFound = errors.New("not found")

// Credentials authenticate against one registry host.
type Credentials struct {
	Username, Password string
}

// Client talks to registries. The zero value is not usable; use New.
type Client struct {
	HTTP *http.Client
	// Creds returns credentials for a host, or nil for anonymous access.
	Creds func(host string) *Credentials
	// Insecure hosts are spoken to over plain HTTP.
	Insecure map[string]bool
	// UserAgent identifies the client to registries.
	UserAgent string

	mu     sync.Mutex
	tokens map[string]string // host+repo → bearer token
}

// New returns a client with sane timeouts. localhost registries are insecure.
func New() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		Insecure:  map[string]bool{},
		UserAgent: "neptide",
		tokens:    map[string]string{},
	}
}

func (c *Client) base(host string) string {
	h, _, _ := strings.Cut(host, ":")
	if c.Insecure[host] || h == "localhost" || h == "127.0.0.1" {
		return "http://" + host
	}
	return "https://" + host
}

// Tags lists all tags of the repository, following pagination.
func (c *Client) Tags(ctx context.Context, r Ref) ([]string, error) {
	next := fmt.Sprintf("%s/v2/%s/tags/list?n=1000", c.base(r.Host), r.Repo)
	var tags []string
	for next != "" {
		res, err := c.do(ctx, http.MethodGet, next, r, nil)
		if err != nil {
			return nil, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&body)
		_ = res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("tags of %s: %w", r.Name(), err)
		}
		tags = append(tags, body.Tags...)
		next = nextLink(res.Header.Get("Link"), res.Request.URL)
	}
	return tags, nil
}

// Digest resolves r's tag to the digest Docker would pull.
func (c *Client) Digest(ctx context.Context, r Ref) (string, error) {
	u := fmt.Sprintf("%s/v2/%s/manifests/%s", c.base(r.Host), r.Repo, r.Tag)
	h := http.Header{"Accept": {Accept}}
	res, err := c.do(ctx, http.MethodHead, u, r, h)
	if err != nil {
		return "", err
	}
	_ = res.Body.Close()
	if d := res.Header.Get("Docker-Content-Digest"); d != "" {
		return d, nil
	}
	// Some registries omit the header on HEAD: hash the manifest ourselves.
	res, err = c.do(ctx, http.MethodGet, u, r, h)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if d := res.Header.Get("Docker-Content-Digest"); d != "" {
		return d, nil
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, io.LimitReader(res.Body, 8<<20)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil)), nil
}

func (c *Client) do(ctx context.Context, method, u string, r Ref, h http.Header) (*http.Response, error) {
	key := r.Host + "/" + r.Repo
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range h {
			req.Header[k] = v
		}
		req.Header.Set("User-Agent", c.UserAgent)
		c.mu.Lock()
		tok := c.tokens[key]
		c.mu.Unlock()
		switch {
		case strings.HasPrefix(tok, "Basic "):
			req.Header.Set("Authorization", tok)
		case tok != "":
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		switch {
		case res.StatusCode == http.StatusUnauthorized && attempt == 0:
			challenge := res.Header.Get("WWW-Authenticate")
			_ = res.Body.Close()
			tok, err := c.authenticate(ctx, r, challenge)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			c.tokens[key] = tok
			c.mu.Unlock()
			continue
		case res.StatusCode == http.StatusNotFound:
			_ = res.Body.Close()
			return nil, fmt.Errorf("%s: %w", r.String(), ErrNotFound)
		case res.StatusCode >= 300:
			_ = res.Body.Close()
			return nil, fmt.Errorf("%s %s: HTTP %d", method, r.String(), res.StatusCode)
		}
		return res, nil
	}
	return nil, fmt.Errorf("%s: unauthorized", r.String())
}

var paramRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// authenticate answers a WWW-Authenticate challenge (Bearer or Basic).
func (c *Client) authenticate(ctx context.Context, r Ref, challenge string) (string, error) {
	var creds *Credentials
	if c.Creds != nil {
		creds = c.Creds(r.Host)
	}
	scheme, _, _ := strings.Cut(challenge, " ")
	if strings.EqualFold(scheme, "Basic") {
		if creds == nil {
			return "", fmt.Errorf("%s requires credentials", r.Host)
		}
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.SetBasicAuth(creds.Username, creds.Password)
		return req.Header.Get("Authorization"), nil
	}
	params := map[string]string{}
	for _, m := range paramRe.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm := params["realm"]
	if !strings.EqualFold(scheme, "Bearer") || realm == "" {
		return "", fmt.Errorf("%s: unsupported auth challenge %q", r.Host, challenge)
	}
	q := url.Values{"scope": {"repository:" + r.Repo + ":pull"}}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if creds != nil {
		req.SetBasicAuth(creds.Username, creds.Password)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token for %s: HTTP %d", r.Name(), res.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", fmt.Errorf("token for %s: empty response", r.Name())
}

var linkRe = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="?next"?`)

func nextLink(header string, base *url.URL) string {
	m := linkRe.FindStringSubmatch(header)
	if m == nil {
		return ""
	}
	u, err := base.Parse(m[1])
	if err != nil {
		return ""
	}
	return u.String()
}
