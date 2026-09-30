package registry_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
	"github.com/StevenACoffman/garbagetruck/internal/registry"
)

// jobsParent is the repository resource name districtsJobs parses to. A
// BatchDeleteVersions addressed here is the bug these tests exist to catch:
// the RPC rejects a repository parent with "Invalid package name".
const jobsParent = "projects/khan-academy/locations/us-central1/repositories/districts-jobs"

// serviceMaxPerRequest is the cap BatchDeleteVersions enforces. A request
// carrying more is rejected whole with "A maximum of 50 versions are allowed
// per request", so every batch must come in at or under it. Hardcoded here
// rather than read from the package under test: the point is to fail if that
// package's own constant drifts above what the service accepts.
const serviceMaxPerRequest = 50

// expiredIn builds an expired version of one package under districtsJobs.
func expiredIn(t *testing.T, pkg, digest string) garbagetruck.Expired {
	t.Helper()
	repo, err := name.NewRepository(districtsJobs+"/"+pkg, name.WithDefaultRegistry(""))
	if err != nil {
		t.Fatalf("NewRepository(%s/%s): %v", districtsJobs, pkg, err)
	}
	return garbagetruck.Expired{Repo: repo, Digest: digest}
}

func TestDeleteBatchesAddressesPackagesNotTheRepository(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	batches, err := prefix.DeleteBatches([]garbagetruck.Expired{
		expiredIn(t, "roster", "sha256:aaa"),
	})
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}

	want := jobsParent + "/packages/roster"
	if batches[0].Package != want {
		t.Errorf("Package = %q, want %q", batches[0].Package, want)
	}
	if batches[0].Package == jobsParent {
		t.Error("Package is the repository; the RPC rejects that as an invalid package name")
	}
	if batches[0].ID != "roster" {
		t.Errorf("ID = %q, want %q", batches[0].ID, "roster")
	}
	if got := batches[0].Names; len(got) != 1 || got[0] != want+"/versions/sha256:aaa" {
		t.Errorf("Names = %q, want [%q]", got, want+"/versions/sha256:aaa")
	}
}

func TestDeleteBatchesNeverMixesPackagesInOneRequest(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	// Interleaved on purpose: PlanSweep sorts oldest first, so one package's
	// versions do not arrive contiguously.
	batches, err := prefix.DeleteBatches([]garbagetruck.Expired{
		expiredIn(t, "roster", "sha256:a"),
		expiredIn(t, "send_monthly_admin_emails", "sha256:b"),
		expiredIn(t, "roster", "sha256:c"),
		expiredIn(t, "precache", "sha256:d"),
	})
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}

	// One batch per package, in package-name order.
	wantIDs := []string{"precache", "roster", "send_monthly_admin_emails"}
	var gotIDs []string
	for _, batch := range batches {
		gotIDs = append(gotIDs, batch.ID)
		for _, version := range batch.Names {
			if !strings.HasPrefix(version, batch.Package+"/versions/") {
				t.Errorf("batch %s carries %q, which is in another package", batch.ID, version)
			}
		}
	}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("batch ids = %v, want %v", gotIDs, wantIDs)
	}
}

func TestDeleteBatchesSplitsALargePackage(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	const versions = 450
	expired := make([]garbagetruck.Expired, 0, versions)
	for i := range versions {
		expired = append(expired, expiredIn(t, "roster", "sha256:"+strconv.Itoa(i)))
	}

	batches, err := prefix.DeleteBatches(expired)
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}

	var total int
	for _, batch := range batches {
		if len(batch.Names) > serviceMaxPerRequest {
			t.Errorf("batch of %d names exceeds the %d the service allows; "+
				"the whole request would be rejected",
				len(batch.Names), serviceMaxPerRequest)
		}
		total += len(batch.Names)
	}
	if want := versions / serviceMaxPerRequest; len(batches) != want {
		t.Errorf("got %d batches, want %d for %d versions", len(batches), want, versions)
	}
	if total != versions {
		t.Errorf("batched %d names, want %d — a sweep must not drop deletions", total, versions)
	}
}

