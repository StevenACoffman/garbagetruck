// Package sync implements the "sync" CLI command: it reconciles the
// "protected-" tags in a Google Artifact Registry repository with the images a
// GitOps repository declares to be in use.
package sync

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/garbagetruck/cmd/root"
	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
	"github.com/StevenACoffman/garbagetruck/internal/gitops"
	"github.com/StevenACoffman/garbagetruck/internal/registry"
)

// Config holds the configuration for the sync command.
type Config struct {
	*root.Config
	Repo        string
	Branch      string
	SSHKey      string
	Prefix      string
	Impersonate string
	DryRun      bool
	Flags       *ff.FlagSet
	Command     *ff.Command
}

// New creates and registers the sync command with the given parent config.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("sync").SetParent(parent.Flags)
	cfg.Flags.StringVar(&cfg.Repo, 'r', "manifest-repo", "",
		"GitOps Kubernetes manifest repository, e.g. github.com/Khan/districts-k8s")
	cfg.Flags.StringVar(&cfg.Branch, 'b', "manifest-branch", "",
		"branch to read manifests from (default: the repository's default branch)")
	cfg.Flags.StringVar(&cfg.SSHKey, 0, "ssh-key", "",
		"private key file to authenticate with (default: the ssh-agent)")
	cfg.Flags.StringVar(&cfg.Prefix, 'p', "registry-prefix", "",
		"Artifact Registry subtree, e.g. us-central1-docker.pkg.dev/khan-academy/districts-jobs")
	cfg.Flags.StringVar(&cfg.Impersonate, 0, "impersonate-service-account", "",
		"service account to impersonate (default: application default credentials)")
	cfg.Flags.BoolVar(&cfg.DryRun, 'n', "dry-run",
		"describe the changes without making them; the registry is only read")
	cfg.Command = &ff.Command{
		Name:      "sync",
		Usage:     "garbagetruck sync -r <repo> -p <registry-prefix> [--dry-run]",
		ShortHelp: "reconcile the registry's protected- tags with the manifests",
		LongHelp: `Make a Google Artifact Registry repository's "protected-" tags agree with
the images a GitOps Kubernetes manifest repository declares to be in use.

For every image the manifests pin, garbagetruck ensures the registry version
carries a tag of "protected-" plus the manifest's own tag, or the tag
"protected-digest-only" when the manifest pinned a digest and wrote no tag. Any
"protected-" tag the manifests no longer justify is removed. No other tag is
touched, and no image is ever deleted.

These tags exist to be spared. A repository cleanup policy that keeps versions
whose tags start with "protected-" is what turns them into protection;
garbagetruck does not install that policy.

With --dry-run the registry is only read, and the output describes what would
change. Without it, the same output describes what did change.

Every flag can also be set from a GARBAGETRUCK_-prefixed environment variable:
--registry-prefix reads GARBAGETRUCK_REGISTRY_PREFIX, and so on.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) exec(ctx context.Context, _ []string) error {
	switch {
	case cfg.Repo == "":
		return errors.New("sync: --manifest-repo is required")
	case cfg.Prefix == "":
		return errors.New("sync: --registry-prefix is required")
	}

	err := cfg.run(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gitops.ErrBadRepo), errors.Is(err, registry.ErrBadPrefix):
		// A malformed flag value is a usage error, and the help the dispatcher
		// prints for it names the spellings that are accepted.
		return err
	default:
		// Everything else is a runtime failure. Printing thirty lines of help
		// for an unreachable remote or a rejected credential buries the one
		// line that says what went wrong.
		_, _ = fmt.Fprintf(cfg.Stderr, "error: %v\n", err)
		return root.ExitError(1)
	}
}

// run is a flat sequence: read the manifests, read the registry, work out the
// difference, then either describe it or make it.
func (cfg *Config) run(ctx context.Context) error {
	prefix, err := registry.ParsePrefix(cfg.Prefix)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	manifest, err := cfg.readManifests(ctx)
	if err != nil {
		return err
	}

	client, err := registry.NewClient(ctx, cfg.Impersonate)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	defer func() { _ = client.Close() }()

	images, err := client.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	plan := garbagetruck.PlanProtection(manifest, images)
	if cfg.DryRun {
		// The registry is never written because Apply — the only function
		// that writes — is never called. Nothing below this line knows or
		// needs to know that this was a dry run.
		cfg.report(&plan, wouldTense())
		return nil
	}

	applied, err := client.Apply(ctx, prefix, &plan)
	cfg.report(&applied, didTense())
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

// readManifests clones the GitOps repository and collects the image references
// it declares to be in use.
func (cfg *Config) readManifests(ctx context.Context) (garbagetruck.Refs, error) {
	worktree, err := gitops.Clone(ctx, gitops.Source{
		Repo:   cfg.Repo,
		Branch: cfg.Branch,
		SSHKey: cfg.SSHKey,
	})
	if err != nil {
		return nil, fmt.Errorf("sync: %w", err)
	}
	defer func() { _ = worktree.Close() }()

	refs, err := garbagetruck.ScanFS(worktree.FS())
	if err != nil {
		return nil, fmt.Errorf("sync: %s: %w", cfg.Repo, err)
	}
	return refs, nil
}

// report writes the plan in the given tense. A dry run and a real run print
// the same shape, because they are the same data: what would change, and what
// did.
func (cfg *Config) report(plan *garbagetruck.Plan, tense tense) {
	writeChanges(cfg.Stdout, tense.create, plan.Create)
	writeChanges(cfg.Stdout, tense.move, plan.Move)
	writeChanges(cfg.Stdout, tense.remove, plan.Remove)

	for _, problem := range plan.Problems {
		_, _ = fmt.Fprintf(cfg.Stdout, "unprotected %s: %s\n", problem.Ref, problem.Reason)
	}
	// Say "nothing to do" only when that is the whole story. Printing it under
	// a list of unprotected images would contradict the lines above it.
	if plan.IsEmpty() && len(plan.Problems) == 0 {
		_, _ = fmt.Fprintln(cfg.Stdout, tense.nothing)
	}
}

// writeChanges prints one group of changes under its heading, or nothing when
// the group is empty.
func writeChanges(out io.Writer, heading string, changes []garbagetruck.Change) {
	for _, change := range changes {
		_, _ = fmt.Fprintf(out, "%s %s on %s (%s)\n",
			heading, change.Tag, change.Repo.Name(), change.Digest)
	}
}
