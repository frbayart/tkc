package policy

import "time"

// Capability is a communication action subject to authorization.
type Capability string

// Capabilities.
const (
	SendText         Capability = "text"
	SendVoiceMessage Capability = "voice_message"
	SendImage        Capability = "image"
	SendAttachment   Capability = "attachment"
	AudioCall        Capability = "audio_call"
	VideoCall        Capability = "video_call"
)

// Reason explains an authorization decision.
type Reason string

// Decision reasons.
const (
	ReasonAllowed            Reason = "allowed"
	ReasonCapabilityDenied   Reason = "capability_denied"
	ReasonOutsideSchedule    Reason = "outside_schedule"
	ReasonAllowanceExhausted Reason = "daily_allowance_exhausted"
	ReasonUnknownCapability  Reason = "unknown_capability"
)

// Request is an authorization request evaluated at instant At.
type Request struct {
	Capability Capability
	At         time.Time
	// CallTimeUsedToday is the call time already used on this relationship,
	// counted in each side's local day. Only used for calls.
	CallTimeUsedToday [2]time.Duration
}

// Decision is the result of Authorize.
type Decision struct {
	Allowed bool
	Reason  Reason
	// MaxDuration is set for allowed calls: the call must be terminated by the
	// infrastructure after this duration. It is the minimum of the per-call
	// limit, the remaining allowed schedule window and the remaining daily
	// allowances.
	MaxDuration time.Duration
}

// maxLookahead bounds the schedule expansion used to find the end of the
// current window. Windows longer than this are capped, which is safe: the
// call is simply re-authorized.
const maxLookahead = 48 * time.Hour

// Authorize evaluates a request against the effective policy.
func (e Effective) Authorize(r Request) Decision {
	switch r.Capability {
	case SendText:
		return boolDecision(e.Messaging.Text)
	case SendVoiceMessage:
		return boolDecision(e.Messaging.VoiceMessages)
	case SendImage:
		return boolDecision(e.Messaging.Images)
	case SendAttachment:
		return boolDecision(e.Messaging.Attachments)
	case AudioCall:
		if !e.Audio.Enabled {
			return deny(ReasonCapabilityDenied)
		}
		return e.authorizeCall(r)
	case VideoCall:
		// A video call carries audio, so it requires both permissions.
		if !e.Video.Enabled || !e.Audio.Enabled {
			return deny(ReasonCapabilityDenied)
		}
		return e.authorizeCall(r)
	default:
		return deny(ReasonUnknownCapability)
	}
}

func (e Effective) authorizeCall(r Request) Decision {
	var current *Interval
	for _, iv := range e.AllowedCallPeriods(r.At, r.At.Add(maxLookahead)) {
		if iv.Contains(r.At) {
			current = &iv
			break
		}
	}
	if current == nil {
		return deny(ReasonOutsideSchedule)
	}

	limit := current.End.Sub(r.At)
	if e.MaxCall > 0 {
		limit = min(limit, e.MaxCall)
	}
	for s := range 2 {
		if allowance := e.DailyAllowance[s]; allowance > 0 {
			remaining := allowance - r.CallTimeUsedToday[s]
			if remaining <= 0 {
				return deny(ReasonAllowanceExhausted)
			}
			// The allowance resets at the next local midnight of that side;
			// the call is still capped at the current remaining allowance and
			// must be re-authorized to continue past midnight.
			limit = min(limit, remaining)
		}
	}
	return Decision{Allowed: true, Reason: ReasonAllowed, MaxDuration: limit}
}

func boolDecision(ok bool) Decision {
	if ok {
		return Decision{Allowed: true, Reason: ReasonAllowed}
	}
	return deny(ReasonCapabilityDenied)
}

func deny(r Reason) Decision { return Decision{Allowed: false, Reason: r} }
