package registry

// This file is compiled only for tests, so what it exports is reachable from
// registry_test without becoming part of the package's API.

// CollapseForTest drives the failure accumulator DeleteVersions uses, calling
// attempt for each batch and returning the collapsed error.
//
// It exists because the collapsing is worth testing on its own: exercising it
// through DeleteVersions would need a registry, and what matters here is the
// grouping and rendering, which are pure.
func CollapseForTest(batches []DeleteBatch, attempt func(DeleteBatch) error) error {
	var failures deleteFailures
	for _, batch := range batches {
		if err := attempt(batch); err != nil {
			failures.add(batch, err)
		}
	}
	return failures.err()
}
