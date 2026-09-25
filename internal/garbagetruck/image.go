// Package garbagetruck holds the vocabulary every garbagetruck command shares:
// container image references, the set of them a GitOps repository declares to
// be in use, what a registry currently stores, and the plan that reconciles the
// two. That set is the source of truth for what must never be deleted, so this
// package performs no I/O and talks to no registry.
package garbagetruck

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// ErrMalformedRef is returned by ParseRef when its input cannot name an image.
var ErrMalformedRef = errors.New("malformed image reference")

// Ref is one container image reference: a repository, plus the tag and the
// digest that select a version of it. A manifest usually writes one or the
// other, and may write both as "repo:tag@sha256:...".
type Ref struct {
	// Repo carries the registry, namespace, and image name separately, e.g.
	// registry "us-central1-docker.pkg.dev" and repository
	// "khan-academy/districts-jobs/ltv2-to-assessments". It is left out of
	// JSON because Refs.ByImage keys on it.
	Repo name.Repository `json:"-"`
	// Tag is the mutable label, e.g. "webapp-057cabe...". Empty only when the
	// reference is pinned by digest and carries no tag.
	Tag string `json:"tag,omitempty"`
	// Digest is the immutable content hash, e.g. "sha256:0f2a...". Empty
	// unless the reference is digest-pinned.
	Digest string `json:"digest,omitempty"`
}

// Refs is a collection of image references.
type Refs []Ref

// ParseRef splits a container image reference into its repository and the tag
// and digest that select a version of it. A reference carrying neither
// resolves to the "latest" tag, because that is the image Docker and
// Kubernetes actually pull for it.
//
// A reference written as "repo:tag@sha256:..." keeps both parts. Parsing it
// with go-containerregistry alone would not: name.Digest accepts that spelling
// but exposes only the repository and the digest, discarding the tag.
//
// It returns ErrMalformedRef when the repository, tag, or digest is not valid.
func ParseRef(s string) (Ref, error) {
	text := strings.TrimSpace(s)
	if text == "" {
		return Ref{}, fmt.Errorf("%q: %w", s, ErrMalformedRef)
	}
	base, _, pinned := strings.Cut(text, "@")
	if !pinned {
		tag, err := name.NewTag(base, asWritten())
		if err != nil {
			return Ref{}, fmt.Errorf("%q: %w: %w", s, ErrMalformedRef, err)
		}
		return Ref{Repo: tag.Context(), Tag: tag.TagStr()}, nil
	}

	digest, err := name.NewDigest(text, asWritten())
	if err != nil {
		return Ref{}, fmt.Errorf("%q: %w: %w", s, ErrMalformedRef, err)
	}
	ref := Ref{Repo: digest.Context(), Digest: digest.DigestStr()}
	if !hasWrittenTag(base) {
		return ref, nil
	}
	// name.NewDigest silently ignores a tag it cannot parse, so validate the
	// one that was written rather than trusting it.
	tag, err := name.NewTag(base, asWritten())
	if err != nil {
		return Ref{}, fmt.Errorf("%q: %w: %w", s, ErrMalformedRef, err)
	}
	ref.Tag = tag.TagStr()
	return ref, nil
}

// Compare orders references by image name, then tag, then digest, so that a
// sorted slice groups every reference to an image together.
func (r Ref) Compare(other Ref) int {
	if c := strings.Compare(r.Repo.Name(), other.Repo.Name()); c != 0 {
		return c
	}
	if c := strings.Compare(r.Tag, other.Tag); c != 0 {
		return c
	}
	return strings.Compare(r.Digest, other.Digest)
}

// String renders the reference the way a manifest would write it.
func (r Ref) String() string {
	out := r.Repo.Name()
	if r.Tag != "" {
		out += ":" + r.Tag
	}
	if r.Digest != "" {
		out += "@" + r.Digest
	}
	return out
}

// ByImage groups the references by image name, mapping each name to its
// sorted, de-duplicated references:
//
//	map[string]Refs{
//		"us-central1-docker.pkg.dev/khan-academy/districts-jobs/cedar_umi_changed": {
//			{Tag: "webapp-034d2665381d9ab1eed1784a784c614be173f573"},
//			{Tag: "webapp-057cabe8414d1a7723deef50117707cd8e35b982"},
//		},
//	}
//
// A digest-pinned reference carries its digest in its own field rather than
// being flattened in beside the tags.
func (refs Refs) ByImage() map[string]Refs {
	byImage := make(map[string]Refs)
	for _, ref := range refs.Sorted() {
		image := ref.Repo.Name()
		byImage[image] = append(byImage[image], ref)
	}
	return byImage
}

// Sorted returns the references in Compare order with duplicates removed.
func (refs Refs) Sorted() Refs {
	sorted := slices.Clone(refs)
	slices.SortFunc(sorted, Ref.Compare)
	return slices.Compact(sorted)
}

// hasWrittenTag reports whether a reference literally carries a ":tag", as
// opposed to leaving it implied. A colon before the final slash is a registry
// port, so "localhost:5000/team/app" carries no tag.
func hasWrittenTag(base string) bool {
	colon := strings.LastIndex(base, ":")
	return colon >= 0 && colon > strings.LastIndex(base, "/")
}
