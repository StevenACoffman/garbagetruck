// Package registry lists and tags Docker images in Google Artifact Registry.
// It is the only package that talks to Google Cloud.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
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

	// maxPolicyID is the longest a cleanup policy id may be. The API says
	// they "must unique within a repository and be under 128 characters in
	// length", so 127 is the last usable length.
	maxPolicyID = 127

	// policyIDDigest is how many hex characters of a scope's digest are kept
	// when a qualified id has to be shortened.
	policyIDDigest = 8
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
//
// The test is a plain string prefix, not a path prefix, so the subpath "ltv2-"
// matches the package "ltv2-extra" as well as "ltv2-/nested". That is
// deliberate: Artifact Registry's cleanup policies scope by
// PackageNamePrefixes, which is documented as "applied on any prefix match"
// and offers no path-aware form. Matching the same way here keeps the set of
// images garbagetruck protects identical to the set a policy it installs would
// delete. A path-aware test would be tidier and would quietly leave
// "ltv2-extra" unprotected but deletable.
func (p Prefix) Matches(pkg string) bool {
	return strings.HasPrefix(pkg, p.Subpath)
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

// PolicyID qualifies a cleanup policy id with the part of this prefix after
// the project, so that two prefixes do not overwrite each other's rules.
//
// Policy ids only have to be unique within a repository, so two different
// repositories never collide however they are named. What does collide is two
// prefixes that share a repository and differ only by subpath: without the
// scope both would write "garbagetruck-delete-old" to the same repository and
// the second would silently replace the first. The scope also makes a rule
// self-describing when someone reads the repository's policies directly.
func (p Prefix) PolicyID(base string) string {
	scope := p.Repository
	if p.Subpath != "" {
		scope += "/" + p.Subpath
	}
	return joinPolicyID(base, scopeSlug(scope))
}

// joinPolicyID joins a base id to a scope within the length the API allows.
func joinPolicyID(base, scope string) string {
	id := base + "-" + scope
	if len(id) <= maxPolicyID {
		return id
	}
	// Truncating alone would let two long scopes collide, which is the one
	// thing the scope exists to prevent, so carry a digest of the whole scope.
	sum := sha256.Sum256([]byte(scope))
	digest := hex.EncodeToString(sum[:])[:policyIDDigest]
	room := maxPolicyID - len(base) - len(digest) - 2 // two joining hyphens
	if room < 0 {
		room = 0
	}
	return base + "-" + scope[:min(room, len(scope))] + "-" + digest
}

// scopeSlug reduces a repository-and-subpath to characters that are safe in
// an id. No character set is documented for policy ids, so this keeps to the
// one Artifact Registry repository names already use.
func scopeSlug(scope string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		default:
			return '-'
		}
	}, scope)
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	return strings.Trim(slug, "-")
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
