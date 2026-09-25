package registry_test

import (
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/StevenACoffman/garbagetruck/internal/registry"
)

const thirtyDays = 30 * 24 * time.Hour

func TestPrefixPolicies(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}
	policies := prefix.Policies(registry.PolicySpec{
		DeleteOlderThan: thirtyDays,
		KeepMostRecent:  5,
	})

	want := []string{
		scoped(t, registry.DeleteOldID),
		scoped(t, registry.KeepProtectedID),
		scoped(t, registry.KeepRecentID),
	}
	if got := ids(policies); !slices.Equal(got, want) {
		t.Fatalf("policy ids = %v, want %v", got, want)
	}

	del := policies[scoped(t, registry.DeleteOldID)]
	if del.GetAction() != artifactregistrypb.CleanupPolicy_DELETE {
		t.Errorf("%s action = %v, want DELETE", registry.DeleteOldID, del.GetAction())
	}
	if got := del.GetCondition().GetOlderThan(); !proto.Equal(got, durationpb.New(thirtyDays)) {
		t.Errorf("%s OlderThan = %v, want %v", registry.DeleteOldID, got, thirtyDays)
	}

	recent := policies[scoped(t, registry.KeepRecentID)]
	if recent.GetAction() != artifactregistrypb.CleanupPolicy_KEEP {
		t.Errorf("%s action = %v, want KEEP", registry.KeepRecentID, recent.GetAction())
	}
	if got := recent.GetMostRecentVersions().GetKeepCount(); got != 5 {
		t.Errorf("%s KeepCount = %d, want 5", registry.KeepRecentID, got)
	}

	protected := policies[scoped(t, registry.KeepProtectedID)]
	if protected.GetAction() != artifactregistrypb.CleanupPolicy_KEEP {
		t.Errorf("%s action = %v, want KEEP", registry.KeepProtectedID, protected.GetAction())
	}
	wantTags := []string{"protected-"}
	if got := protected.GetCondition().GetTagPrefixes(); !slices.Equal(got, wantTags) {
		t.Errorf("%s TagPrefixes = %v, want %v", registry.KeepProtectedID, got, wantTags)
	}
}

func TestPrefixPoliciesKeepRulesStaySeparate(t *testing.T) {
	t.Parallel()

	// Artifact Registry ORs rules against each other but ANDs the conditions
	// inside one rule. Merged into a single KEEP, an image would have to be
	// both recent and protected to survive — and everything protected but old,
	// which is precisely what must be kept, would be deleted.
	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}
	policies := prefix.Policies(registry.PolicySpec{DeleteOlderThan: thirtyDays, KeepMostRecent: 5})

	recent := policies[scoped(t, registry.KeepRecentID)].GetCondition()
	if recent.GetTagPrefixes() != nil {
		t.Errorf(
			"the keep-recent rule must not also constrain tags, got %v",
			recent.GetTagPrefixes(),
		)
	}
	if policies[scoped(t, registry.KeepProtectedID)].GetMostRecentVersions() != nil {
		t.Error("the keep-protected rule must not also constrain version count")
	}
}

func TestPrefixPoliciesScope(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		prefix string
		want   []string
	}{
		// The repository itself needs no confinement: every package in it is
		// in scope already.
		"whole repository": {districtsJobs, nil},
		"subpath":          {districtsJobs + "/ltv2-", []string{"ltv2-"}},
		"nested subpath":   {districtsJobs + "/a/b", []string{"a/b"}},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()

			prefix, err := registry.ParsePrefix(tc.prefix)
			if err != nil {
				t.Fatalf("ParsePrefix(%q): %v", tc.prefix, err)
			}
			policies := prefix.Policies(registry.PolicySpec{
				DeleteOlderThan: thirtyDays, KeepMostRecent: 5,
			})

			// Every rule must carry the same scope. A DELETE rule that lost it
			// would reach another team's images; a KEEP rule that lost it
			// would quietly protect more than asked.
			for id, policy := range policies {
				if got := policyScope(policy); !slices.Equal(got, tc.want) {
					t.Errorf("%s PackageNamePrefixes = %v, want %v", id, got, tc.want)
				}
			}
		})
	}
}

