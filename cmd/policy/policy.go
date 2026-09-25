// Package policy implements the "policy" CLI command: it installs the Google
// Artifact Registry cleanup policies that give garbagetruck's "protected-"
// tags their meaning.
package policy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/garbagetruck/cmd/root"
	"github.com/StevenACoffman/garbagetruck/internal/registry"
)

const (
	// defaultDeleteOlderThan matches the thirty-day window the reference
	// policy document describes.
	defaultDeleteOlderThan = 30 * 24 * time.Hour
	// defaultKeepMostRecent spares the newest few versions of every image
	// however old they are, so an image that stopped being rebuilt does not
	// disappear entirely.
	defaultKeepMostRecent = 5
)

// Config holds the configuration for the policy command.
type Config struct {
	*root.Config
	Prefix          string
	DeleteOlderThan time.Duration
	KeepMostRecent  int
	CleanupDryRun   bool
	DryRun          bool
	Impersonate     string
	Flags           *ff.FlagSet
	Command         *ff.Command
}

// New creates and registers the policy command with the given parent config.
func New(parent *root.Config) *Config {
	var cfg Config
	cfg.Config = parent
	cfg.Flags = ff.NewFlagSet("policy").SetParent(parent.Flags)
	cfg.Flags.StringVar(&cfg.Prefix, 'p', "registry-prefix", "",
		"Artifact Registry subtree, e.g. us-central1-docker.pkg.dev/khan-academy/districts-jobs")
	cfg.Flags.DurationVar(&cfg.DeleteOlderThan, 0, "delete-older-than", defaultDeleteOlderThan,
		"delete versions older than this")
	cfg.Flags.IntVar(&cfg.KeepMostRecent, 0, "keep-most-recent", defaultKeepMostRecent,
		"versions of each image to keep regardless of age")
	cfg.Flags.BoolVar(
		&cfg.CleanupDryRun,
		0,
		"cleanup-dry-run",
		"install the policies with the registry's cleanup pipeline held back, so it deletes nothing",
	)
	cfg.Flags.BoolVar(&cfg.DryRun, 'n', "dry-run",
		"describe the changes without making them; the registry is only read")
	cfg.Flags.StringVar(&cfg.Impersonate, 0, "impersonate-service-account", "",
		"service account to impersonate (default: application default credentials)")
	cfg.Command = &ff.Command{
		Name:      "policy",
		Usage:     "garbagetruck policy -p <registry-prefix> [--dry-run] [--cleanup-dry-run]",
		ShortHelp: "install the cleanup policies that spare protected- tags",
		LongHelp: `Install the Google Artifact Registry cleanup policies that make
garbagetruck's "protected-" tags mean something. Three rules are maintained:

  garbagetruck-delete-old        delete versions older than --delete-older-than
  garbagetruck-keep-recent       keep the --keep-most-recent newest versions
  garbagetruck-keep-protected    keep anything tagged "protected-"

Artifact Registry evaluates rules against one another with OR, and a KEEP
always beats a DELETE, so a version survives if any keep rule matches it. The
two keep rules stay separate for that reason: combined into one, a version
would have to be both recent and protected to survive.

Policies attach to a repository. When --registry-prefix names a repository the
rules cover all of it. When it names a subpath, the rules are confined with
PackageNamePrefixes, which matches on a plain string prefix.

Policies this tool did not write are left in place, and reported. A cleanup
policy of your own that also deletes will delete on top of these rules.

ORDER MATTERS. These rules delete images that carry no "protected-" tag, and
only "garbagetruck sync" adds those tags. Installing a live policy against a
registry that sync has never touched makes every version older than the window
a candidate for deletion. Run "garbagetruck sync" first, or install with
--cleanup-dry-run and read what the registry reports it would have deleted.

--dry-run and --cleanup-dry-run hold back different things. --dry-run stops
garbagetruck from writing anything at all. --cleanup-dry-run writes the
policies and turns off the registry's own deletion pipeline, which is a change
to the repository. They can be combined.

Every flag can also be set from a GARBAGETRUCK_-prefixed environment variable:
--keep-most-recent reads GARBAGETRUCK_KEEP_MOST_RECENT, and so on.`,
		Flags: cfg.Flags,
		Exec:  cfg.exec,
	}
	parent.Command.Subcommands = append(parent.Command.Subcommands, cfg.Command)
	return &cfg
}

