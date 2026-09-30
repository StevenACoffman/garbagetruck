package registry

// This file is compiled only for tests, so what it exports is reachable from
// registry_test without becoming part of the package's API.

// CollapseForTest drives the failure accumulator DeleteVersions uses, calling
// attempt for each batch and returning the same pair DeleteVersions does: how
// many versions were deleted, and the collapsed error.
//
// attempt stands in for deleteBatch and returns the same pair, so a test can
// model a whole-batch failure (0, err) or a partial one (n, err). The loop
// here mirrors DeleteVersions deliberately: if that accounting changes, this
// must change with it or the tests stop describing the real thing.
//
// It exists because the collapsing is worth testing on its own: exercising it
// through DeleteVersions would need a registry, and what matters here is the
// grouping and rendering, which are pure.
func CollapseForTest(
	batches []DeleteBatch,
	attempt func(DeleteBatch) (int, error),
) (int, error) {
	var (
		deleted  int
		failures deleteFailures
	)
	for _, batch := range batches {
		done, err := attempt(batch)
		deleted += done
		if err != nil {
			failures.add(batch, len(batch.Names)-done, err)
		}
	}
	return deleted, failures.err()
}
