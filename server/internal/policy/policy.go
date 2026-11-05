// Package policy implements per-family relationship policies and their
// intersection, as specified in spec/relationship-policies.md.
//
// Design rules:
//
//   - Fail closed: the zero value of FamilyPolicy permits nothing.
//   - The effective policy is the intersection of both families' policies:
//     for every permission, the most restrictive rule wins. Intersect can never
//     grant a capability that either family denies.
//   - Time windows are evaluated in each child's configured home time zone and
//     intersected as absolute instants.
//
// Everything in this package is pure and deterministic (no I/O, no clock):
// callers pass the instant to evaluate.
package policy

import (
	"errors"
	"fmt"
	"time"
)

// FamilyPolicy is the policy one family imposes on one child relationship.
type FamilyPolicy struct {
	// TimeZone is the IANA time zone of this family's child (e.g. "Europe/Paris").
	// Schedules in this policy are interpreted in this zone.
	TimeZone string `json:"time_zone"`

	Messaging MessagingPolicy `json:"messaging"`
	Audio     CallPolicy      `json:"audio"`
	Video     CallPolicy      `json:"video"`

	// CallSchedule restricts when audio and video calls may take place.
	// An empty schedule permits no calls (fail closed).
	CallSchedule Schedule `json:"call_schedule"`

	// MaxCallMinutes is the maximum duration of a single call; 0 means no
	// per-call limit from this family.
	MaxCallMinutes int `json:"max_call_minutes"`

	// DailyCallMinutes is the total call time allowed per local day (in this
	// family's time zone); 0 means no daily allowance limit from this family.
	DailyCallMinutes int `json:"daily_call_minutes"`

	// RequireIncomingAcceptance forces the child to explicitly accept incoming
	// calls (no auto-answer).
	RequireIncomingAcceptance bool `json:"require_incoming_acceptance"`

	Recording RecordingPolicy `json:"recording"`
}

// MessagingPolicy controls asynchronous messaging capabilities.
type MessagingPolicy struct {
	Text          bool `json:"text"`
	VoiceMessages bool `json:"voice_messages"`
	Images        bool `json:"images"`
	Attachments   bool `json:"attachments"`
}

// CallPolicy controls one kind of real-time call.
type CallPolicy struct {
	Enabled bool `json:"enabled"`
}

// RecordingPolicy describes whether calls are recorded for parental review and
// how long encrypted recordings are retained.
//
// Recording is a supervision measure rather than a capability, so its
// intersection semantics differ (see Intersect): recording applies if either
// family requires it, and retention is bounded by the shortest limits among
// the families that require it. This semantics is provisional and must be
// confirmed by product/legal review (spec/retention.md, "Open questions").
type RecordingPolicy struct {
	Enabled bool `json:"enabled"`
	// RetentionDays bounds the age of retained recent recordings.
	RetentionDays int `json:"retention_days"`
	// KeepRecentCount bounds how many recent recordings are retained.
	KeepRecentCount int `json:"keep_recent_count"`
	// KeepLongTermCount is the number of latest recordings kept beyond
	// RetentionDays, up to LongTermMaxAgeDays.
	KeepLongTermCount  int `json:"keep_long_term_count"`
	LongTermMaxAgeDays int `json:"long_term_max_age_days"`
}

// Validation errors.
var (
	ErrInvalidTimeZone    = errors.New("policy: invalid time zone")
	ErrInvalidWindow      = errors.New("policy: invalid schedule window")
	ErrNegativeValue      = errors.New("policy: negative value")
	ErrUnboundedRetention = errors.New("policy: unbounded recording retention")
)

// Validate reports whether the policy is well formed.
func (p FamilyPolicy) Validate() error {
	if p.TimeZone == "" {
		return fmt.Errorf("%w: empty", ErrInvalidTimeZone)
	}
	if _, err := time.LoadLocation(p.TimeZone); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalidTimeZone, p.TimeZone, err)
	}
	if err := p.CallSchedule.Validate(); err != nil {
		return err
	}
	for name, v := range map[string]int{
		"max_call_minutes":       p.MaxCallMinutes,
		"daily_call_minutes":     p.DailyCallMinutes,
		"retention_days":         p.Recording.RetentionDays,
		"keep_recent_count":      p.Recording.KeepRecentCount,
		"keep_long_term_count":   p.Recording.KeepLongTermCount,
		"long_term_max_age_days": p.Recording.LongTermMaxAgeDays,
	} {
		if v < 0 {
			return fmt.Errorf("%w: %s=%d", ErrNegativeValue, name, v)
		}
	}
	if p.Recording.Enabled && p.Recording.RetentionDays == 0 {
		return fmt.Errorf("%w: recording requires retention_days", ErrUnboundedRetention)
	}
	if p.Recording.KeepLongTermCount > 0 && p.Recording.LongTermMaxAgeDays == 0 {
		// spec/retention.md: keeping recordings forever is discouraged; require
		// a finite maximum age for long-term retention.
		return fmt.Errorf("%w: keep_long_term_count requires long_term_max_age_days", ErrUnboundedRetention)
	}
	return nil
}
