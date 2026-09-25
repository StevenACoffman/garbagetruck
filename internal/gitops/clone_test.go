package gitops_test

import (
	"errors"
	"testing"

	"github.com/StevenACoffman/garbagetruck/internal/gitops"
)

const canonical = "ssh://git@github.com/Khan/districts-k8s.git"

func TestSSHURL(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		input   string
		want    string
		wantErr error
	}{
		"host and path":         {input: "github.com/Khan/districts-k8s", want: canonical},
		"host and path dot git": {input: "github.com/Khan/districts-k8s.git", want: canonical},
		"surrounding space":     {input: "  github.com/Khan/districts-k8s  ", want: canonical},
		"trailing slash":        {input: "github.com/Khan/districts-k8s/", want: canonical},
		"https":                 {input: "https://github.com/Khan/districts-k8s", want: canonical},
		"https dot git": {
			input: "https://github.com/Khan/districts-k8s.git",
			want:  canonical,
		},
		"http":                {input: "http://github.com/Khan/districts-k8s", want: canonical},
		"scp":                 {input: "git@github.com:Khan/districts-k8s.git", want: canonical},
		"scp without dot git": {input: "git@github.com:Khan/districts-k8s", want: canonical},
		"nested path": {
			input: "gitlab.example.com/a/b/c",
			want:  "ssh://git@gitlab.example.com/a/b/c.git",
		},
		"ssh url passes through": {
			input: "ssh://git@github.com:2222/Khan/x.git",
			want:  "ssh://git@github.com:2222/Khan/x.git",
		},
		"other ssh account": {
			input: "deploy@git.example.com:team/repo.git",
			want:  "ssh://deploy@git.example.com/team/repo.git",
		},

		"empty":         {input: "", wantErr: gitops.ErrNoRepo},
		"blank":         {input: "   ", wantErr: gitops.ErrNoRepo},
		"host only":     {input: "github.com", wantErr: gitops.ErrBadRepo},
		"no path":       {input: "github.com/", wantErr: gitops.ErrBadRepo},
		"no host":       {input: "/Khan/districts-k8s", wantErr: gitops.ErrBadRepo},
		"https no path": {input: "https://github.com", wantErr: gitops.ErrBadRepo},
		"scp no host":   {input: "@github.com:Khan/x", wantErr: gitops.ErrBadRepo},
		"scp no colon":  {input: "git@github.com", wantErr: gitops.ErrBadRepo},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := gitops.SSHURL(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("SSHURL(%q) error = %v, want %v", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SSHURL(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("SSHURL(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestCloneRejectsAnEmptyRepo pins the one Clone failure that happens before
// any network or filesystem work, so the check cannot be dropped later.
// Cloning itself needs a remote and a key, and is exercised by running the
// command rather than by a unit test.
func TestCloneRejectsAnEmptyRepo(t *testing.T) {
	t.Parallel()

	_, err := gitops.Clone(t.Context(), gitops.Source{Repo: ""})
	if !errors.Is(err, gitops.ErrNoRepo) {
		t.Fatalf("Clone with no repo: error = %v, want ErrNoRepo", err)
	}
}
