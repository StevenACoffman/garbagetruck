// Package protected implements the "protected" CLI command: it reports the
// container images and tags a GitOps repository declares to be in use, which
// are exactly the ones garbagetruck must never delete from the registry.
package protected

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/garbagetruck/cmd/root"
	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
	"github.com/StevenACoffman/garbagetruck/internal/gitops"
)

// Config holds the configuration for the protected command.
type Config struct {
	*root.Config
	Repo    string
	Branch  string
	SSHKey  string
	JSON    bool
	Flags   *ff.FlagSet
	Command *ff.Command
}

// New creates and registers the protected command with the given parent config.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("protected").SetParent(parent.Flags)
	cfg.Flags.StringVar(&cfg.Repo, 'r', "manifest-repo", "",
		"GitOps Kubernetes manifest repository, e.g. github.com/Khan/districts-k8s")
	cfg.Flags.StringVar(&cfg.Branch, 'b', "manifest-branch", "",
		"branch to read manifests from (default: the repository's default branch)")
	cfg.Flags.StringVar(&cfg.SSHKey, 0, "ssh-key", "",
		"private key file to authenticate with (default: the ssh-agent)")
	cfg.Flags.BoolVar(&cfg.JSON, 0, "json", "output the image-to-tags map as JSON")
	cfg.Command = &ff.Command{
		Name:      "protected",
		Usage:     "garbagetruck protected -r <repo> [--manifest-branch <branch>] [--ssh-key <path>] [--json]",
		ShortHelp: "list the images and tags that must not be deleted",
		LongHelp: `Report every container image a GitOps Kubernetes manifest repository
declares to be in use. These are the images and tags garbagetruck must never
delete from Google Artifact Registry.

The repository is cloned over git+ssh into a temporary directory, which is
removed before the command exits. Every *.yaml and *.yml file in it is read,
and every "image:" value is collected, sorted, and de-duplicated into a map of
image name to tags:

  us-central1-docker.pkg.dev/khan-academy/districts-jobs/cedar_umi_changed
      webapp-034d2665381d9ab1eed1784a784c614be173f573
      webapp-057cabe8414d1a7723deef50117707cd8e35b982

An image written without a tag is reported as ":latest", because that is what
Kubernetes pulls for it. An image pinned by digest is reported by its digest.

Authentication is by ssh-agent. Pass --ssh-key to use a private key file
instead, which is what a deployment with a mounted deploy key needs.

Every flag can also be set from a GARBAGETRUCK_-prefixed environment variable:
--manifest-repo reads GARBAGETRUCK_MANIFEST_REPO, and so on.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) exec(ctx context.Context, _ []string) error {
	if cfg.Repo == "" {
		return errors.New("protected: --manifest-repo is required")
	}
	err := cfg.report(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gitops.ErrBadRepo):
		// A malformed --manifest-repo is a usage error, and the help the
		// dispatcher prints for it names the spellings that are accepted.
		return err
	default:
		// Everything else — an unreachable remote, a rejected key, an
		// unreadable manifest — is a runtime failure, not a usage error.
		// Printing the command's help for those buries the one line that
		// says what actually went wrong, so report it and exit non-zero.
		_, _ = fmt.Fprintf(cfg.Stderr, "error: %v\n", err)
		return root.ExitError(1)
	}
}

// report clones the manifest repository, collects the images it declares to be
// in use, and writes them out.
func (cfg *Config) report(ctx context.Context) error {
	worktree, err := gitops.Clone(ctx, gitops.Source{
		Repo:   cfg.Repo,
		Branch: cfg.Branch,
		SSHKey: cfg.SSHKey,
	})
	if err != nil {
		return fmt.Errorf("protected: %w", err)
	}
	defer func() { _ = worktree.Close() }()

	refs, err := garbagetruck.ScanFS(worktree.FS())
	if err != nil {
		return fmt.Errorf("protected: %s: %w", cfg.Repo, err)
	}

	if err := cfg.write(refs.ByImage()); err != nil {
		return fmt.Errorf("protected: %w", err)
	}
	return nil
}

// write renders the map to stdout: as JSON when --json was given, otherwise as
// an image name followed by its indented tags and digests.
func (cfg *Config) write(byImage map[string]garbagetruck.Refs) error {
	if cfg.JSON {
		encoder := json.NewEncoder(cfg.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(byImage); err != nil {
			return fmt.Errorf("write json: %w", err)
		}
		return nil
	}
	for _, image := range slices.Sorted(maps.Keys(byImage)) {
		_, _ = fmt.Fprintln(cfg.Stdout, image)
		for _, ref := range byImage[image] {
			_, _ = fmt.Fprintf(cfg.Stdout, "\t%s\n", version(ref))
		}
	}
	return nil
}

// version renders what distinguishes one reference to an image from another:
// its tag, its digest, or both when the manifest pinned both.
func version(ref garbagetruck.Ref) string {
	switch {
	case ref.Tag == "":
		return ref.Digest
	case ref.Digest == "":
		return ref.Tag
	default:
		return ref.Tag + "@" + ref.Digest
	}
}
