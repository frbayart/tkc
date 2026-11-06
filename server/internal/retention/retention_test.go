package retention

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/frbayart/tkc/server/internal/policy"
)

var now = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// Candidate policy from spec/retention.md.
var specPolicy = policy.RecordingPolicy{
	Enabled:            true,
	RetentionDays:      7,
	KeepRecentCount:    5,
	KeepLongTermCount:  2,
	LongTermMaxAgeDays: 90,
}

func ago(d time.Duration) time.Time { return now.Add(-d) }

func rec(id string, age time.Duration) Recording { return Recording{ID: id, StartedAt: ago(age)} }

type want struct {
	keep   bool
	reason Reason
}

func check(t *testing.T, plan Plan, expected map[string]want) {
	t.Helper()
	if len(plan.Decisions) != len(expected) {
		t.Fatalf("got %d decisions, want %d", len(plan.Decisions), len(expected))
	}
	for _, d := range plan.Decisions {
		w, ok := expected[d.Recording.ID]
		if !ok {
			t.Fatalf("unexpected recording %q", d.Recording.ID)
		}
		if d.Keep != w.keep || d.Reason != w.reason {
			t.Errorf("%s: keep=%v reason=%s, want keep=%v reason=%s", d.Recording.ID, d.Keep, d.Reason, w.keep, w.reason)
		}
		if d.Keep && !d.ExpiresAt.After(now) {
			t.Errorf("%s: kept but ExpiresAt %v not in the future", d.Recording.ID, d.ExpiresAt)
		}
		if !d.Keep && !d.ExpiresAt.IsZero() {
			t.Errorf("%s: deleted but ExpiresAt set", d.Recording.ID)
		}
	}
}

func TestSpecPolicy(t *testing.T) {
	recs := []Recording{
		rec("r1", 1*time.Hour),
		rec("r2", 1*day),
		rec("r3", 2*day),
		rec("r4", 3*day),
		rec("r5", 4*day),
		rec("r6", 5*day),   // within 7 days but beyond the 5 most recent
		rec("r7", 10*day),  // too old for recent
		rec("r8", 100*day), // too old for anything
	}
	plan := Compute(recs, specPolicy, now)
	check(t, plan, map[string]want{
		"r1": {true, ReasonLongTerm},
		"r2": {true, ReasonLongTerm},
		"r3": {true, ReasonRecent},
		"r4": {true, ReasonRecent},
		"r5": {true, ReasonRecent},
		"r6": {false, ReasonCountExceeded},
		"r7": {false, ReasonExpired},
		"r8": {false, ReasonExpired},
	})
	if got := plan.Decisions[0].Recording.ID; got != "r1" {
		t.Fatalf("decisions not sorted most recent first: %s", got)
	}
	if got, want := plan.Decisions[0].ExpiresAt, ago(time.Hour).Add(90*day); !got.Equal(want) {
		t.Fatalf("r1 expires %v, want %v", got, want)
	}
	// r5 (4 days old) is the first to expire, 3 days from now.
	if want := ago(4 * day).Add(7 * day); !plan.NextEvaluation.Equal(want) {
		t.Fatalf("NextEvaluation = %v, want %v", plan.NextEvaluation, want)
	}
	if len(plan.Keep()) != 5 || len(plan.Delete()) != 3 {
		t.Fatalf("keep=%d delete=%d", len(plan.Keep()), len(plan.Delete()))
	}
}

func TestLongTermKeepsOldLatestCalls(t *testing.T) {
	// Only two calls, a month old: kept by the long-term rule.
	recs := []Recording{rec("a", 30*day), rec("b", 31*day), rec("c", 32*day)}
	check(t, Compute(recs, specPolicy, now), map[string]want{
		"a": {true, ReasonLongTerm},
		"b": {true, ReasonLongTerm},
		"c": {false, ReasonExpired},
	})
}

func TestAbsoluteExpiration(t *testing.T) {
	// Even the latest recording is deleted after LongTermMaxAgeDays.
	check(t, Compute([]Recording{rec("old", 91*day)}, specPolicy, now), map[string]want{
		"old": {false, ReasonExpired},
	})
}

func TestRecordingDisabled(t *testing.T) {
	recs := []Recording{rec("a", time.Hour), rec("b", 2*day)}
	plan := Compute(recs, policy.RecordingPolicy{}, now)
	check(t, plan, map[string]want{
		"a": {false, ReasonRecordingDisabled},
		"b": {false, ReasonRecordingDisabled},
	})
	if !plan.NextEvaluation.IsZero() {
		t.Fatal("NextEvaluation set with nothing kept")
	}
}

func TestZeroAgeLimitsKeepNothing(t *testing.T) {
	p := policy.RecordingPolicy{Enabled: true, KeepLongTermCount: 2}
	check(t, Compute([]Recording{rec("a", time.Minute)}, p, now), map[string]want{
		"a": {false, ReasonExpired},
	})
}

