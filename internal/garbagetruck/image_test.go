package garbagetruck_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
)

const (
	districtsJobs = "us-central1-docker.pkg.dev/khan-academy/districts-jobs"
	umiChanged    = districtsJobs + "/cedar_umi_changed"
	webappTag     = "webapp-057cabe8414d1a7723deef50117707cd8e35b982"
	olderTag      = "webapp-034d2665381d9ab1eed1784a784c614be173f573"
	digestA       = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB       = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestParseRef(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		input      string
		wantImage  string
		wantTag    string
		wantDigest string
		wantErr    bool
	}{
		"tagged": {
			input: umiChanged + ":" + webappTag, wantImage: umiChanged, wantTag: webappTag,
		},
		"registry and namespace are decomposed": {
			input: districtsJobs + "/ltv2-to-assessments:" + olderTag,
			// Repo.Name() rejoins them; the split is asserted separately below.
			wantImage: districtsJobs + "/ltv2-to-assessments", wantTag: olderTag,
		},
		"untagged resolves to latest": {
			input: "alpine", wantImage: "alpine", wantTag: "latest",
		},
		"unqualified image keeps its spelling": {
			input: "alpine:3.20", wantImage: "alpine", wantTag: "3.20",
		},
		"explicit latest": {
			input:     "cgr.dev/chainguard/kubectl:latest",
			wantImage: "cgr.dev/chainguard/kubectl", wantTag: "latest",
		},
		"registry port is not a tag": {
			input:     "localhost:5000/team/app",
			wantImage: "localhost:5000/team/app",
			wantTag:   "latest",
		},
		"registry port with tag": {
			input:     "localhost:5000/team/app:v1.2.3",
			wantImage: "localhost:5000/team/app", wantTag: "v1.2.3",
		},
		"digest pinned carries no tag": {
			input: umiChanged + "@" + digestA, wantImage: umiChanged, wantDigest: digestA,
		},
		"tag and digest are both kept": {
			input:     umiChanged + ":" + webappTag + "@" + digestA,
			wantImage: umiChanged, wantTag: webappTag, wantDigest: digestA,
		},
		"surrounding space is trimmed": {
			input: "  alpine:3.20  ", wantImage: "alpine", wantTag: "3.20",
		},
		"empty":           {input: "", wantErr: true},
		"blank":           {input: "   ", wantErr: true},
		"empty tag":       {input: "alpine:", wantErr: true},
		"empty digest":    {input: "alpine@", wantErr: true},
		"digest only":     {input: "@" + digestA, wantErr: true},
		"short digest":    {input: "alpine@sha256:abc123", wantErr: true},
		"unknown algo":    {input: "alpine@md5:" + strings.Repeat("d", 32), wantErr: true},
		"space in tag":    {input: "alpine:not a tag", wantErr: true},
		"uppercase image": {input: "Alpine:3.20", wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := garbagetruck.ParseRef(tc.input)
			if tc.wantErr {
				if !errors.Is(err, garbagetruck.ErrMalformedRef) {
					t.Fatalf("ParseRef(%q) error = %v, want ErrMalformedRef", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRef(%q) unexpected error: %v", tc.input, err)
			}
			checkRef(t, tc.input, got, tc.wantImage, tc.wantTag, tc.wantDigest)
		})
	}
}

func TestParseRefDecomposesTheRepository(t *testing.T) {
	t.Parallel()

	const input = districtsJobs + "/ltv2-to-assessments:" + olderTag
	got, err := garbagetruck.ParseRef(input)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", input, err)
	}
	if registry := got.Repo.RegistryStr(); registry != "us-central1-docker.pkg.dev" {
		t.Errorf("RegistryStr() = %q, want %q", registry, "us-central1-docker.pkg.dev")
	}
	want := "khan-academy/districts-jobs/ltv2-to-assessments"
	if repo := got.Repo.RepositoryStr(); repo != want {
		t.Errorf("RepositoryStr() = %q, want %q", repo, want)
	}
}

func TestRefString(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		umiChanged + ":" + webappTag:                 umiChanged + ":" + webappTag,
		umiChanged + "@" + digestA:                   umiChanged + "@" + digestA,
		umiChanged + ":" + webappTag + "@" + digestA: umiChanged + ":" + webappTag + "@" + digestA,
		"alpine": "alpine:latest",
	}

	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			ref, err := garbagetruck.ParseRef(input)
			if err != nil {
				t.Fatalf("ParseRef(%q): %v", input, err)
			}
			if got := ref.String(); got != want {
				t.Errorf("String() = %q, want %q", got, want)
			}
		})
	}
}

func TestRefsSorted(t *testing.T) {
	t.Parallel()

	refs := parseRefs(t,
		umiChanged+":"+webappTag,
		districtsJobs+"/alerter:v1",
		umiChanged+":"+olderTag,
		umiChanged+":"+webappTag,
	)
	want := []string{
		districtsJobs + "/alerter:v1",
		umiChanged + ":" + olderTag,
		umiChanged + ":" + webappTag,
	}

	if got := refStrings(refs.Sorted()); !slices.Equal(got, want) {
		t.Errorf("Sorted() = %v, want %v", got, want)
	}
	if len(refs) != 4 {
		t.Errorf("Sorted() mutated its receiver: len = %d, want 4", len(refs))
	}
}

func TestRefsByImage(t *testing.T) {
	t.Parallel()

	refs := parseRefs(t,
		umiChanged+":"+webappTag,
		umiChanged+":"+olderTag,
		umiChanged+":"+webappTag,
		districtsJobs+"/roster@"+digestA,
	)

	byImage := refs.ByImage()
	if len(byImage) != 2 {
		t.Fatalf("ByImage() has %d images, want 2", len(byImage))
	}

	wantChanged := []string{umiChanged + ":" + olderTag, umiChanged + ":" + webappTag}
	if got := refStrings(byImage[umiChanged]); !slices.Equal(got, wantChanged) {
		t.Errorf("ByImage()[%s] = %v, want %v", umiChanged, got, wantChanged)
	}

	roster := byImage[districtsJobs+"/roster"]
	if len(roster) != 1 || roster[0].Digest != digestA || roster[0].Tag != "" {
		t.Errorf("ByImage() digest-pinned entry = %+v, want digest %s and no tag", roster, digestA)
	}
}

// checkRef reports every field of a parsed reference that differs from the
// expectation, so one run names all of them rather than only the first.
func checkRef(t *testing.T, input string, got garbagetruck.Ref, image, tag, digest string) {
	t.Helper()

	if name := got.Repo.Name(); name != image {
		t.Errorf("ParseRef(%q).Repo.Name() = %q, want %q", input, name, image)
	}
	if got.Tag != tag {
		t.Errorf("ParseRef(%q).Tag = %q, want %q", input, got.Tag, tag)
	}
	if got.Digest != digest {
		t.Errorf("ParseRef(%q).Digest = %q, want %q", input, got.Digest, digest)
	}
}

// parseRefs builds fixtures from the spellings a manifest would use, so the
// tests read as the manifests do.
func parseRefs(t *testing.T, inputs ...string) garbagetruck.Refs {
	t.Helper()

	refs := make(garbagetruck.Refs, 0, len(inputs))
	for _, input := range inputs {
		ref, err := garbagetruck.ParseRef(input)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", input, err)
		}
		refs = append(refs, ref)
	}
	return refs
}
