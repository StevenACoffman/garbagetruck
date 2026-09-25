package registry_test

import (
	"errors"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/StevenACoffman/garbagetruck/internal/registry"
)

const districtsJobs = "us-central1-docker.pkg.dev/khan-academy/districts-jobs"

func TestParsePrefix(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		input   string
		want    registry.Prefix
		wantErr bool
	}{
		"repository": {
			input: districtsJobs,
			want: registry.Prefix{
				Location: "us-central1", Project: "khan-academy", Repository: "districts-jobs",
			},
		},
		"trailing slash": {
			input: districtsJobs + "/",
			want: registry.Prefix{
				Location: "us-central1", Project: "khan-academy", Repository: "districts-jobs",
			},
		},
		"subpath": {
			input: districtsJobs + "/nested/group",
			want: registry.Prefix{
				Location: "us-central1", Project: "khan-academy",
				Repository: "districts-jobs", Subpath: "nested/group",
			},
		},
		"another location": {
			input: "europe-west1-docker.pkg.dev/p/r",
			want:  registry.Prefix{Location: "europe-west1", Project: "p", Repository: "r"},
		},
		"empty":            {input: "", wantErr: true},
		"host only":        {input: "us-central1-docker.pkg.dev", wantErr: true},
		"no repository":    {input: "us-central1-docker.pkg.dev/khan-academy", wantErr: true},
		"wrong host":       {input: "gcr.io/khan-academy/districts-jobs", wantErr: true},
		"no location":      {input: "-docker.pkg.dev/p/r", wantErr: true},
		"empty project":    {input: "us-central1-docker.pkg.dev//r", wantErr: true},
		"empty repository": {input: "us-central1-docker.pkg.dev/p//x", wantErr: true},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			got, err := registry.ParsePrefix(tc.input)
			if tc.wantErr {
				if !errors.Is(err, registry.ErrBadPrefix) {
					t.Fatalf("ParsePrefix(%q) error = %v, want ErrBadPrefix", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePrefix(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParsePrefix(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPrefixParentAndHost(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	wantParent := "projects/khan-academy/locations/us-central1/repositories/districts-jobs"
	if got := prefix.Parent(); got != wantParent {
		t.Errorf("Parent() = %q, want %q", got, wantParent)
	}
	if got := prefix.Host(); got != "us-central1-docker.pkg.dev" {
		t.Errorf("Host() = %q, want %q", got, "us-central1-docker.pkg.dev")
	}
}

func TestPrefixMatches(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		prefix string
		pkg    string
		want   bool
	}{
		"whole repository takes everything": {districtsJobs, "ltv2-to-assessments", true},
		"whole repository takes nested":     {districtsJobs, "a/b", true},
		"subpath takes itself":              {districtsJobs + "/a", "a", true},
		"subpath takes its children":        {districtsJobs + "/a", "a/b", true},
		"subpath rejects a sibling":         {districtsJobs + "/a", "b", false},
		"subpath is not a string prefix":    {districtsJobs + "/a", "ab", false},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			prefix, err := registry.ParsePrefix(tc.prefix)
			if err != nil {
				t.Fatalf("ParsePrefix(%q): %v", tc.prefix, err)
			}
			if got := prefix.Matches(tc.pkg); got != tc.want {
				t.Errorf("Matches(%q) = %v, want %v", tc.pkg, got, tc.want)
			}
		})
	}
}

func TestPrefixRepoRebuildsTheImageName(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	repo, err := prefix.Repo("ltv2-to-assessments")
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}

	want := districtsJobs + "/ltv2-to-assessments"
	if got := repo.Name(); got != want {
		t.Errorf("Repo().Name() = %q, want %q", got, want)
	}
	if got := repo.RegistryStr(); got != "us-central1-docker.pkg.dev" {
		t.Errorf("RegistryStr() = %q, want the registry host", got)
	}
	wantRepo := "khan-academy/districts-jobs/ltv2-to-assessments"
	if got := repo.RepositoryStr(); got != wantRepo {
		t.Errorf("RepositoryStr() = %q, want %q", got, wantRepo)
	}

	// The image name a manifest writes must round-trip back to the package id
	// the API uses, or Apply would tag the wrong resource.
	roundTrip, err := name.NewRepository(repo.Name(), name.WithDefaultRegistry(""))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	if roundTrip != repo {
		t.Errorf("round trip = %+v, want %+v", roundTrip, repo)
	}
}

func TestPrefixRepoAndPackageRoundTrip(t *testing.T) {
	t.Parallel()

	// Repo and Package are inverses. If they ever disagree, Apply would edit
	// tags on the wrong registry resource, so pin both directions — including
	// a nested package id, whose slashes the API escapes.
	cases := map[string]string{
		"flat":            "ltv2-to-assessments",
		"nested":          "nested/group/image",
		"dots and dashes": "cedar_umi.changed-v2",
	}

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	for label, pkg := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			repo, err := prefix.Repo(pkg)
			if err != nil {
				t.Fatalf("Repo(%q): %v", pkg, err)
			}
			got, err := prefix.Package(repo)
			if err != nil {
				t.Fatalf("Package(%q): %v", repo.Name(), err)
			}
			if got != pkg {
				t.Errorf("Package(Repo(%q)) = %q, want %q", pkg, got, pkg)
			}
		})
	}
}

func TestPrefixPackageRejectsForeignImages(t *testing.T) {
	t.Parallel()

	// A change must never be applied to an image outside the prefix being
	// reconciled, so the mapping refuses rather than guessing.
	cases := map[string]string{
		"another registry":      "gcr.io/khan-academy/districts-jobs/roster",
		"another project":       "us-central1-docker.pkg.dev/other/districts-jobs/roster",
		"another repository":    "us-central1-docker.pkg.dev/khan-academy/other/roster",
		"the repository itself": districtsJobs,
	}

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	for label, image := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			repo, err := name.NewRepository(image, name.WithDefaultRegistry(""))
			if err != nil {
				t.Fatalf("NewRepository(%q): %v", image, err)
			}
			if got, err := prefix.Package(repo); !errors.Is(err, registry.ErrBadPrefix) {
				t.Errorf("Package(%q) = %q, %v; want ErrBadPrefix", image, got, err)
			}
		})
	}
}
