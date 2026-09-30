package registry

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
)

// maxDeleteBatch bounds one BatchDeleteVersions call.
//
// 50 is the cap the service enforces, not a guess: the field is documented as
// "the maximum number of versions deleted per batch is determined by the
// service and is dependent on the available resources in the region", and a
// larger request is rejected whole with
//
//	InvalidArgument: A maximum of 50 versions are allowed per request
//
// A rejection costs the entire batch, so this must not be raised on the
// assumption that some region allows more.
const maxDeleteBatch = 50

// maxNamedPackages caps how many package ids one collapsed failure line
// names. Sweeping a whole repository can fail across every package it holds,
// and a line that names thirty of them is the wall of text this collapsing
// exists to prevent.
const maxNamedPackages = 5

// ErrVersionNotDeleted is the cause recorded for a version BatchDeleteVersions
// returned in its metadata's FailedVersions.
//
// The operation completed, the registry did not delete the version, and it
// gave no reason, so there is nothing to report but the fact. Sweeping again
// is the remedy: the next run re-lists and asks once more for whatever is
// still there.
var ErrVersionNotDeleted = errors.New("registry reported the version as not deleted")

// DeleteBatch is one BatchDeleteVersions request: the package the RPC must be
// addressed to, and the version resource names within it to remove.
type DeleteBatch struct {
	// Package is the package resource name, which is the Parent the RPC
	// requires. It is not the repository; see Prefix.DeleteBatches.
	Package string
	// ID is the bare package id, for reporting progress and failures in the
	// words a human reading the registry would use.
	ID string
	// Names are the version resource names to delete, every one of them
	// within Package.
	Names []string
}

// DeleteError reports the batches a sweep could not delete, grouped by cause.
//
// A sweep of a large repository issues thousands of batches. When they all
// fail for one reason — a malformed parent, a batch size the service refuses —
// joining the errors prints that reason thousands of times and buries it. This
// prints one line per distinct cause, with the batch and version counts and
// the packages affected, so the reason is legible at the bottom of a long run.
type DeleteError struct {
	groups []*deleteFailure
}

// deleteFailure is every batch that failed for one cause.
type deleteFailure struct {
	cause    error
	batches  int
	versions int
	// packages holds the distinct package ids in first-seen order, which is
	// batch order, so that two runs over one input report identically.
	packages []string
	seen     map[string]bool
}

// deleteFailures accumulates batch failures during a sweep, keyed by the
// cause's message so that identical causes collapse into one group.
type deleteFailures struct {
	byCause map[string]*deleteFailure
	// order preserves first-seen cause order, because map iteration would
	// shuffle the report between runs over identical input.
	order []string
}

// DeleteBatches groups expired versions into the requests a sweep issues: one
// per package, split so that no request carries more than maxDeleteBatch
// names.
//
// Grouping by package is not an optimisation, it is the only form the RPC
// accepts. BatchDeleteVersions describes its Parent as "the name of the
// repository holding all requested versions", but the method is bound to
//
//	/v1/{parent=projects/*/locations/*/repositories/*/packages/*}/versions:batchDelete
//
// and passing a repository name is rejected with `InvalidArgument: Invalid
// package name`. One call cannot span packages however its field is named.
//
// It fails as a whole if any version falls outside prefix: a mismatch means
// the caller and this client disagree about what is being swept, which is not
// a thing to discover halfway through a deletion.
//
// Packages come back in name order, and the versions within each package keep
// the order they were given — oldest first, as PlanSweep sorts them — so that
// two runs over one input issue identical requests and an interrupted run has
// deleted the least controversial versions.
func (p Prefix) DeleteBatches(expired []garbagetruck.Expired) ([]DeleteBatch, error) {
	byPackage := make(map[string][]string)
	for i := range expired {
		pkg, err := p.Package(expired[i].Repo)
		if err != nil {
			return nil, err
		}
		byPackage[pkg] = append(byPackage[pkg], p.versionName(pkg, expired[i].Digest))
	}

	var batches []DeleteBatch
	for _, pkg := range slices.Sorted(maps.Keys(byPackage)) {
		names := byPackage[pkg]
		for start := 0; start < len(names); start += maxDeleteBatch {
			batches = append(batches, DeleteBatch{
				Package: p.packageName(pkg),
				ID:      pkg,
				Names:   names[start:min(start+maxDeleteBatch, len(names))],
			})
		}
	}
	return batches, nil
}

// Batches is how many batches failed.
func (e *DeleteError) Batches() int {
	total := 0
	for _, group := range e.groups {
		total += group.batches
	}
	return total
}

// Versions is how many versions were not deleted.
func (e *DeleteError) Versions() int {
	total := 0
	for _, group := range e.groups {
		total += group.versions
	}
	return total
}

// Error renders one line per distinct cause. A single failed batch reads as
// one plain line, because collapsing a count of one only adds noise.
func (e *DeleteError) Error() string {
	if len(e.groups) == 1 && e.groups[0].batches == 1 {
		group := e.groups[0]
		return fmt.Sprintf("delete %d versions in %s: %v",
			group.versions, group.packages[0], group.cause)
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%d versions in %d batches not deleted", e.Versions(), e.Batches())
	for _, group := range e.groups {
		fmt.Fprintf(&out, "\n  %d batches, %d versions in %s: %v",
			group.batches, group.versions, group.describePackages(), group.cause)
	}
	return out.String()
}

// Unwrap returns one representative cause per group, so that errors.Is finds
// a sentinel a caller cares about without being handed thousands of copies.
func (e *DeleteError) Unwrap() []error {
	causes := make([]error, 0, len(e.groups))
	for _, group := range e.groups {
		causes = append(causes, group.cause)
	}
	return causes
}

// add records versions of one batch that were not deleted, under their cause.
//
// versions is passed rather than taken from the batch because a batch can
// fail in part: BatchDeleteVersions may delete most of what it was given and
// report the rest, and counting the whole batch then would overstate the loss.
func (f *deleteFailures) add(batch DeleteBatch, versions int, cause error) {
	key := cause.Error()
	if f.byCause == nil {
		f.byCause = make(map[string]*deleteFailure)
	}
	group, found := f.byCause[key]
	if !found {
		group = &deleteFailure{cause: cause, seen: make(map[string]bool)}
		f.byCause[key] = group
		f.order = append(f.order, key)
	}
	group.batches++
	group.versions += versions
	if !group.seen[batch.ID] {
		group.seen[batch.ID] = true
		group.packages = append(group.packages, batch.ID)
	}
}

// err returns the collapsed error, or nil when every batch succeeded.
func (f *deleteFailures) err() error {
	if len(f.order) == 0 {
		return nil
	}
	groups := make([]*deleteFailure, 0, len(f.order))
	for _, key := range f.order {
		groups = append(groups, f.byCause[key])
	}
	return &DeleteError{groups: groups}
}

// describePackages names the affected packages, truncating a long list to a
// count plus the first few.
func (f *deleteFailure) describePackages() string {
	if len(f.packages) == 1 {
		return f.packages[0]
	}
	named, extra := f.packages, ""
	if len(named) > maxNamedPackages {
		extra = fmt.Sprintf(" and %d more", len(named)-maxNamedPackages)
		named = named[:maxNamedPackages]
	}
	return fmt.Sprintf("%d packages (%s%s)", len(f.packages), strings.Join(named, ", "), extra)
}
