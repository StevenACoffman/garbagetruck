package garbagetruck_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
)

const roster = districtsJobs + "/roster"

// version is one stored version in a test fixture: a digest, its tags, and
// when the registry took it. A zero created means the registry reported none.
type version struct {
	digest  string
	tags    []string
	created time.Time
}

// image is one registry image in a test fixture, written as the manifests
// spell it so the fixtures read like the data they stand for.
type image struct {
	name     string
	versions []version
}

func TestPlanProtectionCreatesMissingTags(t *testing.T) {
	t.Parallel()

	plan := garbagetruck.PlanProtection(
		parseRefs(t, roster+":"+webappTag),
		images(t, image{roster, []version{{digest: digestA, tags: []string{webappTag}}}}),
	)

	wantCreate := []string{roster + " protected-" + webappTag + " -> " + digestA}
	if got := changeStrings(plan.Create); !slices.Equal(got, wantCreate) {
		t.Errorf("Create = %v, want %v", got, wantCreate)
	}
	if len(plan.Move) != 0 || len(plan.Remove) != 0 || len(plan.Problems) != 0 {
		t.Errorf("plan should only create: %+v", plan)
	}
}

func TestPlanProtectionLeavesCorrectTagsAlone(t *testing.T) {
	t.Parallel()

	plan := garbagetruck.PlanProtection(
		parseRefs(t, roster+":"+webappTag),
		images(t, image{roster, []version{
			{digest: digestA, tags: []string{webappTag, "protected-" + webappTag}},
		}}),
	)

	if !plan.IsEmpty() {
		t.Errorf("plan should be empty, got %+v", plan)
	}
}

func TestPlanProtectionMovesAStaleTag(t *testing.T) {
	t.Parallel()

	// The manifest's tag was rebuilt onto a new digest, and the protected tag
	// still points at the old one. Moving it keeps protection unbroken, where
	// a remove-then-create would leave a window with neither version safe.
	plan := garbagetruck.PlanProtection(
		parseRefs(t, roster+":"+webappTag),
		images(t, image{roster, []version{
			{digest: digestA, tags: []string{"protected-" + webappTag}},
			{digest: digestB, tags: []string{webappTag}},
		}}),
	)

	wantMove := []string{roster + " protected-" + webappTag + " -> " + digestB}
	if got := changeStrings(plan.Move); !slices.Equal(got, wantMove) {
		t.Errorf("Move = %v, want %v", got, wantMove)
	}
	if len(plan.Create) != 0 || len(plan.Remove) != 0 {
		t.Errorf("a move must not also create or remove: %+v", plan)
	}
}

func TestPlanProtectionRemovesUnearnedTags(t *testing.T) {
	t.Parallel()

	plan := garbagetruck.PlanProtection(
		parseRefs(t, roster+":"+webappTag),
		images(t, image{roster, []version{
			{digest: digestA, tags: []string{webappTag, "protected-" + webappTag}},
			{digest: digestB, tags: []string{olderTag, "protected-" + olderTag}},
		}}),
	)

	wantRemove := []string{roster + " protected-" + olderTag + " -> " + digestB}
	if got := changeStrings(plan.Remove); !slices.Equal(got, wantRemove) {
		t.Errorf("Remove = %v, want %v", got, wantRemove)
	}
	if len(plan.Create) != 0 || len(plan.Move) != 0 {
		t.Errorf("plan should only remove: %+v", plan)
	}
}

func TestPlanProtectionNeverTouchesUnprefixedTags(t *testing.T) {
	t.Parallel()

	// Only "protected-" tags belong to garbagetruck. Everything else was put
	// there by whoever pushed the image and must survive untouched.
	plan := garbagetruck.PlanProtection(
		nil,
		images(t, image{roster, []version{
			{digest: digestA, tags: []string{webappTag, "latest", "v1.2.3"}},
		}}),
	)

	if !plan.IsEmpty() {
		t.Errorf("plan must not touch unprefixed tags, got %+v", plan)
	}
}

func TestPlanProtectionTagsDigestPinnedReferences(t *testing.T) {
	t.Parallel()

	plan := garbagetruck.PlanProtection(
		parseRefs(t, roster+"@"+digestA),
		images(t, image{roster, []version{{digest: digestA, tags: nil}}}),
	)

	want := []string{roster + " " + garbagetruck.DigestOnlyTag + " -> " + digestA}
	if got := changeStrings(plan.Create); !slices.Equal(got, want) {
		t.Errorf("Create = %v, want %v", got, want)
	}
}

