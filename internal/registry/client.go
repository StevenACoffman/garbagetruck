package registry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
)

// cloudPlatformScope is the scope an impersonated token needs to read and tag
// Artifact Registry contents.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

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
func (c *Client) List(ctx context.Context, prefix Prefix) ([]garbagetruck.Image, error) {
	var images []garbagetruck.Image
	request := &artifactregistrypb.ListPackagesRequest{Parent: prefix.Parent()}
	for pkg, err := range c.ar.ListPackages(ctx, request).All() {
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
		versions, listErr := c.listVersions(ctx, pkg.GetName())
		if listErr != nil {
			return nil, listErr
		}
		images = append(images, garbagetruck.Image{Repo: repo, Versions: versions})
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
	for _, step := range steps {
		for _, change := range step.changes {
			if err := step.do(ctx, prefix, change); err != nil {
				errs = append(errs, err)
				continue
			}
			*step.done = append(*step.done, change)
		}
	}
	return applied, errors.Join(errs...)
}

// listVersions reads one package's versions. VersionView_FULL is what makes
// the response carry RelatedTags, which saves a ListTags call per package.
func (c *Client) listVersions(ctx context.Context, pkg string) ([]garbagetruck.Version, error) {
	request := &artifactregistrypb.ListVersionsRequest{
		Parent: pkg,
		View:   artifactregistrypb.VersionView_FULL,
	}
	var versions []garbagetruck.Version
	for version, err := range c.ar.ListVersions(ctx, request).All() {
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
