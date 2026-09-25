package cmd_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/garbagetruck/cmd"
)

func TestRun(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args        []string
		wantErr     error
		wantErrText string
		wantOutput  []string
	}{
		"protected is registered and documents its flags": {
			args:       []string{"protected", "--help"},
			wantErr:    ff.ErrHelp,
			wantOutput: []string{"manifest-repo", "manifest-branch", "ssh-key", "json"},
		},
		"sync is registered and documents its flags": {
			args:    []string{"sync", "--help"},
			wantErr: ff.ErrHelp,
			wantOutput: []string{
				"registry-prefix", "dry-run", "impersonate-service-account", "manifest-repo",
			},
		},
		"sync refuses to run without a registry prefix": {
			args:        []string{"sync", "--manifest-repo", "github.com/Khan/districts-k8s"},
			wantErrText: "sync: --registry-prefix is required",
		},
		"policy is registered and documents its flags": {
			args:    []string{"policy", "--help"},
			wantErr: ff.ErrHelp,
			wantOutput: []string{
				"registry-prefix", "delete-older-than", "keep-most-recent",
				"cleanup-dry-run", "dry-run",
			},
		},
		"policy refuses to run without a registry prefix": {
			args:        []string{"policy"},
			wantErrText: "policy: --registry-prefix is required",
		},
		"policy rejects a non-positive retention window": {
			args: []string{
				"policy", "-p", "us-central1-docker.pkg.dev/p/r", "--delete-older-than", "0s",
			},
			wantErrText: "policy: --delete-older-than must be positive",
		},
		"policy rejects a negative keep count": {
			args: []string{
				"policy", "-p", "us-central1-docker.pkg.dev/p/r", "--keep-most-recent", "-1",
			},
			wantErrText: "policy: --keep-most-recent must be between 0 and",
		},
		"sweep is registered and documents its flags": {
			args:    []string{"sweep", "--help"},
			wantErr: ff.ErrHelp,
			wantOutput: []string{
				"registry-prefix", "delete-older-than", "keep-most-recent", "dry-run",
				"yes", "CANNOT BE UNDONE",
			},
		},
		"sweep refuses to run without a registry prefix": {
			// --yes is present so this pins the prefix check specifically,
			// rather than passing for the wrong reason.
			args:        []string{"sweep", "--yes"},
			wantErrText: "sweep: --registry-prefix is required",
		},
		"sweep refuses to delete without --yes": {
			args:        []string{"sweep", "-p", "us-central1-docker.pkg.dev/p/r"},
			wantErrText: "sweep: --yes is required to delete",
		},
		"sweep rejects a non-positive retention window": {
			args: []string{
				"sweep", "--yes", "-p", "us-central1-docker.pkg.dev/p/r",
				"--delete-older-than", "0s",
			},
			wantErrText: "sweep: --delete-older-than must be positive",
		},
		"no subcommand is not a failure": {
			args:    nil,
			wantErr: ff.ErrNoExec,
		},
		"an unknown subcommand is named back": {
			args:        []string{"frobnicate"},
			wantErrText: `unknown subcommand "frobnicate"`,
		},
		"version writes to the injected stdout": {
			args:       []string{"version"},
			wantOutput: []string{"GoVersion", "Platform"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			err := cmd.Run(t.Context(), tc.args, strings.NewReader(""), &stdout, &stderr)
			checkError(t, err, tc.wantErr, tc.wantErrText)

			output := stdout.String() + stderr.String()
			for _, want := range tc.wantOutput {
				if !strings.Contains(output, want) {
					t.Errorf("Run(%q) output does not mention %q:\n%s", tc.args, want, output)
				}
			}
		})
	}
}

// checkError fails the test unless err matches the expectation: the sentinel
// when one is named, otherwise the substring when one is named, otherwise no
// error at all.
func checkError(t *testing.T, err, want error, wantText string) {
	t.Helper()

	switch {
	case want != nil:
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	case wantText != "":
		if err == nil || !strings.Contains(err.Error(), wantText) {
			t.Fatalf("error = %v, want one containing %q", err, wantText)
		}
	case err != nil:
		t.Fatalf("unexpected error: %v", err)
	}
}