func TestPlanPoliciesOnAnEmptyRepository(t *testing.T) {
	t.Parallel()

	managed := managedPolicies(t)
	plan := registry.PlanPolicies(registry.Policies{}, managed, false)

	want := []string{
		scoped(t, registry.DeleteOldID),
		scoped(t, registry.KeepProtectedID),
		scoped(t, registry.KeepRecentID),
	}
	if got := plan.Create; !slices.Equal(got, want) {
		t.Errorf("Create = %v, want %v", got, want)
	}
	if len(plan.Update) != 0 || len(plan.Unchanged) != 0 || len(plan.Preserved) != 0 {
		t.Errorf("nothing exists yet, so nothing can be updated or preserved: %+v", plan)
	}
	if plan.IsEmpty() {
		t.Error("a plan that creates three policies is not empty")
	}
}

func TestPlanPoliciesIsEmptyWhenAlreadyInstalled(t *testing.T) {
	t.Parallel()

	managed := managedPolicies(t)
	current := registry.Policies{ByID: managed}

	plan := registry.PlanPolicies(current, managedPolicies(t), false)
	if !plan.IsEmpty() {
		t.Errorf("re-running against an installed policy set must change nothing: %+v", plan)
	}
	if len(plan.Unchanged) != 3 {
		t.Errorf("Unchanged = %v, want all three", plan.Unchanged)
	}
}

func TestPlanPoliciesUpdatesAChangedRule(t *testing.T) {
	t.Parallel()

	prefix, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}
	current := registry.Policies{
		ByID: prefix.Policies(registry.PolicySpec{DeleteOlderThan: thirtyDays, KeepMostRecent: 5}),
	}
	wider := prefix.Policies(
		registry.PolicySpec{DeleteOlderThan: 60 * 24 * time.Hour, KeepMostRecent: 5},
	)

	plan := registry.PlanPolicies(current, wider, false)
	if want := []string{scoped(t, registry.DeleteOldID)}; !slices.Equal(plan.Update, want) {
		t.Errorf("Update = %v, want %v", plan.Update, want)
	}
	if len(plan.Unchanged) != 2 {
		t.Errorf("Unchanged = %v, want the two keep rules", plan.Unchanged)
	}
}

func TestPlanPoliciesPreservesPoliciesItDoesNotOwn(t *testing.T) {
	t.Parallel()

	// UpdateRepository replaces the whole cleanup_policies map, so anything
	// garbagetruck does not carry forward is destroyed.
	foreign := &artifactregistrypb.CleanupPolicy{
		Id:     "someone-elses-rule",
		Action: artifactregistrypb.CleanupPolicy_KEEP,
	}
	current := registry.Policies{
		ByID: map[string]*artifactregistrypb.CleanupPolicy{"someone-elses-rule": foreign},
	}

	plan := registry.PlanPolicies(current, managedPolicies(t), false)

	if got := plan.Preserved; !slices.Equal(got, []string{"someone-elses-rule"}) {
		t.Errorf("Preserved = %v, want the foreign rule", got)
	}
	if got := plan.Want.ByID["someone-elses-rule"]; !proto.Equal(got, foreign) {
		t.Error("the foreign rule must survive into what gets written")
	}
	if len(plan.Want.ByID) != 4 {
		t.Errorf("Want has %d policies, want three managed plus one foreign", len(plan.Want.ByID))
	}
}

func TestPlanPoliciesReportsAForeignDeleteRule(t *testing.T) {
	t.Parallel()

	// A hand-installed delete rule ORs with garbagetruck's own, so the union
	// deletes more than this tool governs. Silently carrying it forward would
	// let someone believe they had widened a retention window when they had
	// not.
	current := registry.Policies{
		ByID: map[string]*artifactregistrypb.CleanupPolicy{
			"delete-after-30-days": {
				Id:     "delete-after-30-days",
				Action: artifactregistrypb.CleanupPolicy_DELETE,
			},
			"keep-something": {
				Id:     "keep-something",
				Action: artifactregistrypb.CleanupPolicy_KEEP,
			},
		},
	}

	plan := registry.PlanPolicies(current, managedPolicies(t), false)

	if got := plan.ForeignDeletes; !slices.Equal(got, []string{"delete-after-30-days"}) {
		t.Errorf("ForeignDeletes = %v, want the hand-installed delete rule", got)
	}
	if len(plan.Preserved) != 2 {
		t.Errorf("Preserved = %v, want both foreign rules", plan.Preserved)
	}
}

