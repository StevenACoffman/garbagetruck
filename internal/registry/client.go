package registry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"github.com/google/go-containerregistry/pkg/name"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
)

// cloudPlatformScope is the scope an impersonated token needs to read and tag
// Artifact Registry contents.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// retryInitial and retryMax bound the backoff between read attempts.
const (
	retryInitial = 250 * time.Millisecond
	retryMax     = 10 * time.Second
)

// Progress is called as a long operation advances: how many of how many
// items are done, and the name of the one just handled. A nil Progress
// reports nothing, which is what lets a caller opt out without every call
// site having to supply one.
type Progress func(done, total int, item string)

// packageRef is one Artifact Registry package under a prefix, resolved once
// so that listing can report how many there are before it starts.
type packageRef struct {
	id       string
	resource string
	repo     name.Repository
}

// Client reads and edits the Docker images an Artifact Registry repository
// holds. Close it when done.
type Client struct {
	ar *artifactregistry.Client
}

// NewClient connects with Application Default Credentials. When impersonated
// names a service account, calls are made as that account instead — the same
// arrangement garbage-gar needed to reach this registry.
func NewClient(ctx context.Context, impersonated string) (*Client, error) {
	var opts []option.ClientOption
	if impersonated != "" {
		source, err := impersonate.CredentialsTokenSource(ctx, impersonate.CredentialsConfig{
			TargetPrincipal: impersonated,
			Scopes:          []string{cloudPlatformScope},
		})
		if err != nil {
			return nil, fmt.Errorf("impersonate %s: %w", impersonated, err)
		}
		opts = append(opts, option.WithTokenSource(source))
	}
	ar, err := artifactregistry.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("artifact registry client: %w", err)
	}
	return &Client{ar: ar}, nil
}

// Close releases the underlying connection.
func (c *Client) Close() error {
	if err := c.ar.Close(); err != nil {
		return fmt.Errorf("close artifact registry client: %w", err)
	}
	return nil
}

// List returns every image under prefix, each with the versions the registry
// stores for it: the digest that identifies the content, and the tags pointing
// at it. Results are sorted by image name so runs are comparable.
func (c *Client) List(
	ctx context.Context,
	prefix Prefix,
	onProgress Progress,
) ([]garbagetruck.Image, error) {
	// Enumerate the packages before listing any versions. It costs one
	// paginated call and turns an open-ended wait into a countable one: this
	// is the slow half of a run, one version listing per package.
	packages, err := c.packages(ctx, prefix)
	if err != nil {
		return nil, err
	}

	images := make([]garbagetruck.Image, 0, len(packages))
	for i, pkg := range packages {
		onProgress.report(i+1, len(packages), pkg.id)
		versions, listErr := c.listVersions(ctx, pkg.resource)
		if listErr != nil {
			return nil, listErr
		}
		images = append(images, garbagetruck.Image{Repo: pkg.repo, Versions: versions})
	}
	slices.SortFunc(images, func(a, b garbagetruck.Image) int {
		return strings.Compare(a.Repo.Name(), b.Repo.Name())
	})
	return images, nil
}

// Apply performs the plan's changes and returns the subset that succeeded, so
// that a caller renders a real run with the same code that renders a dry run.
//
// One failing change does not stop the rest: reconciling most of a registry
// beats reconciling none of it, and the returned plan says exactly what
// landed. Every failure is joined into the returned error.
//
// This is the only function in garbagetruck that modifies a registry. Dry run
// is implemented by not calling it, rather than by a flag checked inside it.
func (c *Client) Apply(
	ctx context.Context,
	prefix Prefix,
	plan *garbagetruck.Plan,
	onProgress Progress,
) (garbagetruck.Plan, error) {
	applied := garbagetruck.Plan{Problems: plan.Problems}
	// Ordered deliberately: a Move repoints an existing tag atomically, so
	// running the Removes last never leaves a version briefly unprotected.
	steps := []struct {
		changes []garbagetruck.Change
		do      func(context.Context, Prefix, garbagetruck.Change) error
		done    *[]garbagetruck.Change
	}{
		{plan.Move, c.moveTag, &applied.Move},
		{plan.Create, c.createTag, &applied.Create},
		{plan.Remove, c.deleteTag, &applied.Remove},
	}

	var errs []error
	total, seen := len(plan.Changes()), 0
	for _, step := range steps {
		for _, change := range step.changes {
			seen++
			onProgress.report(seen, total, change.Tag+" on "+change.Repo.Name())
			if err := step.do(ctx, prefix, change); err != nil {
				errs = append(errs, err)
				continue
			}
			*step.done = append(*step.done, change)
		}
	}
	return applied, errors.Join(errs...)
}

