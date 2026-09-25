package garbagetruck

import (
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

const (
	// ProtectedPrefix marks the tags garbagetruck maintains. A registry
	// cleanup policy keeps any version carrying a tag with this prefix, which
	// is the whole mechanism: garbagetruck never deletes anything itself, it
	// decides what the policy must spare.
	ProtectedPrefix = "protected-"

	// DigestOnlyTag protects a version the manifests pin by digest alone.
	// Such a reference has no tag to derive a name from, so every one of them
	// within an image competes for this single name; see DigestOnlyConflict.
	DigestOnlyTag = ProtectedPrefix + "digest-only"

	// NotInRegistry means a manifest pins a version of an image the registry
	// holds, but that exact version is not there — it has already been
	// deleted, or was never pushed. A cluster is referencing something gone.
	NotInRegistry ProblemReason = iota
	// DigestOnlyConflict means two digest-pinned references to one image both
	// need DigestOnlyTag, and a tag can point at only one version. The lower
	// digest wins so that repeated runs do not flap; the other is unprotected.
	DigestOnlyConflict
)

// ProblemReason says why a manifest reference could not be acted on.
type ProblemReason int

// Change is one tag operation on one version of one image.
type Change struct {
	Repo name.Repository
	// Tag always carries ProtectedPrefix; garbagetruck touches no other tags.
	Tag string
	// Digest is the version the tag points at, or must point at.
	Digest string
}

// Problem is a manifest reference the plan could not act on. Problems never
// block the rest of a plan — they are reported so a human can act on them.
type Problem struct {
	Ref    Ref
	Reason ProblemReason
}

// Plan is the set of changes that makes a registry's protected tags agree with
// the GitOps manifests.
//
// Apply Move and Create before Remove. A Move repoints a tag that already
// exists, which the registry does atomically, so a version is never left
// unprotected in between; doing the Removes first would open that window.
type Plan struct {
	// Create holds tags absent from their image entirely.
	Create []Change
	// Move holds tags that exist but point at the wrong version.
	Move []Change
	// Remove holds protected tags whose manifest reference is gone.
	Remove []Change
	// Problems holds manifest references no change could satisfy.
	Problems []Problem
}

// tagKey identifies one protected tag. A tag name is unique within an image,
// which is what lets both the desired and the current state be flat maps from
// this key to a digest, instead of a set of tags per version.
type tagKey struct {
	image string
	tag   string
}

// PlanProtection computes the protected tag changes that would make images
// agree with manifest.
//
// Only images the registry actually holds are considered: the manifests name
// images from registries outside the one being reconciled, and those are
// silently out of scope. An image that is present but is missing the exact
// version a manifest pins is a Problem rather than a silent omission, because
// that case means a running cluster refers to an image that is already gone.
func PlanProtection(manifest Refs, images []Image) Plan {
	repos := reposByImage(images)
	desired, problems := desiredTags(manifest, images)
	current := currentTags(images)

	var plan Plan
	plan.Problems = problems
	for key, digest := range desired {
		change := Change{Repo: repos[key.image], Tag: key.tag, Digest: digest}
		switch existing, tagged := current[key]; {
		case !tagged:
			plan.Create = append(plan.Create, change)
		case existing != digest:
			plan.Move = append(plan.Move, change)
		}
	}
	for key, digest := range current {
		if _, wanted := desired[key]; !wanted {
			plan.Remove = append(plan.Remove,
				Change{Repo: repos[key.image], Tag: key.tag, Digest: digest})
		}
	}

	plan.sort()
	return plan
}

// ProtectedTag is the tag that protects the version a reference selects:
// ProtectedPrefix joined to the manifest's own tag, or DigestOnlyTag when the
// manifest pinned a digest and wrote no tag.
func (r Ref) ProtectedTag() string {
	if r.Tag == "" {
		return DigestOnlyTag
	}
	return ProtectedPrefix + r.Tag
}

// Compare orders changes by image, then tag, then digest.
func (c Change) Compare(other Change) int {
	if n := strings.Compare(c.Repo.Name(), other.Repo.Name()); n != 0 {
		return n
	}
	if n := strings.Compare(c.Tag, other.Tag); n != 0 {
		return n
	}
	return strings.Compare(c.Digest, other.Digest)
}

// String names the reason in the words a report should use.
func (r ProblemReason) String() string {
	switch r {
	case NotInRegistry:
		return "no such version in the registry"
	case DigestOnlyConflict:
		return "another digest already holds " + DigestOnlyTag
	default:
		return "unknown"
	}
}

// IsEmpty reports whether the plan would change nothing. Problems do not make
// a plan non-empty: they describe what cannot be changed, not what would be.
func (p *Plan) IsEmpty() bool {
	return len(p.Create) == 0 && len(p.Move) == 0 && len(p.Remove) == 0
}

// Changes returns every change the plan would make, in application order:
// Move and Create before Remove, so protection is never dropped in between.
func (p *Plan) Changes() []Change {
	all := make([]Change, 0, len(p.Move)+len(p.Create)+len(p.Remove))
	all = append(all, p.Move...)
	all = append(all, p.Create...)
	return append(all, p.Remove...)
}

// sort puts every slice in a stable order, so that two runs over unchanged
// inputs produce byte-identical output and a diff means something changed.
func (p *Plan) sort() {
	slices.SortFunc(p.Create, Change.Compare)
	slices.SortFunc(p.Move, Change.Compare)
	slices.SortFunc(p.Remove, Change.Compare)
	slices.SortFunc(p.Problems, func(a, b Problem) int {
		if n := a.Ref.Compare(b.Ref); n != 0 {
			return n
		}
		return int(a.Reason) - int(b.Reason)
	})
}

// reposByImage indexes the repositories by the name the manifests use.
func reposByImage(images []Image) map[string]name.Repository {
	repos := make(map[string]name.Repository, len(images))
	for _, image := range images {
		repos[image.Repo.Name()] = image.Repo
	}
	return repos
}

// currentTags maps every protected tag the registry holds to the digest it
// points at. Tags without ProtectedPrefix are left out: they belong to whoever
// pushed the image, and garbagetruck must not touch them.
func currentTags(images []Image) map[tagKey]string {
	current := make(map[tagKey]string)
	for _, image := range images {
		for _, version := range image.Versions {
			for _, tag := range version.Tags {
				if strings.HasPrefix(tag, ProtectedPrefix) {
					current[tagKey{image: image.Repo.Name(), tag: tag}] = version.Digest
				}
			}
		}
	}
	return current
}

// desiredTags maps every protected tag the manifests call for to the digest it
// must point at, and reports the references it could not resolve.
func desiredTags(manifest Refs, images []Image) (map[tagKey]string, []Problem) {
	versions := versionsByImage(images)
	desired := make(map[tagKey]string)
	var problems []Problem

	for _, ref := range manifest.Sorted() {
		image := ref.Repo.Name()
		lookup, known := versions[image]
		if !known {
			continue // outside the registry being reconciled
		}
		digest, found := lookup.resolve(ref)
		if !found {
			problems = append(problems, Problem{Ref: ref, Reason: NotInRegistry})
			continue
		}
		key := tagKey{image: image, tag: ref.ProtectedTag()}
		if taken, clash := desired[key]; clash && taken != digest {
			problems = append(problems, Problem{Ref: ref, Reason: DigestOnlyConflict})
			desired[key] = min(taken, digest)
			continue
		}
		desired[key] = digest
	}
	return desired, problems
}