func (cfg *Config) exec(ctx context.Context, _ []string) error {
	spec, err := cfg.spec()
	if err != nil {
		// A bad flag value is a usage error: the dispatcher prints the help,
		// which documents the accepted values.
		return err
	}

	err = cfg.run(ctx, spec)
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

// spec validates the flags and returns the retention they describe. Checking
// and converting in one place is what keeps the int32 narrowing honest: the
// bound it relies on is the line above it, not a check in another function
// that a later edit could drift away from.
func (cfg *Config) spec() (registry.PolicySpec, error) {
	switch {
	case cfg.Prefix == "":
		return registry.PolicySpec{}, errors.New("policy: --registry-prefix is required")
	case cfg.DeleteOlderThan <= 0:
		return registry.PolicySpec{}, errors.New("policy: --delete-older-than must be positive")
	case cfg.KeepMostRecent < 0 || cfg.KeepMostRecent > math.MaxInt32:
		return registry.PolicySpec{}, fmt.Errorf(
			"policy: --keep-most-recent must be between 0 and %d", math.MaxInt32)
	}
	return registry.PolicySpec{
		DeleteOlderThan: cfg.DeleteOlderThan,
		KeepMostRecent:  int32(cfg.KeepMostRecent),
		CleanupDryRun:   cfg.CleanupDryRun,
	}, nil
}

// run reads the repository's current policies, works out the difference, then
// either describes it or writes it.
func (cfg *Config) run(ctx context.Context, spec registry.PolicySpec) error {
	prefix, err := registry.ParsePrefix(cfg.Prefix)
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}

	client, err := registry.NewClient(ctx, cfg.Impersonate)
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg.Progressf("reading cleanup policies on %s", prefix.Parent())
	current, err := client.ReadPolicies(ctx, prefix)
	if err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	cfg.Progressf("repository has %d cleanup policies", len(current.ByID))

	plan := registry.PlanPolicies(current, prefix.Policies(spec), spec.CleanupDryRun)

	if cfg.DryRun {
		// Nothing is written because WritePolicies, the only function here
		// that writes, is never called.
		cfg.report(&plan, wouldTense())
		return nil
	}
	if plan.IsEmpty() {
		// Writing an identical repository would be a no-op with an audit-log
		// entry and a wasted call, and it would make "nothing written" false.
		cfg.report(&plan, didTense())
		return nil
	}
	cfg.Progressf("writing %d cleanup policies", len(plan.Want.ByID))
	if err := client.WritePolicies(ctx, prefix, plan.Want); err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	cfg.report(&plan, didTense())
	return nil
}

// report writes the plan in the given tense: what would change, or what did.
func (cfg *Config) report(plan *registry.PolicyPlan, wording *tense) {
	for _, id := range plan.Create {
		_, _ = fmt.Fprintf(cfg.Stdout, "%s %s\n", wording.install, id)
	}
	for _, id := range plan.Update {
		_, _ = fmt.Fprintf(cfg.Stdout, "%s %s\n", wording.update, id)
	}
	if plan.DryRunFrom != plan.DryRunTo {
		_, _ = fmt.Fprintf(cfg.Stdout, "%s %s\n", wording.pipeline, onOff(plan.DryRunTo))
	}
	for _, id := range plan.Unchanged {
		_, _ = fmt.Fprintf(cfg.Stdout, "%s %s as it is\n", wording.unchanged, id)
	}
	if len(plan.Preserved) > 0 {
		_, _ = fmt.Fprintf(cfg.Stdout, "%s %d policy garbagetruck does not manage: %s\n",
			wording.preserved, len(plan.Preserved), strings.Join(plan.Preserved, ", "))
	}
	for _, id := range plan.ForeignDeletes {
		_, _ = fmt.Fprintf(cfg.Stderr,
			"warning: %s also deletes, on top of %s\n", id, registry.DeleteOldID)
	}
	if plan.IsEmpty() {
		_, _ = fmt.Fprintln(cfg.Stdout, wording.nothing)
	}
}

// onOff names the state of the registry's cleanup pipeline, which the dry-run
// setting inverts: dry run on means deletion off.
func onOff(cleanupDryRun bool) string {
	if cleanupDryRun {
		return "off"
	}
	return "on"
}
