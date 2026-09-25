package garbagetruck_test

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
)

// brokenFS holds one manifest that cannot be opened, so that ScanFS's failure
// path is exercised without depending on real filesystem permissions.
type brokenFS struct{}

func TestScanFSLineShapes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		line string
		want []string
	}{
		"deployment indent": {
			line: "        image: " + umiChanged + ":" + webappTag,
			want: []string{umiChanged + ":" + webappTag},
		},
		"cronjob indent": {
			line: "            image: alpine:3.20",
			want: []string{"alpine:3.20"},
		},
		"tab indent": {
			line: "\timage: alpine:3.20",
			want: []string{"alpine:3.20"},
		},
		"list item": {
			line: "      - image: gcr.io/gke-release/adapter:v0.13.1-gke.0",
			want: []string{"gcr.io/gke-release/adapter:v0.13.1-gke.0"},
		},
		"trailing comment": {
			line: "        image: alpine:3.20 # pinned by hand",
			want: []string{"alpine:3.20"},
		},
		"double quoted": {
			line: `        image: "alpine:3.20"`,
			want: []string{"alpine:3.20"},
		},
		"single quoted": {
			line: "        image: 'alpine:3.20'",
			want: []string{"alpine:3.20"},
		},
		"quoted with trailing comment": {
			line: `        image: "alpine:3.20"  # why`,
			want: []string{"alpine:3.20"},
		},
		"extra spacing after key": {
			line: "        image:     alpine:3.20",
			want: []string{"alpine:3.20"},
		},
		"untagged resolves to latest": {
			line: "        image: alpine",
			want: []string{"alpine:latest"},
		},
		"imagePullPolicy is not an image": {
			line: "        imagePullPolicy: Always",
			want: nil,
		},
		"top level key is not a container image": {
			line: "image: alpine:3.20",
			want: nil,
		},
		"missing space after key": {
			line: "        image:alpine",
			want: nil,
		},
		"scalar starting with a dash": {
			line: "        -image: alpine",
			want: nil,
		},
		"empty value": {
			line: "        image:   ",
			want: nil,
		},
		"comment only value": {
			line: "        image: # filled in by kustomize",
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fsys := fstest.MapFS{"deploy.yaml": &fstest.MapFile{Data: []byte(tc.line + "\n")}}
			refs, err := garbagetruck.ScanFS(fsys)
			if err != nil {
				t.Fatalf("ScanFS: %v", err)
			}
			if got := refStrings(refs); !slices.Equal(got, tc.want) {
				t.Errorf("ScanFS(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

func TestScanFSFileSelection(t *testing.T) {
	t.Parallel()

	const line = "        image: alpine:3.20\n"
	fsys := fstest.MapFS{
		"a/deploy.yaml":         &fstest.MapFile{Data: []byte(line)},
		"b/cronjob.yml":         &fstest.MapFile{Data: []byte(line)},
		"notes.txt":             &fstest.MapFile{Data: []byte(line)},
		"package.json":          &fstest.MapFile{Data: []byte(line)},
		"deploy.yaml.tmpl":      &fstest.MapFile{Data: []byte(line)},
		".git/config.yaml":      &fstest.MapFile{Data: []byte("        image: leaked:1\n")},
		".github/workflows.yml": &fstest.MapFile{Data: []byte("        image: leaked:2\n")},
	}

	refs, err := garbagetruck.ScanFS(fsys)
	if err != nil {
		t.Fatalf("ScanFS: %v", err)
	}
	want := []string{"alpine:3.20"}
	if got := refStrings(refs); !slices.Equal(got, want) {
		t.Errorf("ScanFS() = %v, want %v", got, want)
	}
}

func TestScanFSSortsAndDeduplicatesAcrossFiles(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"z/deploy.yaml": &fstest.MapFile{Data: []byte(
			"        image: " + umiChanged + ":" + webappTag + "\n" +
				"        imagePullPolicy: Always\n" +
				"        image: " + districtsJobs + "/alerter:v1\n",
		)},
		"a/cronjob.yml": &fstest.MapFile{Data: []byte(
			"            image: " + umiChanged + ":" + olderTag + "\n" +
				"            image: " + umiChanged + ":" + webappTag + "\n",
		)},
	}

	refs, err := garbagetruck.ScanFS(fsys)
	if err != nil {
		t.Fatalf("ScanFS: %v", err)
	}

	want := []string{
		districtsJobs + "/alerter:v1",
		umiChanged + ":" + olderTag,
		umiChanged + ":" + webappTag,
	}
	if got := refStrings(refs); !slices.Equal(got, want) {
		t.Errorf("ScanFS() = %v, want %v", got, want)
	}

	wantTags := []string{umiChanged + ":" + olderTag, umiChanged + ":" + webappTag}
	if got := refStrings(refs.ByImage()[umiChanged]); !slices.Equal(got, wantTags) {
		t.Errorf("ByImage()[%s] = %v, want %v", umiChanged, got, wantTags)
	}
}

func TestScanFSReportsUnreadableFile(t *testing.T) {
	t.Parallel()

	_, err := garbagetruck.ScanFS(brokenFS{})
	if err == nil {
		t.Fatal("ScanFS on an unreadable manifest returned no error")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("ScanFS error = %v, want one wrapping fs.ErrPermission", err)
	}
}

func (brokenFS) Open(name string) (fs.File, error) {
	if name == "." {
		return fstest.MapFS{"deploy.yaml": &fstest.MapFile{}}.Open(name)
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}

// refStrings renders refs for comparison, so a failure names the references
// rather than printing a wall of struct fields.
func refStrings(refs garbagetruck.Refs) []string {
	var out []string
	for _, ref := range refs {
		out = append(out, ref.String())
	}
	return out
}