// ReadPolicies returns the repository's cleanup configuration: every policy on
// it, garbagetruck's own and anyone else's, plus whether the cleanup pipeline
// is currently held back.
func (c *Client) ReadPolicies(ctx context.Context, prefix Prefix) (Policies, error) {
	repo, err := c.ar.GetRepository(ctx, &artifactregistrypb.GetRepositoryRequest{
		Name: prefix.Parent(),
	}, retryReads())
	if err != nil {
		return Policies{}, fmt.Errorf("get repository %s: %w", prefix.Parent(), err)
	}
	return Policies{ByID: repo.GetCleanupPolicies(), DryRun: repo.GetCleanupPolicyDryRun()}, nil
}

// WritePolicies replaces the repository's cleanup configuration with want.
//
// The field mask names only the two fields being set, so nothing else on the
// repository — encryption keys, access settings, labels — can be disturbed by
// this call. want must already carry the policies garbagetruck does not
// manage, because the map field is replaced wholesale; see PlanPolicies.
//
// With Apply, this is one of only two functions in garbagetruck that modify a
// registry. A dry run is implemented by not reaching either of them.
func (c *Client) WritePolicies(ctx context.Context, prefix Prefix, want Policies) error {
	_, err := c.ar.UpdateRepository(ctx, &artifactregistrypb.UpdateRepositoryRequest{
		Repository: &artifactregistrypb.Repository{
			Name:                prefix.Parent(),
			CleanupPolicies:     want.ByID,
			CleanupPolicyDryRun: want.DryRun,
		},
		UpdateMask: &fieldmaskpb.FieldMask{
			Paths: []string{"cleanup_policies", "cleanup_policy_dry_run"},
		},
	})
	if err != nil {
		return fmt.Errorf("update cleanup policies on %s: %w", prefix.Parent(), err)
	}
	return nil
}

// DeleteVersions permanently removes the given versions.
//
// This is the only irreversible operation in garbagetruck: a deleted version
// cannot be restored, and any tag pointing at it goes with it. Callers are
// expected to have computed expired with garbagetruck.PlanSweep, which spares
// anything carrying a protected tag.
//
// Deletions go in batches under one repository parent. A batch that fails is
// recorded and the rest continue, because stopping halfway through would leave
// the registry in a state no one chose; the returned error joins every
// failure. The count returned is of versions actually deleted.
func (c *Client) DeleteVersions(
	ctx context.Context,
	prefix Prefix,
	expired []garbagetruck.Expired,
	onProgress Progress,
) (int, error) {
	batches, err := prefix.DeleteBatches(expired)
	if err != nil {
		return 0, err
	}

	var (
		deleted  int
		seen     int
		failures deleteFailures
	)
	for _, batch := range batches {
		seen += len(batch.Names)
		onProgress.report(seen, len(expired), batch.ID)
		done, err := c.deleteBatch(ctx, batch)
		deleted += done
		if err != nil {
			failures.add(batch, len(batch.Names)-done, err)
		}
	}
	return deleted, failures.err()
}

// deleteBatch removes one batch and waits for the registry to finish, so that
// a failure is reported against the batch that caused it rather than surfacing
// long afterwards.
//
// It returns how many versions the registry actually removed, which is not
// always how many it was asked to remove: BatchDeleteVersions is a
// partial-success operation. It can finish with Wait reporting no error at
// all, having skipped some of the versions it was given and named them in the
// operation's metadata instead. Counting a completed batch as wholly applied
// is what made a sweep report more deletions than it performed.
//
// The wrapping text is fixed, carrying neither the batch's size nor its
// package. DeleteVersions groups batches by their cause to collapse a
// systemic failure into one line, and per-batch text would make every cause
// unique and defeat that; the counts and package names belong to the group,
// not to each error. Rejection, mid-flight failure, and a silent skip are
// worth telling apart, so each carries its own cause and groups separately.
func (c *Client) deleteBatch(ctx context.Context, batch DeleteBatch) (int, error) {
	op, err := c.ar.BatchDeleteVersions(ctx, &artifactregistrypb.BatchDeleteVersionsRequest{
		Parent: batch.Package,
		Names:  batch.Names,
	})
	if err != nil {
		return 0, fmt.Errorf("batch rejected: %w", err)
	}
	if err := op.Wait(ctx); err != nil {
		return 0, fmt.Errorf("batch failed: %w", err)
	}

	// Metadata does not call the server; it reads what the final poll inside
	// Wait already returned. A nil result means the server sent none, which is
	// not evidence of a skip, so the batch counts as applied.
	meta, err := op.Metadata()
	if err != nil {
		return 0, fmt.Errorf("batch delete metadata: %w", err)
	}
	skipped := len(meta.GetFailedVersions())
	if skipped == 0 {
		return len(batch.Names), nil
	}
	return len(batch.Names) - skipped, ErrVersionNotDeleted
}