// TestDeleteBatchesStaysUnderTheServiceCap guards the constant itself. A
// single package with one more version than the cap must come back as two
// requests, never one oversized one.
func TestDeleteBatchesStaysUnderTheServiceCap(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	expired := make([]garbagetruck.Expired, 0, serviceMaxPerRequest+1)
	for i := range serviceMaxPerRequest + 1 {
		expired = append(expired, expiredIn(t, "roster", "sha256:"+strconv.Itoa(i)))
	}

	batches, err := prefix.DeleteBatches(expired)
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("got %d batches for %d versions, want 2 — one batch would be rejected whole",
			len(batches), serviceMaxPerRequest+1)
	}
	if len(batches[0].Names) != serviceMaxPerRequest {
		t.Errorf("first batch has %d names, want %d",
			len(batches[0].Names), serviceMaxPerRequest)
	}
	if len(batches[1].Names) != 1 {
		t.Errorf("second batch has %d names, want 1", len(batches[1].Names))
	}
}

func TestDeleteBatchesKeepsVersionsOldestFirst(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	batches, err := prefix.DeleteBatches([]garbagetruck.Expired{
		expiredIn(t, "roster", "sha256:oldest"),
		expiredIn(t, "roster", "sha256:middle"),
		expiredIn(t, "roster", "sha256:newest"),
	})
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}

	want := []string{"sha256:oldest", "sha256:middle", "sha256:newest"}
	for i, digest := range want {
		if !strings.HasSuffix(batches[0].Names[i], "/versions/"+digest) {
			t.Errorf("name %d = %q, want it to end in %q",
				i, batches[0].Names[i], "/versions/"+digest)
		}
	}
}

func TestDeleteBatchesEscapesNestedPackageIDs(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	batches, err := prefix.DeleteBatches([]garbagetruck.Expired{
		expiredIn(t, "team/nested", "sha256:aaa"),
	})
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}

	// A package id is one path segment, so a slash inside it must be escaped.
	want := jobsParent + "/packages/team%2Fnested"
	if batches[0].Package != want {
		t.Errorf("Package = %q, want %q", batches[0].Package, want)
	}
	if batches[0].ID != "team/nested" {
		t.Errorf("ID = %q, want %q", batches[0].ID, "team/nested")
	}
}

func TestDeleteBatchesRejectsAForeignImage(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	elsewhere, err := name.NewRepository(
		"us-central1-docker.pkg.dev/other-project/other-repo/roster",
		name.WithDefaultRegistry(""))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}

	batches, err := prefix.DeleteBatches([]garbagetruck.Expired{
		expiredIn(t, "roster", "sha256:aaa"),
		{Repo: elsewhere, Digest: "sha256:bbb"},
	})
	if !errors.Is(err, registry.ErrBadPrefix) {
		t.Fatalf("err = %v, want ErrBadPrefix", err)
	}
	if batches != nil {
		t.Error("returned batches alongside an error; a partial deletion set must not escape")
	}
}

func TestDeleteBatchesOnNothing(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	batches, err := prefix.DeleteBatches(nil)
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}
	if len(batches) != 0 {
		t.Errorf("got %d batches for no expired versions, want 0", len(batches))
	}
}

// The collapsing tests drive DeleteError through the same accumulator the
// client uses. They assert on the rendered message because that rendering is
// the whole point: the reason a sweep failed has to survive at the bottom of a
// thousand-batch run.

func TestDeleteErrorCollapsesOneCauseAcrossManyBatches(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	// 300 versions across two packages: 6 batches of 50, all failing alike.
	var expired []garbagetruck.Expired
	for i := range 150 {
		expired = append(expired,
			expiredIn(t, "roster", "sha256:r"+strconv.Itoa(i)),
			expiredIn(t, "precache", "sha256:p"+strconv.Itoa(i)))
	}
	batches, err := prefix.DeleteBatches(expired)
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}
	if len(batches) != 6 {
		t.Fatalf("got %d batches, want 6", len(batches))
	}

	cap50 := errors.New("rpc error: A maximum of 50 versions are allowed per request")
	failed := registry.CollapseForTest(batches, func(registry.DeleteBatch) error { return cap50 })

	var target *registry.DeleteError
	if !errors.As(failed, &target) {
		t.Fatalf("err = %T, want *registry.DeleteError", failed)
	}
	if target.Batches() != 6 {
		t.Errorf("Batches() = %d, want 6", target.Batches())
	}
	if target.Versions() != 300 {
		t.Errorf("Versions() = %d, want 300", target.Versions())
	}

	// One line of summary plus exactly one line for the single cause.
	lines := strings.Split(target.Error(), "\n")
	if len(lines) != 2 {
		t.Fatalf("rendered %d lines, want 2:\n%s", len(lines), target.Error())
	}
	if !strings.Contains(lines[0], "300 versions in 6 batches not deleted") {
		t.Errorf("summary = %q", lines[0])
	}
	if !strings.Contains(lines[1], "6 batches, 300 versions in 2 packages") {
		t.Errorf("cause line = %q", lines[1])
	}
	if !strings.Contains(lines[1], "maximum of 50 versions") {
		t.Error("cause line dropped the reason, which is the one thing it must keep")
	}
	// errors.Is must still reach the cause, once, not six times.
	if !errors.Is(failed, cap50) {
		t.Error("errors.Is cannot reach the underlying cause")
	}
}

