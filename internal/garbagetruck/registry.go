package garbagetruck

import (
	"time"

	"github.com/google/go-containerregistry/pkg/name"
)

// Image is one image in a registry and every version the registry stores for
// it. It is the registry's side of the comparison the planner performs, in
// plain values, so that planning needs no registry client.
type Image struct {
	Repo     name.Repository
	Versions []Version
}

// Version is one stored version of an image: the digest that identifies its
// content, every tag currently pointing at it, and when the registry first
// held it.
type Version struct {
	Digest string
	Tags   []string
	// Created is when the version entered the registry. Retention measures
	// age from it, because that is what Artifact Registry's own cleanup
	// policies measure: "the minimum time since the version of an artifact
	// was created in the repository". A zero value means the registry did not
	// report one, and an undated version is never deleted.
	Created time.Time
}

// asWritten keeps an unqualified reference spelled the way the manifest wrote
// it. Without it go-containerregistry canonicalises "alpine" to
// "index.docker.io/library/alpine", which is correct but is not what the
// manifest says, and this tool reports what the manifest says.
func asWritten() name.Option { return name.WithDefaultRegistry("") }
