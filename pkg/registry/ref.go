// SPDX-License-Identifier: AGPL-3.0-or-later

package registry

import (
	"fmt"
	"strings"
)

// DockerHub is the registry host that unqualified image names resolve to.
const DockerHub = "registry-1.docker.io"

// Ref is a parsed image reference.
type Ref struct {
	Host   string // registry-1.docker.io, ghcr.io, localhost:5000
	Repo   string // library/nginx, serraniel/sendan
	Tag    string // may be empty when only a digest is given
	Digest string // sha256:…, may be empty
}

// ParseRef normalises an image reference the way Docker does.
func ParseRef(s string) (Ref, error) {
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return Ref{}, fmt.Errorf("invalid image reference %q", s)
	}
	var r Ref
	name := s
	if i := strings.Index(name, "@"); i >= 0 {
		name, r.Digest = name[:i], name[i+1:]
		if !strings.HasPrefix(r.Digest, "sha256:") {
			return Ref{}, fmt.Errorf("invalid digest in %q", s)
		}
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, r.Tag = name[:i], name[i+1:]
	}
	first, rest, hasSlash := strings.Cut(name, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Host, r.Repo = first, rest
	} else {
		r.Host, r.Repo = DockerHub, name
		if !strings.Contains(r.Repo, "/") {
			r.Repo = "library/" + r.Repo
		}
	}
	if r.Host == "docker.io" || r.Host == "index.docker.io" {
		r.Host = DockerHub
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	if r.Repo == "" || strings.ToLower(r.Repo) != r.Repo {
		return Ref{}, fmt.Errorf("invalid repository in %q", s)
	}
	return r, nil
}

// Name is the repository as Docker displays it (nginx, ghcr.io/a/b).
func (r Ref) Name() string {
	if r.Host == DockerHub {
		return strings.TrimPrefix(r.Repo, "library/")
	}
	return r.Host + "/" + r.Repo
}

// String renders name:tag@digest, leaving out empty parts.
func (r Ref) String() string {
	s := r.Name()
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

// WithTag returns a copy pointing at another tag, without a digest.
func (r Ref) WithTag(tag string) Ref {
	r.Tag, r.Digest = tag, ""
	return r
}

// WithDigest returns a copy pinned to digest.
func (r Ref) WithDigest(d string) Ref {
	r.Digest = d
	return r
}
