// Package sweep implements the "sweep" CLI command: it deletes the Google
// Artifact Registry image versions that the expiration policy no longer
// spares.
package sweep

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/garbagetruck/cmd/root"
	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
	"github.com/StevenACoffman/garbagetruck/internal/registry"
)

const (
	// defaultDeleteOlderThan and defaultKeepMostRecent match the policy
	// command's defaults. The two must agree: a sweep that deleted more than
	// the installed policy would is a sweep nobody asked for.
	defaultDeleteOlderThan = 30 * 24 * time.Hour
	defaultKeepMostRecent  = 5

	// listed caps how many expired versions are named individually before the
	// report falls back to a count. A dry run over a large registry can name
	// tens of thousands, which is a wall of text rather than a report.
	listed = 50
)

// Config holds the configuration for the sweep command.
type Config struct {
	*root.Config
	Prefix          string
	DeleteOlderThan time.Duration
	KeepMostRecent  int
	DryRun          bool
	Yes             bool
	Impersonate     string
	Flags           *ff.FlagSet
	Command         *ff.Command
}

// New creates and registers the sweep command with the given parent config.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("sweep").SetParent(parent.Flags)
	cfg.Flags.StringVar(&cfg.Prefix, 'p', "registry-prefix", "",
		"Artifact Registry subtree, e.g. us-central1-docker.pkg.dev/khan-academy/districts-jobs")
	cfg.Flags.DurationVar(&cfg.DeleteOlderThan, 0, "delete-older-than", defaultDeleteOlderThan,
		"delete versions created longer ago than this")
	cfg.Flags.IntVar(&cfg.KeepMostRecent, 0, "keep-most-recent", defaultKeepMostRecent,
		"versions of each image to keep regardless of age")
	cfg.Flags.BoolVar(&cfg.DryRun, 'n', "dry-run",
		"list the versions that would be deleted and delete nothing")
	// No short form on purpose. A one-letter confirmation is easy to add out
	// of habit, and habit is what this flag exists to interrupt.
	cfg.Flags.BoolVar(&cfg.Yes, 0, "yes",
		"actually delete; without it sweep refuses to remove anything")
	cfg.Flags.StringVar(&cfg.Impersonate, 0, "impersonate-service-account", "",
		"service account to impersonate (default: application default credentials)")
	cfg.Command = &ff.Command{
		Name:      "sweep",
		Usage:     "garbagetruck sweep -p <registry-prefix> [--dry-run]",
		ShortHelp: "delete the image versions the expiration policy no longer spares",
		LongHelp: `Delete the Google Artifact Registry image versions that the expiration
policy no longer spares, applying the same rules garbagetruck policy installs:

  keep   any version tagged "protected-"
  keep   the --keep-most-recent newest versions of each image
  keep   any version created more recently than --delete-older-than
  delete everything else

A keep always beats a delete, so a version survives if any rule spares it. A
version whose creation time the registry does not report is never deleted,
because its age cannot be established.

DELETION CANNOT BE UNDONE. A deleted version is gone, along with every tag
pointing at it. Run with --dry-run first and read the list.

Deleting requires --yes. Without it sweep refuses to remove anything, so that
no single mistyped or half-edited command destroys images. Passing both
--dry-run and --yes deletes nothing: dry run wins.

Only "protected-" tags make a version safe here, and only "garbagetruck sync"
writes those. Sweeping a registry that sync has never touched would delete
images the cluster is running, so this command refuses to start when nothing
under the prefix carries a protected tag.

The registry applies the same policy on its own schedule once "garbagetruck
policy" has installed it. This command does the same work immediately and
says exactly what it removed.

Every flag can also be set from a GARBAGETRUCK_-prefixed environment variable:
--delete-older-than reads GARBAGETRUCK_DELETE_OLDER_THAN, and so on.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) exec(ctx context.Context, _ []string) error {
	retention, err := cfg.retention()
	if err != nil {
		return err
	}

	err = cfg.run(ctx, retention)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, registry.ErrBadPrefix):
		return err
	default:
		_, _ = fmt.Fprintf(cfg.Stderr, "error: %v\n", err)
		return root.ExitError(1)
	}
}

// retention validates the flags and returns the policy they describe.
func (cfg *Config) retention() (garbagetruck.Retention, error) {
	switch {
	case cfg.Prefix == "":
		return garbagetruck.Retention{}, errors.New("sweep: --registry-prefix is required")
	case cfg.DeleteOlderThan <= 0:
		return garbagetruck.Retention{}, errors.New("sweep: --delete-older-than must be positive")
	case cfg.KeepMostRecent < 0 || cfg.KeepMostRecent > math.MaxInt32:
		return garbagetruck.Retention{}, fmt.Errorf(
			"sweep: --keep-most-recent must be between 0 and %d", math.MaxInt32)
	case !cfg.DryRun && !cfg.Yes:
		// Checked here, before the listing, so a missing --yes costs a second
		// rather than the minutes it takes to walk the registry first.
		return garbagetruck.Retention{}, errors.New(
			"sweep: --yes is required to delete; run with --dry-run first to " +
				"see what would be removed")
	}
	return garbagetruck.Retention{
		OlderThan:  cfg.DeleteOlderThan,
		KeepNewest: cfg.KeepMostRecent,
	}, nil
}

// run reads the registry, decides what the retention policy removes, then
// either lists it or deletes it.
func (cfg *Config) run(ctx context.Context, retention garbagetruck.Retention) error {
	prefix, err := registry.ParsePrefix(cfg.Prefix)
	if err != nil {
		return fmt.Errorf("sweep: %w", err)
	}

	client, err := registry.NewClient(ctx, cfg.Impersonate)
	if err != nil {
		return fmt.Errorf("sweep: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg.Progressf("listing images under %s", cfg.Prefix)
	images, err := client.List(ctx, prefix, cfg.step)
	if err != nil {
		return fmt.Errorf("sweep: %w", err)
	}

	sweep := garbagetruck.PlanSweep(images, retention, time.Now())
	cfg.report(&sweep)

	if cfg.DryRun {
		// Nothing is deleted because DeleteVersions, the only function here
		// that deletes, is never called. --dry-run wins over --yes when both
		// are given: of the two readings of that command, the one that
		// destroys nothing is the one to act on.
		return nil
	}
	if sweep.IsEmpty() {
		return nil
	}
	if err := guardUnprotected(&sweep); err != nil {
		return fmt.Errorf("sweep: %w", err)
	}

	cfg.Progressf("deleting %d versions", len(sweep.Expired))
	deleted, err := client.DeleteVersions(ctx, prefix, sweep.Expired, cfg.step)
	_, _ = fmt.Fprintf(cfg.Stdout, "deleted %d versions\n", deleted)
	if err != nil {
		return fmt.Errorf("sweep: %w", err)
	}
	return nil
}

// report writes what the retention policy decided.
func (cfg *Config) report(sweep *garbagetruck.Sweep) {
	for i, expired := range sweep.Expired {
		if i == listed {
			_, _ = fmt.Fprintf(cfg.Stdout, "... and %d more\n", len(sweep.Expired)-listed)
			break
		}
		_, _ = fmt.Fprintf(cfg.Stdout, "%s created %s\n",
			expired.String(), expired.Created.Format(time.DateOnly))
	}
	_, _ = fmt.Fprintf(cfg.Stdout,
		"%d to delete; keeping %d protected, %d newest, %d too young, %d undated\n",
		len(sweep.Expired), sweep.Kept.Protected, sweep.Kept.Newest,
		sweep.Kept.TooYoung, sweep.Kept.Undated)
}

// step reports one item of a long operation. It satisfies registry.Progress.
func (cfg *Config) step(done, total int, item string) {
	cfg.Progressf("  [%d/%d] %s", done, total, item)
}

// guardUnprotected refuses to delete when nothing under the prefix is
// protected.
//
// Only "garbagetruck sync" writes protected tags. If none exist, either sync
// has never run or it failed, and every image the cluster is running looks
// expendable. That is the one mistake here that causes an outage, and it is
// cheap to refuse: the listing already says whether any protected tag exists.
func guardUnprotected(sweep *garbagetruck.Sweep) error {
	if sweep.Kept.Protected > 0 {
		return nil
	}
	return errors.New(
		"nothing under this prefix carries a protected tag, so every image looks " +
			"expendable; run garbagetruck sync first, or --dry-run to see the list")
}