func TestDeleteErrorKeepsDistinctCausesApart(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	var expired []garbagetruck.Expired
	for i := range 60 {
		expired = append(expired, expiredIn(t, "roster", "sha256:r"+strconv.Itoa(i)))
	}
	expired = append(expired, expiredIn(t, "precache", "sha256:p0"))
	batches, err := prefix.DeleteBatches(expired)
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}

	denied := errors.New("PermissionDenied")
	tooBig := errors.New("A maximum of 50 versions are allowed per request")
	failed := registry.CollapseForTest(batches, func(batch registry.DeleteBatch) error {
		if batch.ID == "precache" {
			return denied
		}
		return tooBig
	})

	var target *registry.DeleteError
	if !errors.As(failed, &target) {
		t.Fatalf("err = %T, want *registry.DeleteError", failed)
	}
	// Two distinct causes must not be merged into one count.
	lines := strings.Split(target.Error(), "\n")
	if len(lines) != 3 {
		t.Fatalf("rendered %d lines, want 3 (summary + 2 causes):\n%s",
			len(lines), target.Error())
	}
	if !errors.Is(failed, denied) || !errors.Is(failed, tooBig) {
		t.Error("errors.Is must reach both distinct causes")
	}
}

func TestDeleteErrorLeavesASingleBatchFailureUncollapsed(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	batches, err := prefix.DeleteBatches([]garbagetruck.Expired{
		expiredIn(t, "roster", "sha256:aaa"),
	})
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}

	boom := errors.New("PermissionDenied")
	failed := registry.CollapseForTest(batches, func(registry.DeleteBatch) error { return boom })

	// A count of one adds only noise, so one batch reads as one plain line.
	want := "delete 1 versions in roster: PermissionDenied"
	if got := failed.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if strings.Contains(failed.Error(), "\n") {
		t.Error("a single failure should render on one line")
	}
}

func TestDeleteErrorTruncatesALongPackageList(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}

	// Ten packages, one expired version each, all failing for one reason.
	var expired []garbagetruck.Expired
	for i := range 10 {
		expired = append(expired, expiredIn(t, "pkg"+strconv.Itoa(i), "sha256:a"))
	}
	batches, err := prefix.DeleteBatches(expired)
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}

	boom := errors.New("A maximum of 50 versions are allowed per request")
	failed := registry.CollapseForTest(batches, func(registry.DeleteBatch) error { return boom })

	rendered := failed.Error()
	if !strings.Contains(rendered, "10 packages") {
		t.Errorf("want the full package count, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "and 5 more") {
		t.Errorf("want a truncated tail, got:\n%s", rendered)
	}
	// The whole point is bounded output: never one line per package.
	if lines := strings.Split(rendered, "\n"); len(lines) != 2 {
		t.Errorf("rendered %d lines, want 2:\n%s", len(lines), rendered)
	}
}

func TestDeleteErrorIsNilWhenEveryBatchSucceeds(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}
	batches, err := prefix.DeleteBatches([]garbagetruck.Expired{
		expiredIn(t, "roster", "sha256:aaa"),
	})
	if err != nil {
		t.Fatalf("DeleteBatches: %v", err)
	}

	if failed := registry.CollapseForTest(batches, func(registry.DeleteBatch) error {
		return nil
	}); failed != nil {
		t.Errorf("err = %v, want nil when nothing failed", failed)
	}
}
