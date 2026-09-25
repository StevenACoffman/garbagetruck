package garbagetruck

import (
	"slices"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
)

// Retention decides which versions of an image survive. It mirrors the
// Artifact Registry cleanup policy garbagetruck installs, so that sweeping
// locally removes what the policy would remove, only sooner and visibly.
type Retention struct {
	// OlderThan removes versions created longer ago than this.
	OlderThan time.Duration
	// KeepNewest spares this many newest versions of every image whatever
	// their age, so an image that stopped being rebuilt does not vanish.
	KeepNewest int
}

// Kept counts the versions Retention spared, by the rule that spared them.
// The rules are tried in the order below and the first match wins, so these
// counts partition the survivors instead of overlapping.
type Kept struct {
	// Protected carried a ProtectedPrefix tag.
	Protected int
	// Newest were among the KeepNewest most recent of their image.
	Newest int
	// TooYoung were created more recently than OlderThan.
	TooYoung int
	// Undated had no creation time, so their age could not be judged.
	Undated int
}

// Expired is one version Retention removes.
type Expired struct {
	Repo    name.Repository
	Digest  string
	Tags    []string
	Created time.Time
}

// Sweep is what Retention decides about one registry listing.
type Sweep struct {
	// Expired holds the versions to delete, oldest first.
	Expired []Expired
	// Kept explains everything else.
	Kept Kept
}

// PlanSweep selects the versions Retention removes from images as of now.
//
// A keep always beats a delete, matching how Artifact Registry evaluates its
// own policies: a version survives if any rule spares it. now is a parameter
// rather than a call to time.Now so that the decision is a pure function of
// its inputs and can be tested at any point in an image's life.
func PlanSweep(images []Image, keep Retention, now time.Time) Sweep {
	cutoff := now.Add(-keep.OlderThan)
	var sweep Sweep
	for _, image := range images {
		sweep.consider(image, keep, cutoff)
	}
	slices.SortFunc(sweep.Expired, func(a, b Expired) int { return a.Compare(&b) })
	return sweep
}

// Compare orders expired versions oldest first, then by image and digest, so
// that a truncated report shows the least controversial deletions first.
func (e *Expired) Compare(other *Expired) int {
	if c := e.Created.Compare(other.Created); c != 0 {
		return c
	}
	if c := strings.Compare(e.Repo.Name(), other.Repo.Name()); c != 0 {
		return c
	}
	return strings.Compare(e.Digest, other.Digest)
}

// String renders the version the way a manifest would name it.
func (e *Expired) String() string { return e.Repo.Name() + "@" + e.Digest }

// IsEmpty reports whether the sweep would delete nothing.
func (s *Sweep) IsEmpty() bool { return len(s.Expired) == 0 }

// consider sorts one image's versions newest first and applies the rules in
// keep-beats-delete order.
func (s *Sweep) consider(image Image, keep Retention, cutoff time.Time) {
	versions := slices.Clone(image.Versions)
	slices.SortStableFunc(versions, newestFirst)

	for rank, version := range versions {
		switch {
		case isProtected(version):
			s.Kept.Protected++
		case rank < keep.KeepNewest:
			s.Kept.Newest++
		case version.Created.IsZero():
			// Never delete a version whose age cannot be established. An
			// undated version is a gap in what the registry told us, not
			// evidence that it is old.
			s.Kept.Undated++
		case !version.Created.Before(cutoff):
			s.Kept.TooYoung++
		default:
			s.Expired = append(s.Expired, Expired{
				Repo:    image.Repo,
				Digest:  version.Digest,
				Tags:    version.Tags,
				Created: version.Created,
			})
		}
	}
}

// isProtected reports whether any tag on the version marks it as in use.
func isProtected(version Version) bool {
	return slices.ContainsFunc(version.Tags, func(tag string) bool {
		return strings.HasPrefix(tag, ProtectedPrefix)
	})
}

// newestFirst orders versions by age, newest first, with the digest breaking
// ties so that two versions created in the same instant rank deterministically.
// Undated versions sort last, so they never displace a dated version from the
// newest-N that spares it.
func newestFirst(a, b Version) int {
	if c := b.Created.Compare(a.Created); c != 0 {
		return c
	}
	return strings.Compare(a.Digest, b.Digest)
}