// packages resolves every package under prefix, so a caller knows how much
// work there is before the per-package listing begins.
func (c *Client) packages(ctx context.Context, prefix Prefix) ([]packageRef, error) {
	var found []packageRef
	request := &artifactregistrypb.ListPackagesRequest{Parent: prefix.Parent()}
	for pkg, err := range c.ar.ListPackages(ctx, request, retryReads()).All() {
		if err != nil {
			return nil, fmt.Errorf("list packages in %s: %w", prefix.Parent(), err)
		}
		id, named := resourceID(pkg.GetName(), "/packages/")
		if !named || !prefix.Matches(id) {
			continue
		}
		repo, repoErr := prefix.Repo(id)
		if repoErr != nil {
			return nil, repoErr
		}
		found = append(found, packageRef{id: id, resource: pkg.GetName(), repo: repo})
	}
	return found, nil
}

// report calls p unless it is nil, so callers need no nil check of their own.
func (p Progress) report(done, total int, item string) {
	if p != nil {
		p(done, total, item)
	}
}

// listVersions reads one package's versions. VersionView_FULL is what makes
// the response carry RelatedTags, which saves a ListTags call per package.
func (c *Client) listVersions(ctx context.Context, pkg string) ([]garbagetruck.Version, error) {
	request := &artifactregistrypb.ListVersionsRequest{
		Parent: pkg,
		View:   artifactregistrypb.VersionView_FULL,
	}
	var versions []garbagetruck.Version
	for version, err := range c.ar.ListVersions(ctx, request, retryReads()).All() {
		if err != nil {
			return nil, fmt.Errorf("list versions in %s: %w", pkg, err)
		}
		digest, named := resourceID(version.GetName(), "/versions/")
		if !named {
			continue
		}
		versions = append(versions, garbagetruck.Version{
			Digest: digest,
			Tags:   tagIDs(version.GetRelatedTags()),
			// GetCreateTime returns nil when the registry reports none, and
			// AsTime maps that to the zero time, which retention treats as
			// "age unknown, never delete".
			Created: version.GetCreateTime().AsTime(),
		})
	}
	return versions, nil
}

func (c *Client) createTag(ctx context.Context, prefix Prefix, change garbagetruck.Change) error {
	pkg, err := prefix.Package(change.Repo)
	if err != nil {
		return err
	}
	_, err = c.ar.CreateTag(ctx, &artifactregistrypb.CreateTagRequest{
		Parent: prefix.packageName(pkg),
		TagId:  change.Tag,
		Tag:    &artifactregistrypb.Tag{Version: prefix.versionName(pkg, change.Digest)},
	})
	if err != nil {
		return fmt.Errorf("create tag %s on %s: %w", change.Tag, change.Repo.Name(), err)
	}
	return nil
}

func (c *Client) moveTag(ctx context.Context, prefix Prefix, change garbagetruck.Change) error {
	pkg, err := prefix.Package(change.Repo)
	if err != nil {
		return err
	}
	_, err = c.ar.UpdateTag(ctx, &artifactregistrypb.UpdateTagRequest{
		Tag: &artifactregistrypb.Tag{
			Name:    prefix.tagName(pkg, change.Tag),
			Version: prefix.versionName(pkg, change.Digest),
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"version"}},
	})
	if err != nil {
		return fmt.Errorf("move tag %s on %s: %w", change.Tag, change.Repo.Name(), err)
	}
	return nil
}

func (c *Client) deleteTag(ctx context.Context, prefix Prefix, change garbagetruck.Change) error {
	pkg, err := prefix.Package(change.Repo)
	if err != nil {
		return err
	}
	err = c.ar.DeleteTag(ctx, &artifactregistrypb.DeleteTagRequest{
		Name: prefix.tagName(pkg, change.Tag),
	})
	if err != nil {
		return fmt.Errorf("delete tag %s on %s: %w", change.Tag, change.Repo.Name(), err)
	}
	return nil
}

// tagIDs reduces tag resources to their bare, sorted ids.
func tagIDs(tags []*artifactregistrypb.Tag) []string {
	ids := make([]string, 0, len(tags))
	for _, tag := range tags {
		if id, named := resourceID(tag.GetName(), "/tags/"); named {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// resourceID returns the trailing identifier of an Artifact Registry resource
// name, undoing the escaping the API applies to ids that contain slashes.
func resourceID(resource, section string) (string, bool) {
	_, id, found := strings.Cut(resource, section)
	if !found || id == "" {
		return "", false
	}
	unescaped, err := url.PathUnescape(id)
	if err != nil {
		return id, true
	}
	return unescaped, true
}

// retryReads retries the transient failures a fan-out read runs into on a
// large repository. The generated client sets a per-attempt timeout but
// configures no retry at all, so a single Unavailable in a listing that spans
// hundreds of RPCs aborts the whole run — which is how this was found.
//
// Only reads get this. The tag and policy writes are not idempotent at the RPC
// level: replaying a CreateTag that did reach the server fails with
// AlreadyExists, and a replayed DeleteTag with NotFound. Re-running the
// command is the safe retry for those, because a whole run is idempotent even
// though its individual calls are not.
func retryReads() gax.CallOption {
	return gax.WithRetry(func() gax.Retryer {
		return gax.OnCodes(
			[]codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted},
			gax.Backoff{Initial: retryInitial, Max: retryMax, Multiplier: 2},
		)
	})
}
