// Package retention computes, deterministically, which encrypted call
// recordings must be kept or deleted, as specified in spec/retention.md.
//
// Plan is a pure function: given the recordings of one relationship, the
// effective recording policy and the current instant, it returns a decision
// for every recording. A scheduler applies the deletions and re-runs Plan at
// NextEvaluation (or when a recording is added or the policy changes).
//
// Rules, with recordings ranked from the most recent (rank 0):
//
//   - recent: a recording is kept while it is younger than RetentionDays and,
//     if KeepRecentCount > 0, its rank is below KeepRecentCount;
//   - long term: a recording is kept while its rank is below KeepLongTermCount
//     and it is younger than LongTermMaxAgeDays;
//   - otherwise it is deleted. There is no indefinite retention: a zero age
//     limit keeps nothing under that rule (fail closed);
//   - if recording is disabled by the effective policy, every recording is
//     deleted.
//
// Count limits use 0 to mean "no count limit", consistent with package policy.
package retention

import (
	"cmp"
	"slices"
	"time"

	"github.com/frbayart/tkc/server/internal/policy"
)

// Recording is the lifecycle metadata of one encrypted recording. The content
// itself is opaque to this package.
type Recording struct {
	ID        string
	StartedAt time.Time
}

// Reason explains why a recording is kept or deleted, for display to parents.
type Reason string

// Reasons.
const (
	// Kept.
	ReasonRecent   Reason = "recent"
	ReasonLongTerm Reason = "long_term"
	// Deleted.
	ReasonRecordingDisabled Reason = "recording_disabled"
	ReasonCountExceeded     Reason = "count_exceeded"
	ReasonExpired           Reason = "expired"
)

// Decision is the plan for one recording.
type Decision struct {
	Recording Recording
	Keep      bool
	Reason    Reason
	// ExpiresAt is set for kept recordings: the instant at which the
	// recording will be deleted at the latest. It can be brought forward when
	// newer recordings push it out of a count limit or the policy changes.
	ExpiresAt time.Time
}

// Plan is the result of planning a relationship's recordings.
type Plan struct {
	// Decisions holds one decision per recording, most recent first.
	Decisions []Decision
	// NextEvaluation is the earliest ExpiresAt among kept recordings, or the
	// zero time if nothing is kept.
	NextEvaluation time.Time
}

// Keep returns the kept decisions.
func (p Plan) Keep() []Decision { return p.filter(true) }

// Delete returns the deletion decisions.
func (p Plan) Delete() []Decision { return p.filter(false) }

func (p Plan) filter(keep bool) []Decision {
	var out []Decision
	for _, d := range p.Decisions {
		if d.Keep == keep {
			out = append(out, d)
		}
	}
	return out
}

const day = 24 * time.Hour

// Compute plans the retention of recordings under the effective policy at now.
func Compute(recordings []Recording, p policy.RecordingPolicy, now time.Time) Plan {
	recs := slices.Clone(recordings)
	// Most recent first; ties broken by ID so the result is deterministic.
	slices.SortFunc(recs, func(a, b Recording) int {
		if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})

	plan := Plan{Decisions: make([]Decision, 0, len(recs))}
	for rank, r := range recs {
		d := decide(r, rank, p, now)
		if d.Keep && (plan.NextEvaluation.IsZero() || d.ExpiresAt.Before(plan.NextEvaluation)) {
			plan.NextEvaluation = d.ExpiresAt
		}
		plan.Decisions = append(plan.Decisions, d)
	}
	return plan
}

func decide(r Recording, rank int, p policy.RecordingPolicy, now time.Time) Decision {
	if !p.Enabled {
		return Decision{Recording: r, Reason: ReasonRecordingDisabled}
	}

	recentEnd := r.StartedAt.Add(time.Duration(p.RetentionDays) * day)
	recentAge := now.Before(recentEnd)
	recentRank := p.KeepRecentCount == 0 || rank < p.KeepRecentCount

	longEnd := r.StartedAt.Add(time.Duration(p.LongTermMaxAgeDays) * day)
	longTerm := rank < p.KeepLongTermCount && now.Before(longEnd)

	recent := recentAge && recentRank
	switch {
	case longTerm && (!recent || longEnd.After(recentEnd)):
		return Decision{Recording: r, Keep: true, Reason: ReasonLongTerm, ExpiresAt: longEnd}
	case recent:
		return Decision{Recording: r, Keep: true, Reason: ReasonRecent, ExpiresAt: recentEnd}
	case recentAge:
		return Decision{Recording: r, Reason: ReasonCountExceeded}
	default:
		return Decision{Recording: r, Reason: ReasonExpired}
	}
}