func TestPlanProtectionReportsDigestOnlyConflict(t *testing.T) {
	t.Parallel()

	// Two digest-pinned references to one image both need the single
	// "protected-digest-only" name; only one version can carry it.
	plan := garbagetruck.PlanProtection(
		parseRefs(t, roster+"@"+digestA, roster+"@"+digestB),
		images(
			t,
			image{roster, []version{{digest: digestA, tags: nil}, {digest: digestB, tags: nil}}},
		),
	)

	want := []string{roster + " " + garbagetruck.DigestOnlyTag + " -> " + digestA}
	if got := changeStrings(plan.Create); !slices.Equal(got, want) {
		t.Errorf("Create = %v, want %v (the lower digest wins, deterministically)", got, want)
	}
	if len(plan.Problems) != 1 || plan.Problems[0].Reason != garbagetruck.DigestOnlyConflict {
		t.Fatalf("Problems = %+v, want one DigestOnlyConflict", plan.Problems)
	}
	if plan.Problems[0].Ref.Digest != digestB {
		t.Errorf("the losing reference should be reported, got %v", plan.Problems[0].Ref)
	}
}

func TestPlanProtectionReportsAMissingVersion(t *testing.T) {
	t.Parallel()

	// The image is in the registry but the pinned tag is not: the cluster is
	// running something that has already been deleted.
	plan := garbagetruck.PlanProtection(
		parseRefs(t, roster+":"+webappTag),
		images(t, image{roster, []version{{digest: digestA, tags: []string{olderTag}}}}),
	)

	if !plan.IsEmpty() {
		t.Errorf("nothing can be tagged, got %+v", plan)
	}
	if len(plan.Problems) != 1 || plan.Problems[0].Reason != garbagetruck.NotInRegistry {
		t.Fatalf("Problems = %+v, want one NotInRegistry", plan.Problems)
	}
}

func TestPlanProtectionIgnoresImagesOutsideTheRegistry(t *testing.T) {
	t.Parallel()

	// Manifests reference public images too. Those are out of scope, and must
	// not be reported as problems.
	plan := garbagetruck.PlanProtection(
		parseRefs(t, "alpine:3.20", "cgr.dev/chainguard/kubectl:latest", roster+":"+webappTag),
		images(t, image{roster, []version{{digest: digestA, tags: []string{webappTag}}}}),
	)

	if len(plan.Problems) != 0 {
		t.Errorf("Problems = %+v, want none", plan.Problems)
	}
	if len(plan.Create) != 1 {
		t.Errorf("Create = %v, want just the in-registry image", changeStrings(plan.Create))
	}
}

func TestPlanChangesOrdersMovesAndCreatesBeforeRemoves(t *testing.T) {
	t.Parallel()

	plan := garbagetruck.PlanProtection(
		parseRefs(t, roster+":"+webappTag, roster+":"+olderTag),
		images(t, image{
			roster,
			[]version{
				{
					digest: digestA,
					tags:   []string{webappTag, "protected-" + olderTag, "protected-stale"},
				},
				{digest: digestB, tags: []string{olderTag}},
			},
		}),
	)

	var kinds []string
	for _, change := range plan.Changes() {
		kinds = append(kinds, change.Tag)
	}
	want := []string{
		"protected-" + olderTag,  // Move: onto digestB, where olderTag lives
		"protected-" + webappTag, // Create: digestA has no protected tag for it
		"protected-stale",        // Remove: no manifest reference earns it
	}
	if !slices.Equal(kinds, want) {
		t.Errorf("Changes() = %v, want %v", kinds, want)
	}
}

// images turns the compact fixture form into the registry inventory the
// planner consumes.
func images(t *testing.T, specs ...image) []garbagetruck.Image {
	t.Helper()

	built := make([]garbagetruck.Image, 0, len(specs))
	for _, spec := range specs {
		versions := make([]garbagetruck.Version, 0, len(spec.versions))
		for _, v := range spec.versions {
			versions = append(versions, garbagetruck.Version{
				Digest: v.digest, Tags: v.tags, Created: v.created,
			})
		}
		built = append(built, garbagetruck.Image{
			Repo: repo(t, spec.name), Versions: versions,
		})
	}
	return built
}

// repo parses an image name the way the domain does.
func repo(t *testing.T, image string) name.Repository {
	t.Helper()

	parsed, err := name.NewRepository(image, name.WithDefaultRegistry(""))
	if err != nil {
		t.Fatalf("NewRepository(%q): %v", image, err)
	}
	return parsed
}

// changeStrings renders changes so a failure names them instead of printing
// nested structs.
func changeStrings(changes []garbagetruck.Change) []string {
	var out []string
	for _, change := range changes {
		out = append(out, change.Repo.Name()+" "+change.Tag+" -> "+change.Digest)
	}
	return out
}