func TestPlanPoliciesTracksTheCleanupDryRunSetting(t *testing.T) {
	t.Parallel()

	managed := managedPolicies(t)
	current := registry.Policies{ByID: managed, DryRun: false}

	plan := registry.PlanPolicies(current, managedPolicies(t), true)
	if plan.DryRunFrom || !plan.DryRunTo {
		t.Errorf("DryRun %v -> %v, want false -> true", plan.DryRunFrom, plan.DryRunTo)
	}
	if plan.IsEmpty() {
		t.Error("turning the cleanup pipeline off is a change, even with no policy edits")
	}
	if !plan.Want.DryRun {
		t.Error("Want.DryRun must carry the new setting")
	}
}

// scoped returns the policy id the test prefix qualifies base into, so the
// tests track the naming scheme instead of restating it.
func scoped(t *testing.T, base string) string {
	t.Helper()

	parsed, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", districtsJobs, err)
	}
	return parsed.PolicyID(base)
}

// managedPolicies builds the rules garbagetruck maintains for the test prefix.
func managedPolicies(t *testing.T) map[string]*artifactregistrypb.CleanupPolicy {
	t.Helper()

	parsed, err := registry.ParsePrefix(districtsJobs)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", districtsJobs, err)
	}
	return parsed.Policies(registry.PolicySpec{DeleteOlderThan: thirtyDays, KeepMostRecent: 5})
}

// policyScope returns the package name prefixes a rule is confined to,
// whichever of the two condition shapes it uses.
func policyScope(policy *artifactregistrypb.CleanupPolicy) []string {
	if recent := policy.GetMostRecentVersions(); recent != nil {
		return recent.GetPackageNamePrefixes()
	}
	return policy.GetCondition().GetPackageNamePrefixes()
}

// ids returns the sorted policy ids, so failures name them in a stable order.
func ids(policies map[string]*artifactregistrypb.CleanupPolicy) []string {
	out := make([]string, 0, len(policies))
	for id := range policies {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func TestPlanPoliciesIgnoresServerPopulatedTagState(t *testing.T) {
	t.Parallel()

	// Artifact Registry echoes tag_state back as TAG_STATE_UNSPECIFIED on
	// every condition it stores, where garbagetruck sends the field unset.
	// proto.Equal distinguishes those, so without normalization every run
	// reports the two condition-based policies as needing an update, rewrites
	// them for no effect, and never converges. Observed against a live
	// repository; this pins the fix.
	stored := registry.Policies{ByID: managedPolicies(t)}
	for _, policy := range stored.ByID {
		if condition := policy.GetCondition(); condition != nil {
			condition.TagState = artifactregistrypb.
				CleanupPolicyCondition_TAG_STATE_UNSPECIFIED.Enum()
		}
	}

	plan := registry.PlanPolicies(stored, managedPolicies(t), false)
	if !plan.IsEmpty() {
		t.Errorf("re-running against an unchanged repository must change nothing, got %+v", plan)
	}
	if len(plan.Unchanged) != 3 {
		t.Errorf("Unchanged = %v, want all three", plan.Unchanged)
	}
}

func TestPlanPoliciesKeepsAnotherPrefixesScopedPolicies(t *testing.T) {
	t.Parallel()

	// A scoped id from a different prefix on the same repository belongs to
	// that prefix. garbagetruck must carry it through rather than treat every
	// "garbagetruck-" id as its own, which would break the case scoping
	// exists to support.
	other := "garbagetruck-delete-old-districts-jobs-ltv2"
	current := registry.Policies{
		ByID: map[string]*artifactregistrypb.CleanupPolicy{
			other: {Id: other, Action: artifactregistrypb.CleanupPolicy_DELETE},
		},
	}

	plan := registry.PlanPolicies(current, managedPolicies(t), false)

	if got := plan.Preserved; !slices.Equal(got, []string{other}) {
		t.Errorf("Preserved = %v, want %v", got, []string{other})
	}
	if _, kept := plan.Want.ByID[other]; !kept {
		t.Error("another prefix's policy must survive into what gets written")
	}
}
