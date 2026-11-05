package policy

import (
	"fmt"
	"time"
)

// Side identifies one of the two families of a relationship.
type Side int

// Relationship sides.
const (
	SideA Side = 0
	SideB Side = 1
)

// Effective is the effective policy of a relationship: the intersection of the
// two families' policies.
//
// Boolean capabilities and numeric limits are merged eagerly. Schedules cannot
// be merged into a single local schedule when the children live in different
// time zones, so both are kept with their locations and intersected as
// absolute intervals on demand (see AllowedCallPeriods).
type Effective struct {
	Messaging MessagingPolicy
	Audio     CallPolicy
	Video     CallPolicy

	// MaxCall is the per-call limit; 0 means unlimited.
	MaxCall time.Duration

	// DailyAllowance holds each family's daily call allowance, counted in that
	// family's local day; 0 means unlimited.
	DailyAllowance [2]time.Duration

	RequireIncomingAcceptance bool
	Recording                 RecordingPolicy

	schedules [2]Schedule
	locations [2]*time.Location
}

// Intersect computes the effective policy of a relationship from the policies
// of family A and family B. It is commutative up to side labelling and never
// grants a capability denied by either family.
func Intersect(a, b FamilyPolicy) (Effective, error) {
	if err := a.Validate(); err != nil {
		return Effective{}, fmt.Errorf("family A: %w", err)
	}
	if err := b.Validate(); err != nil {
		return Effective{}, fmt.Errorf("family B: %w", err)
	}
	locA, _ := time.LoadLocation(a.TimeZone)
	locB, _ := time.LoadLocation(b.TimeZone)

	return Effective{
		Messaging: MessagingPolicy{
			Text:          a.Messaging.Text && b.Messaging.Text,
			VoiceMessages: a.Messaging.VoiceMessages && b.Messaging.VoiceMessages,
			Images:        a.Messaging.Images && b.Messaging.Images,
			Attachments:   a.Messaging.Attachments && b.Messaging.Attachments,
		},
		Audio:                     CallPolicy{Enabled: a.Audio.Enabled && b.Audio.Enabled},
		Video:                     CallPolicy{Enabled: a.Video.Enabled && b.Video.Enabled},
		MaxCall:                   minutes(minLimit(a.MaxCallMinutes, b.MaxCallMinutes)),
		DailyAllowance:            [2]time.Duration{minutes(a.DailyCallMinutes), minutes(b.DailyCallMinutes)},
		RequireIncomingAcceptance: a.RequireIncomingAcceptance || b.RequireIncomingAcceptance,
		Recording:                 intersectRecording(a.Recording, b.Recording),
		schedules:                 [2]Schedule{a.CallSchedule, b.CallSchedule},
		locations:                 [2]*time.Location{locA, locB},
	}, nil
}

// AllowedCallPeriods returns the absolute periods within [from, to) during
// which calls are allowed by both families' schedules. Intended both for
// enforcement and for showing parents the final behaviour.
func (e Effective) AllowedCallPeriods(from, to time.Time) []Interval {
	return IntersectIntervals(
		e.schedules[SideA].Periods(e.locations[SideA], from, to),
		e.schedules[SideB].Periods(e.locations[SideB], from, to),
	)
}

// Location returns the time zone of the given side.
func (e Effective) Location(s Side) *time.Location { return e.locations[s] }

func intersectRecording(a, b RecordingPolicy) RecordingPolicy {
	switch {
	case a.Enabled && b.Enabled:
		return RecordingPolicy{
			Enabled:            true,
			RetentionDays:      minLimit(a.RetentionDays, b.RetentionDays),
			KeepRecentCount:    minLimit(a.KeepRecentCount, b.KeepRecentCount),
			KeepLongTermCount:  min(a.KeepLongTermCount, b.KeepLongTermCount),
			LongTermMaxAgeDays: minLimit(a.LongTermMaxAgeDays, b.LongTermMaxAgeDays),
		}
	case a.Enabled:
		return a
	case b.Enabled:
		return b
	default:
		return RecordingPolicy{}
	}
}

// minLimit returns the smaller of two limits where 0 means "no limit".
func minLimit(a, b int) int {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	default:
		return min(a, b)
	}
}

func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }
