package garbagetruck_test

import (
	"slices"
	"testing"
	"time"

	"github.com/StevenACoffman/garbagetruck/internal/garbagetruck"
)

// now is the instant every sweep test is evaluated at, so ages are exact
// rather than dependent on when the suite runs.
var now = time.Date(2026, time.September, 25, 12, 0, 0, 0, time.UTC)

// thirtyDayRetention is the policy garbagetruck installs by default.
var thirtyDayRetention = garbagetruck.Retention{
	OlderThan:  30 * 24 * time.Hour,
	KeepNewest: 5,
}

func TestPlanSweepKeepsProtectedVersionsHoweverOld(t *testing.T) {
	t.Parallel()

	// The whole point of the protected- tag. An ancient version that a
	// manifest still pins must survive.
	sweep := garbagetruck.PlanSweep(
		images(t, image{roster, []version{
			aged(digestA, 400, "protected-"+webappTag, webappTag),
		}}),
		thirtyDayRetention, now,
	)

	if !sweep.IsEmpty() {
		t.Fatalf("a protected version must never expire, got %v", expiredStrings(sweep))
	}
	if sweep.Kept.Protected != 1 {
		t.Errorf("Kept.Protected = %d, want 1", sweep.Kept.Protected)
	}
}

func TestPlanSweepKeepsTheNewestRegardlessOfAge(t *testing.T) {
	t.Parallel()

	// Five ancient versions, none protected. KeepNewest spares all of them,
	// so an image that stopped being rebuilt does not disappear entirely.
	var versions []version
	for i := range 5 {
		versions = append(versions, aged(digest(byte('a'+i)), 400+i))
	}
	sweep := garbagetruck.PlanSweep(
		images(t, image{roster, versions}), thirtyDayRetention, now,
	)

	if !sweep.IsEmpty() {
		t.Fatalf("the newest 5 must survive, got %v", expiredStrings(sweep))
	}
	if sweep.Kept.Newest != 5 {
		t.Errorf("Kept.Newest = %d, want 5", sweep.Kept.Newest)
	}
}

func TestPlanSweepKeepsRecentVersions(t *testing.T) {
	t.Parallel()

	// Two versions, both younger than the window, with only one keep-newest
	// slot. The second survives on age alone, which is the rule under test:
	// falling outside the newest N is not on its own a reason to delete.
	sweep := garbagetruck.PlanSweep(
		images(t, image{roster, []version{aged(digestA, 10), aged(digestB, 20)}}),
		garbagetruck.Retention{OlderThan: 30 * 24 * time.Hour, KeepNewest: 1}, now,
	)

	if !sweep.IsEmpty() {
		t.Fatalf("neither version is old enough to delete, got %v", expiredStrings(sweep))
	}
	if sweep.Kept.Newest != 1 || sweep.Kept.TooYoung != 1 {
		t.Errorf("Kept = %+v, want one spared as newest and one as too young", sweep.Kept)
	}
}

func TestPlanSweepExpiresTheOldAndUnprotected(t *testing.T) {
	t.Parallel()

	// Six versions, all older than the window. The newest five are spared by
	// KeepNewest; the sixth is the only one with nothing to save it.
	var versions []version
	for i := range 6 {
		versions = append(versions, aged(digest(byte('a'+i)), 100+i))
	}
	sweep := garbagetruck.PlanSweep(
		images(t, image{roster, versions}), thirtyDayRetention, now,
	)

	want := []string{roster + "@" + digest('f')} // 105 days old, the oldest
	if got := expiredStrings(sweep); !slices.Equal(got, want) {
		t.Errorf("Expired = %v, want %v", got, want)
	}
	if sweep.Kept.Newest != 5 {
		t.Errorf("Kept.Newest = %d, want 5", sweep.Kept.Newest)
	}
}

func TestPlanSweepNeverDeletesAnUndatedVersion(t *testing.T) {
	t.Parallel()

	// A missing creation time is a gap in what the registry reported, not
	// evidence that the version is old. Deleting on a guess is unacceptable
	// when the operation cannot be undone.
	sweep := garbagetruck.PlanSweep(
		[]garbagetruck.Image{{
			Repo: repo(t, roster),
			Versions: []garbagetruck.Version{
				{Digest: digestA}, // zero Created
			},
		}},
		garbagetruck.Retention{OlderThan: time.Hour, KeepNewest: 0}, now,
	)

	if !sweep.IsEmpty() {
		t.Fatalf("an undated version must never expire, got %v", expiredStrings(sweep))
	}
	if sweep.Kept.Undated != 1 {
		t.Errorf("Kept.Undated = %d, want 1", sweep.Kept.Undated)
	}
}

func TestPlanSweepKeepRulesBeatTheDeleteRule(t *testing.T) {
	t.Parallel()

	// Artifact Registry evaluates keeps ahead of deletes. Pin that the local
	// sweep agrees: every one of these is old enough to delete, and every one
	// has exactly one rule sparing it.
	// One version spared by each rule, and one spared by none. Note the
	// newest-N rank runs over every version of the image, so a version the
	// protected rule already spares still occupies a keep slot, exactly as
	// Artifact Registry's mostRecentVersions does.
	sweep := garbagetruck.PlanSweep(
		images(t, image{roster, []version{
			aged(digestA, 10),                          // newest, and too young
			aged(digestB, 400, "protected-"+webappTag), // old, but protected
			aged(digest('c'), 400),                     // nothing spares this
		}}),
		garbagetruck.Retention{OlderThan: 30 * 24 * time.Hour, KeepNewest: 1}, now,
	)

	want := []string{roster + "@" + digest('c')}
	if got := expiredStrings(sweep); !slices.Equal(got, want) {
		t.Errorf("Expired = %v, want %v", got, want)
	}
}

func TestPlanSweepOrdersExpiredOldestFirst(t *testing.T) {
	t.Parallel()

	sweep := garbagetruck.PlanSweep(
		images(t, image{roster, []version{
			aged(digestA, 100), aged(digestB, 300), aged(digest('c'), 200),
		}}),
		garbagetruck.Retention{OlderThan: 30 * 24 * time.Hour, KeepNewest: 0}, now,
	)

	want := []string{
		roster + "@" + digestB,     // 300 days
		roster + "@" + digest('c'), // 200 days
		roster + "@" + digestA,     // 100 days
	}
	if got := expiredStrings(sweep); !slices.Equal(got, want) {
		t.Errorf("Expired = %v, want %v", got, want)
	}
}

func TestPlanSweepIsPerImage(t *testing.T) {
	t.Parallel()

	// KeepNewest applies to each image separately, not to the listing as a
	// whole: one busy image must not consume another image's allowance.
	sweep := garbagetruck.PlanSweep(
		images(t,
			image{roster, []version{aged(digestA, 100)}},
			image{umiChanged, []version{aged(digestB, 100)}},
		),
		garbagetruck.Retention{OlderThan: 30 * 24 * time.Hour, KeepNewest: 1}, now,
	)

	if !sweep.IsEmpty() {
		t.Errorf("each image keeps its own newest, got %v", expiredStrings(sweep))
	}
	if sweep.Kept.Newest != 2 {
		t.Errorf("Kept.Newest = %d, want 2 (one per image)", sweep.Kept.Newest)
	}
}

// aged builds a fixture version created the given number of days before now.
func aged(digest string, days int, tags ...string) version {
	return version{digest: digest, tags: tags, created: now.AddDate(0, 0, -days)}
}

// digest builds a distinct, well-formed digest from a single byte.
func digest(b byte) string {
	return "sha256:" + string(slices.Repeat([]byte{b}, 64))
}

// expiredStrings renders the doomed versions so failures name them.
func expiredStrings(sweep garbagetruck.Sweep) []string {
	var out []string
	for i := range sweep.Expired {
		out = append(out, sweep.Expired[i].String())
	}
	return out
}
