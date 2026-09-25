// Package registry lists and tags Docker images in Google Artifact Registry.
// It is the only package that talks to Google Cloud.
package registry

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

const (
	// hostSuffix ends every Artifact Registry Docker hostname, and what
	// precedes it is the location: "us-central1-docker.pkg.dev".
	hostSuffix = "-docker.pkg.dev"

	// minPrefixParts is host, project, and repository — the shortest prefix
	// that names an Artifact Registry repository.
	minPrefixParts = 3
)

// ErrBadPrefix is returned for a prefix that does not name an Artifact
// Registry repository.
var ErrBadPrefix = errors.New("unrecognized registry prefix")

// Prefix is a subtree of one Artifact Registry Docker repository, written the
// way a manifest writes an image:
//
//	us-central1-docker.pkg.dev/khan-academy/districts-jobs
//	└── Location ┘ └ Project ┘ └ Repository ┘
//
// Anything after the repository narrows the subtree further, because Artifact
// Registry package names may themselves contain slashes.
type Prefix struct {
	Location   string
	Project    string
	Repository string
	// Subpath narrows the selection to packages beneath it. Empty means the
	// whole repository.
	Subpath string
}

// ParsePrefix reads a prefix from the form a manifest would use.
func ParsePrefix(s string) (Prefix, error) {
	text := strings.Trim(strings.TrimSpace(s), "/")
	parts := strings.Split(text, "/")
	if len(parts) < minPrefixParts {
		return Prefix{}, fmt.Errorf(
			"%q: %w: want <location>%s/<project>/<repository>", s, ErrBadPrefix, hostSuffix)
	}
	location, found := strings.CutSuffix(parts[0], hostSuffix)
	if !found || location == "" {
		return Prefix{}, fmt.Errorf("%q: %w: host must end in %s", s, ErrBadPrefix, hostSuffix)
	}
	if parts[1] == "" || parts[2] == "" {
		return Prefix{}, fmt.Errorf("%q: %w: empty project or repository", s, ErrBadPrefix)
	}
	return Prefix{
		Location:   location,
		Project:    parts[1],
		Repository: parts[2],
		Subpath:    strings.Join(parts[3:], "/"),
	}, nil
}

// Parent is the Artifact Registry resource name of the repository, which is
// the parent every package, version, and tag resource hangs from.
func (p Prefix) Parent() string {
	return fmt.Sprintf("projects/%s/locations/%s/repositories/%s",
		p.Project, p.Location, p.Repository)
}

// Host is the Docker registry hostname images under this prefix are pulled
// from.
func (p Prefix) Host() string { return p.Location + hostSuffix }

// Matches reports whether a package belongs to this prefix's subtree.
func (p Prefix) Matches(pkg string) bool {
	if p.Subpath == "" {
		return true
	}
	return pkg == p.Subpath || strings.HasPrefix(pkg, p.Subpath+"/")
}

// Repo is the image name a manifest would write for a package in this
// repository, parsed so the registry and namespace stay distinguishable.
func (p Prefix) Repo(pkg string) (name.Repository, error) {
	image := strings.Join([]string{p.Host(), p.Project, p.Repository, pkg}, "/")
	repo, err := name.NewRepository(image, name.WithDefaultRegistry(""))
	if err != nil {
		return name.Repository{}, fmt.Errorf("%q: %w: %w", image, ErrBadPrefix, err)
	}
	return repo, nil
}

// Package recovers the Artifact Registry package id from an image repository,
// and in doing so confirms the image really belongs to this prefix. It is the
// inverse of Repo, and the pair decides which registry resource a change is
// applied to.
func (p Prefix) Package(repo name.Repository) (string, error) {
	if registry := repo.RegistryStr(); registry != p.Host() {
		return "", fmt.Errorf("%q: %w: not in %s", repo.Name(), ErrBadPrefix, p.Host())
	}
	pkg, inRepo := strings.CutPrefix(repo.RepositoryStr(), p.Project+"/"+p.Repository+"/")
	if !inRepo || pkg == "" {
		return "", fmt.Errorf("%q: %w: not in %s", repo.Name(), ErrBadPrefix, p.Parent())
	}
	return pkg, nil
}

// packageName is the Artifact Registry resource name of one package. The API
// escapes slashes inside a package id, because the id is one path segment.
func (p Prefix) packageName(pkg string) string {
	return p.Parent() + "/packages/" + strings.ReplaceAll(pkg, "/", "%2F")
}

// versionName is the resource name of one version of a package.
func (p Prefix) versionName(pkg, digest string) string {
	return p.packageName(pkg) + "/versions/" + digest
}

// tagName is the resource name of one tag on a package.
func (p Prefix) tagName(pkg, tag string) string {
	return p.packageName(pkg) + "/tags/" + tag
}
