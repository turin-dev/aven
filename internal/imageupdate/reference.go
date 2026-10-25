package imageupdate

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var stableVersion = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Reference is the registry address of an image. Tag is always populated unless the
// source is pinned by digest.
type Reference struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
	Original   string
}

func Parse(image string) (Reference, error) {
	r := Reference{Original: strings.TrimSpace(image)}
	if r.Original == "" || strings.ContainsAny(r.Original, " \t\r\n") {
		return r, errors.New("invalid image reference")
	}
	name, digest, hasDigest := strings.Cut(r.Original, "@")
	if hasDigest {
		if digest == "" || strings.Contains(digest, "@") {
			return r, errors.New("invalid image digest")
		}
		r.Digest = digest
	}
	lastSlash := strings.LastIndexByte(name, '/')
	if colon := strings.LastIndexByte(name, ':'); colon > lastSlash {
		r.Tag, name = name[colon+1:], name[:colon]
		if r.Tag == "" {
			return r, errors.New("empty image tag")
		}
	}
	parts := strings.Split(name, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		r.Registry, parts = parts[0], parts[1:]
	} else {
		r.Registry = "docker.io"
	}
	r.Repository = strings.Join(parts, "/")
	if r.Repository == "" || strings.HasPrefix(r.Repository, "/") || strings.Contains(r.Repository, "..") {
		return r, errors.New("invalid image repository")
	}
	if r.Registry == "docker.io" && !strings.Contains(r.Repository, "/") {
		r.Repository = "library/" + r.Repository
	}
	if !hasDigest && r.Tag == "" {
		r.Tag = "latest"
	}
	return r, nil
}

func (r Reference) WithTag(tag string) string {
	return r.Registry + "/" + r.Repository + ":" + tag
}

func (r Reference) DigestRef(digest string) string {
	return r.Registry + "/" + r.Repository + "@" + digest
}

func (r Reference) IsSemver() (major, minor, patch int, ok bool) {
	m := stableVersion.FindStringSubmatch(r.Tag)
	if m == nil {
		return 0, 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	patch, _ = strconv.Atoi(m[3])
	return major, minor, patch, true
}

func (r Reference) VersionTag(major, minor, patch int) string {
	prefix := ""
	if strings.HasPrefix(r.Tag, "v") {
		prefix = "v"
	}
	return fmt.Sprintf("%s%d.%d.%d", prefix, major, minor, patch)
}
