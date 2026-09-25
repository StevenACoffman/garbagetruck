package garbagetruck

import "github.com/google/go-containerregistry/pkg/name"

// Image is one image in a registry and every version the registry stores for
// it. It is the registry's side of the comparison the planner performs, in
// plain values, so that planning needs no registry client.
type Image struct {
	Repo     name.Repository
	Versions []Version
}

// Version is one stored version of an image: the digest that identifies its
// content, and every tag currently pointing at it.
type Version struct {
	Digest string
	Tags   []string
}

// asWritten keeps an unqualified reference spelled the way the manifest wrote
// it. Without it go-containerregistry canonicalises "alpine" to
// "index.docker.io/library/alpine", which is correct but is not what the
// manifest says, and this tool reports what the manifest says.
func asWritten() name.Option { return name.WithDefaultRegistry("") }
