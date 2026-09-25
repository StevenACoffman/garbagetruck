package registry

import (
	"slices"
	"time"

	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
)

const (
	// DeleteOldID names the rule that expires versions. The id deliberately
	// does not mention a duration: an id of "delete-after-30-days" becomes a
	// lie the moment the duration is configured to anything else.
	DeleteOldID = "garbagetruck-delete-old"
	// KeepRecentID names the rule that spares the newest versions of an image
	// however old they are, so an image that stopped being rebuilt does not
	// vanish entirely.
	KeepRecentID = "garbagetruck-keep-recent"
	// KeepProtectedID names the rule that spares whatever garbagetruck sync
	// has tagged. It is the rule that gives the protected- tags their meaning.
	KeepProtectedID = "garbagetruck-keep-protected"
)

// PolicySpec is the retention garbagetruck maintains for one prefix.
type PolicySpec struct {
	// DeleteOlderThan expires versions past this age.
	DeleteOlderThan time.Duration
	// KeepMostRecent spares this many versions of each image regardless of
	// age. It is int32 because that is the width Artifact Registry accepts,
	// so the range check happens once where the flag is read rather than
	// being re-argued at the conversion.
	KeepMostRecent int32
	// CleanupDryRun installs the policies with Artifact Registry's own cleanup
	// pipeline held back, so it reports what it would delete and deletes
	// nothing. This is a property of the repository, not of garbagetruck: it
	// is written to the registry like any other setting.
	CleanupDryRun bool
}

// Policies is a repository's cleanup configuration.
type Policies struct {
	// ByID holds every policy on the repository, including ones garbagetruck
	// did not author.
	ByID map[string]*artifactregistrypb.CleanupPolicy
	// DryRun is the repository's cleanup_policy_dry_run setting.
	DryRun bool
}

// PolicyPlan is the change to a repository's cleanup configuration.
type PolicyPlan struct {
	// Create names managed policies the repository does not have yet.
	Create []string
	// Update names managed policies whose definition differs.
	Update []string
	// Unchanged names managed policies already in the wanted shape.
	Unchanged []string
	// Preserved names policies garbagetruck did not author and carries
	// through untouched.
	Preserved []string
	// ForeignDeletes are the Preserved policies that also delete. Artifact
	// Registry ORs rules together, so these delete on top of whatever
	// garbagetruck's own rule deletes, and a retention window that looks
	// changed here may not have changed in practice.
	ForeignDeletes []string
	// DryRunFrom and DryRunTo are the repository's cleanup dry-run setting
	// before and after.
	DryRunFrom bool
	DryRunTo   bool
	// Want is exactly what to write: the managed policies merged over the
	// existing ones, unmanaged policies included.
	Want Policies
}

// PlanPolicies works out how to reach managed from current.
//
// It is a merge, not a replacement. UpdateRepository with cleanup_policies in
// the field mask overwrites the entire map, so writing only garbagetruck's
// rules would silently delete every other policy on the repository.
func PlanPolicies(
	current Policies,
	managed map[string]*artifactregistrypb.CleanupPolicy,
	cleanupDryRun bool,
) PolicyPlan {
	plan := PolicyPlan{
		DryRunFrom: current.DryRun,
		DryRunTo:   cleanupDryRun,
		Want: Policies{
			ByID:   make(map[string]*artifactregistrypb.CleanupPolicy, len(current.ByID)),
			DryRun: cleanupDryRun,
		},
	}

	for id, policy := range current.ByID {
		plan.Want.ByID[id] = policy
		if _, isManaged := managed[id]; isManaged {
			continue
		}
		plan.Preserved = append(plan.Preserved, id)
		if policy.GetAction() == artifactregistrypb.CleanupPolicy_DELETE {
			plan.ForeignDeletes = append(plan.ForeignDeletes, id)
		}
	}

	for id, policy := range managed {
		plan.Want.ByID[id] = policy
		switch existing, present := current.ByID[id]; {
		case !present:
			plan.Create = append(plan.Create, id)
		case !proto.Equal(existing, policy):
			plan.Update = append(plan.Update, id)
		default:
			plan.Unchanged = append(plan.Unchanged, id)
		}
	}

	plan.sort()
	return plan
}

// Policies builds the rules garbagetruck maintains, each confined to this
// prefix's subtree.
//
// The two KEEP rules stay separate on purpose. Artifact Registry ORs rules
// against one another but ANDs the conditions inside a single rule, so merging
// them would spare only images that are both recent and protected — and delete
// everything protected but old, which is exactly what must survive.
func (p Prefix) Policies(spec PolicySpec) map[string]*artifactregistrypb.CleanupPolicy {
	scope := p.scope()
	return map[string]*artifactregistrypb.CleanupPolicy{
		DeleteOldID: {
			Id:     DeleteOldID,
			Action: artifactregistrypb.CleanupPolicy_DELETE,
			ConditionType: &artifactregistrypb.CleanupPolicy_Condition{
				Condition: &artifactregistrypb.CleanupPolicyCondition{
					PackageNamePrefixes: scope,
					OlderThan:           durationpb.New(spec.DeleteOlderThan),
				},
			},
		},
		KeepRecentID: {
			Id:     KeepRecentID,
			Action: artifactregistrypb.CleanupPolicy_KEEP,
			ConditionType: &artifactregistrypb.CleanupPolicy_MostRecentVersions{
				MostRecentVersions: &artifactregistrypb.CleanupPolicyMostRecentVersions{
					PackageNamePrefixes: scope,
					KeepCount:           proto.Int32(spec.KeepMostRecent),
				},
			},
		},
		KeepProtectedID: {
			Id:     KeepProtectedID,
			Action: artifactregistrypb.CleanupPolicy_KEEP,
			ConditionType: &artifactregistrypb.CleanupPolicy_Condition{
				Condition: &artifactregistrypb.CleanupPolicyCondition{
					PackageNamePrefixes: scope,
					TagPrefixes:         []string{garbagetruck.ProtectedPrefix},
				},
			},
		},
	}
}

// IsEmpty reports whether the plan would change nothing.
func (p *PolicyPlan) IsEmpty() bool {
	return len(p.Create) == 0 && len(p.Update) == 0 && p.DryRunFrom == p.DryRunTo
}

// sort puts every slice in a stable order, so two runs over unchanged inputs
// produce identical output.
func (p *PolicyPlan) sort() {
	slices.Sort(p.Create)
	slices.Sort(p.Update)
	slices.Sort(p.Unchanged)
	slices.Sort(p.Preserved)
	slices.Sort(p.ForeignDeletes)
}

// scope confines a rule to this prefix's subtree, or returns nil when the
// prefix is the whole repository and no confinement is needed. The values are
// package names relative to the repository, which is what
// PackageNamePrefixes matches on.
func (p Prefix) scope() []string {
	if p.Subpath == "" {
		return nil
	}
	return []string{p.Subpath}
}