func TestNoRecentCountLimit(t *testing.T) {
	p := policy.RecordingPolicy{Enabled: true, RetentionDays: 7}
	var recs []Recording
	for i := range 20 {
		recs = append(recs, rec(fmt.Sprintf("r%02d", i), time.Duration(i)*time.Hour))
	}
	if plan := Compute(recs, p, now); len(plan.Keep()) != 20 {
		t.Fatalf("kept %d, want 20", len(plan.Keep()))
	}
}

func TestDeterministicTieBreak(t *testing.T) {
	p := policy.RecordingPolicy{Enabled: true, RetentionDays: 7, KeepRecentCount: 1}
	same := ago(time.Hour)
	for _, order := range [][]Recording{
		{{ID: "b", StartedAt: same}, {ID: "a", StartedAt: same}},
		{{ID: "a", StartedAt: same}, {ID: "b", StartedAt: same}},
	} {
		check(t, Compute(order, p, now), map[string]want{
			"a": {true, ReasonRecent},
			"b": {false, ReasonCountExceeded},
		})
	}
}

func TestDoesNotMutateInput(t *testing.T) {
	recs := []Recording{rec("old", 3*day), rec("new", time.Hour)}
	Compute(recs, specPolicy, now)
	if recs[0].ID != "old" {
		t.Fatal("input slice was reordered")
	}
}

// Properties: retained recordings never exceed the policy's bounds, and a
// recording deleted now is never kept later without being re-added
// (deletion is final over time).
func TestProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 300 {
		p := policy.RecordingPolicy{
			Enabled:            true,
			RetentionDays:      1 + r.IntN(14),
			KeepRecentCount:    r.IntN(6),
			KeepLongTermCount:  r.IntN(4),
			LongTermMaxAgeDays: r.IntN(120),
		}
		var recs []Recording
		for i := range r.IntN(15) {
			recs = append(recs, rec(fmt.Sprintf("r%02d", i), time.Duration(r.IntN(150*24))*time.Hour))
		}
		plan := Compute(recs, p, now)

		maxKept := p.KeepLongTermCount
		if p.KeepRecentCount > 0 {
			maxKept += p.KeepRecentCount
		} else {
			maxKept = len(recs)
		}
		if len(plan.Keep()) > maxKept {
			t.Fatalf("kept %d > bound %d for %+v", len(plan.Keep()), maxKept, p)
		}
		maxAge := time.Duration(max(p.RetentionDays, p.LongTermMaxAgeDays)) * day
		for _, d := range plan.Keep() {
			if d.ExpiresAt.Sub(d.Recording.StartedAt) > maxAge {
				t.Fatalf("expiry beyond max age: %+v", d)
			}
		}

		later := Compute(recs, p, now.Add(time.Duration(1+r.IntN(30*24))*time.Hour))
		kept := map[string]bool{}
		for _, d := range later.Keep() {
			kept[d.Recording.ID] = true
		}
		for _, d := range plan.Delete() {
			if kept[d.Recording.ID] {
				t.Fatalf("%s deleted now but kept later for %+v", d.Recording.ID, p)
			}
		}
	}
}

func TestPreservationHold(t *testing.T) {
	recs := []Recording{rec("new", time.Hour), rec("old", 30*day), rec("ancient", 200*day)}
	holdUntil := now.Add(70 * day)

	plan := ComputeWithHold(recs, specPolicy, now, holdUntil)
	for _, d := range plan.Decisions {
		if !d.Keep {
			t.Fatalf("%s deleted under hold", d.Recording.ID)
		}
	}
	// The latest recording's normal expiry (90 days) is later than the hold:
	// it keeps its own reason; the others are kept by the hold.
	reasons := map[string]Reason{}
	for _, d := range plan.Decisions {
		reasons[d.Recording.ID] = d.Reason
	}
	if reasons["new"] != ReasonLongTerm || reasons["old"] != ReasonPreservationHold || reasons["ancient"] != ReasonPreservationHold {
		t.Fatalf("reasons %v", reasons)
	}
	if !plan.NextEvaluation.Equal(holdUntil) {
		t.Fatalf("NextEvaluation = %v, want hold end %v", plan.NextEvaluation, holdUntil)
	}

	// The hold applies even when recording has been disabled.
	if p := ComputeWithHold(recs, policy.RecordingPolicy{}, now, holdUntil); len(p.Keep()) != 3 {
		t.Fatalf("disabled + hold kept %d", len(p.Keep()))
	}

	// An expired hold has no effect.
	expired := ComputeWithHold(recs, specPolicy, now, now.Add(-time.Second))
	normal := Compute(recs, specPolicy, now)
	if len(expired.Keep()) != len(normal.Keep()) {
		t.Fatal("expired hold changed the plan")
	}
}
