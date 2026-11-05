package policy

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func mustIntersect(t *testing.T, a, b FamilyPolicy) Effective {
	t.Helper()
	e, err := Intersect(a, b)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Example from spec/relationship-policies.md.
func specFamilies() (FamilyPolicy, FamilyPolicy) {
	a := FamilyPolicy{
		TimeZone:       "Europe/Paris",
		Messaging:      MessagingPolicy{Text: true},
		Audio:          CallPolicy{Enabled: true},
		Video:          CallPolicy{Enabled: true},
		CallSchedule:   Daily(Clock(8, 0), Clock(20, 0)),
		MaxCallMinutes: 30,
	}
	b := FamilyPolicy{
		TimeZone:       "Europe/Paris",
		Messaging:      MessagingPolicy{Text: true},
		Audio:          CallPolicy{Enabled: true},
		Video:          CallPolicy{Enabled: false},
		CallSchedule:   Daily(Clock(9, 0), Clock(19, 0)),
		MaxCallMinutes: 20,
	}
	return a, b
}

func TestSpecExample(t *testing.T) {
	a, b := specFamilies()
	e := mustIntersect(t, a, b)
	paris := mustLoc(t, "Europe/Paris")

	if !e.Audio.Enabled || e.Video.Enabled {
		t.Fatalf("audio=%v video=%v, want audio only", e.Audio.Enabled, e.Video.Enabled)
	}
	if e.MaxCall != 20*time.Minute {
		t.Fatalf("MaxCall = %v, want 20m", e.MaxCall)
	}

	day := time.Date(2026, 10, 5, 0, 0, 0, 0, paris)
	periods := e.AllowedCallPeriods(day, day.Add(24*time.Hour))
	want := Interval{time.Date(2026, 10, 5, 9, 0, 0, 0, paris), time.Date(2026, 10, 5, 19, 0, 0, 0, paris)}
	if len(periods) != 1 || !periods[0].Start.Equal(want.Start) || !periods[0].End.Equal(want.End) {
		t.Fatalf("periods = %v, want [%v]", periods, want)
	}

	cases := []struct {
		name   string
		cap    Capability
		at     time.Time
		allow  bool
		reason Reason
		max    time.Duration
	}{
		{"audio inside window", AudioCall, time.Date(2026, 10, 5, 10, 0, 0, 0, paris), true, ReasonAllowed, 20 * time.Minute},
		{"audio near window end", AudioCall, time.Date(2026, 10, 5, 18, 50, 0, 0, paris), true, ReasonAllowed, 10 * time.Minute},
		{"audio before B window", AudioCall, time.Date(2026, 10, 5, 8, 30, 0, 0, paris), false, ReasonOutsideSchedule, 0},
		{"audio at window end", AudioCall, time.Date(2026, 10, 5, 19, 0, 0, 0, paris), false, ReasonOutsideSchedule, 0},
		{"video denied by B", VideoCall, time.Date(2026, 10, 5, 10, 0, 0, 0, paris), false, ReasonCapabilityDenied, 0},
		{"text allowed", SendText, time.Date(2026, 10, 5, 23, 0, 0, 0, paris), true, ReasonAllowed, 0},
		{"attachments denied", SendAttachment, time.Date(2026, 10, 5, 10, 0, 0, 0, paris), false, ReasonCapabilityDenied, 0},
		{"unknown capability", Capability("telepathy"), time.Date(2026, 10, 5, 10, 0, 0, 0, paris), false, ReasonUnknownCapability, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := e.Authorize(Request{Capability: tc.cap, At: tc.at})
			if d.Allowed != tc.allow || d.Reason != tc.reason || d.MaxDuration != tc.max {
				t.Fatalf("got %+v, want allowed=%v reason=%v max=%v", d, tc.allow, tc.reason, tc.max)
			}
		})
	}
}

func TestZeroPolicyDeniesEverything(t *testing.T) {
	z := FamilyPolicy{TimeZone: "UTC"}
	full := FamilyPolicy{
		TimeZone:     "UTC",
		Messaging:    MessagingPolicy{Text: true, VoiceMessages: true, Images: true, Attachments: true},
		Audio:        CallPolicy{Enabled: true},
		Video:        CallPolicy{Enabled: true},
		CallSchedule: Daily(Clock(0, 0), Clock(24, 0)),
	}
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, pair := range [][2]FamilyPolicy{{z, full}, {full, z}} {
		e := mustIntersect(t, pair[0], pair[1])
		for _, c := range []Capability{SendText, SendVoiceMessage, SendImage, SendAttachment, AudioCall, VideoCall} {
			if d := e.Authorize(Request{Capability: c, At: at}); d.Allowed {
				t.Errorf("%s allowed with a zero policy on one side", c)
			}
		}
	}
	// Both full: everything allowed.
	e := mustIntersect(t, full, full)
	for _, c := range []Capability{SendText, SendVoiceMessage, SendImage, SendAttachment, AudioCall, VideoCall} {
		if d := e.Authorize(Request{Capability: c, At: at}); !d.Allowed {
			t.Errorf("%s denied with full policies: %+v", c, d)
		}
	}
}

func TestVideoRequiresAudio(t *testing.T) {
	p := FamilyPolicy{TimeZone: "UTC", Video: CallPolicy{Enabled: true}, CallSchedule: Daily(0, 1440)}
	e := mustIntersect(t, p, p)
	if d := e.Authorize(Request{Capability: VideoCall, At: time.Unix(0, 0)}); d.Allowed {
		t.Fatal("video call allowed without audio permission")
	}
}

func TestDifferentTimeZones(t *testing.T) {
	// Family A in Paris allows 08:00–20:00 Paris time; family B in New York
	// allows 08:00–20:00 New York time. In October 2026 (both on summer time)
	// Paris is UTC+2, New York UTC-4: overlap is 12:00–18:00 UTC.
	a := FamilyPolicy{TimeZone: "Europe/Paris", Audio: CallPolicy{Enabled: true}, CallSchedule: Daily(Clock(8, 0), Clock(20, 0))}
	b := FamilyPolicy{TimeZone: "America/New_York", Audio: CallPolicy{Enabled: true}, CallSchedule: Daily(Clock(8, 0), Clock(20, 0))}
	e := mustIntersect(t, a, b)

	from := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	got := e.AllowedCallPeriods(from, from.Add(24*time.Hour))
	want := Interval{time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)}
	if len(got) != 1 || !got[0].Start.Equal(want.Start) || !got[0].End.Equal(want.End) {
		t.Fatalf("periods = %v, want [%v]", got, want)
	}
}

func TestWindowCrossingMidnight(t *testing.T) {
	p := FamilyPolicy{
		TimeZone:     "UTC",
		Audio:        CallPolicy{Enabled: true},
		CallSchedule: Schedule{Windows: []Window{{Days: Weekdays{time.Friday}, From: Clock(22, 0), Until: Clock(1, 0)}}},
	}
	e := mustIntersect(t, p, p)
	// 2026-10-09 is a Friday.
	sat0030 := time.Date(2026, 10, 10, 0, 30, 0, 0, time.UTC)
	d := e.Authorize(Request{Capability: AudioCall, At: sat0030})
	if !d.Allowed || d.MaxDuration != 30*time.Minute {
		t.Fatalf("Saturday 00:30 in Friday night window: %+v", d)
	}
	sun0030 := time.Date(2026, 10, 11, 0, 30, 0, 0, time.UTC)
	if d := e.Authorize(Request{Capability: AudioCall, At: sun0030}); d.Allowed {
		t.Fatalf("Sunday 00:30 should be outside (window starts Fridays only): %+v", d)
	}
}

func TestDaylightSavingTransition(t *testing.T) {
	// Europe/Paris leaves summer time on 2026-10-25 at 03:00 local. A
	// 00:00–12:00 local window that day lasts 13 hours of absolute time.
	paris := mustLoc(t, "Europe/Paris")
	p := FamilyPolicy{TimeZone: "Europe/Paris", Audio: CallPolicy{Enabled: true}, CallSchedule: Daily(Clock(0, 0), Clock(12, 0))}
	e := mustIntersect(t, p, p)
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, paris)
	got := e.AllowedCallPeriods(day, day.Add(20*time.Hour))
	if len(got) != 1 || got[0].Duration() != 13*time.Hour {
		t.Fatalf("periods = %v, want one 13h period", got)
	}
}

func TestDailyAllowance(t *testing.T) {
	a := FamilyPolicy{TimeZone: "UTC", Audio: CallPolicy{Enabled: true}, CallSchedule: Daily(0, 1440), DailyCallMinutes: 60}
	b := FamilyPolicy{TimeZone: "UTC", Audio: CallPolicy{Enabled: true}, CallSchedule: Daily(0, 1440), MaxCallMinutes: 45}
	e := mustIntersect(t, a, b)
	at := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	d := e.Authorize(Request{Capability: AudioCall, At: at, CallTimeUsedToday: [2]time.Duration{50 * time.Minute, 0}})
	if !d.Allowed || d.MaxDuration != 10*time.Minute {
		t.Fatalf("got %+v, want allowed for 10m", d)
	}
	d = e.Authorize(Request{Capability: AudioCall, At: at, CallTimeUsedToday: [2]time.Duration{60 * time.Minute, 0}})
	if d.Allowed || d.Reason != ReasonAllowanceExhausted {
		t.Fatalf("got %+v, want allowance exhausted", d)
	}
	// B has no allowance: B-side usage is irrelevant.
	d = e.Authorize(Request{Capability: AudioCall, At: at, CallTimeUsedToday: [2]time.Duration{0, 10 * time.Hour}})
	if !d.Allowed || d.MaxDuration != 45*time.Minute {
		t.Fatalf("got %+v, want allowed for 45m", d)
	}
}

func TestRecordingIntersection(t *testing.T) {
	rec := func(days, recent int) RecordingPolicy {
		return RecordingPolicy{Enabled: true, RetentionDays: days, KeepRecentCount: recent}
	}
	base := FamilyPolicy{TimeZone: "UTC"}

	a, b := base, base
	a.Recording = rec(7, 5)
	e := mustIntersect(t, a, b)
	if !e.Recording.Enabled || e.Recording.RetentionDays != 7 {
		t.Fatalf("recording required by A only: %+v", e.Recording)
	}

	b.Recording = rec(3, 0)
	e = mustIntersect(t, a, b)
	if e.Recording.RetentionDays != 3 || e.Recording.KeepRecentCount != 5 {
		t.Fatalf("got %+v, want retention 3 days, 5 recent", e.Recording)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		p    FamilyPolicy
		err  error
	}{
		{"empty tz", FamilyPolicy{}, ErrInvalidTimeZone},
		{"bad tz", FamilyPolicy{TimeZone: "Mars/Olympus"}, ErrInvalidTimeZone},
		{"empty window", FamilyPolicy{TimeZone: "UTC", CallSchedule: Daily(Clock(8, 0), Clock(8, 0))}, ErrInvalidWindow},
		{"window out of range", FamilyPolicy{TimeZone: "UTC", CallSchedule: Daily(Clock(8, 0), Clock(25, 0))}, ErrInvalidWindow},
		{"no days", FamilyPolicy{TimeZone: "UTC", CallSchedule: Schedule{Windows: []Window{{From: 1, Until: 2}}}}, ErrInvalidWindow},
		{"negative", FamilyPolicy{TimeZone: "UTC", MaxCallMinutes: -1}, ErrNegativeValue},
		{"recording without retention", FamilyPolicy{TimeZone: "UTC", Recording: RecordingPolicy{Enabled: true}}, ErrUnboundedRetention},
		{"long term forever", FamilyPolicy{TimeZone: "UTC", Recording: RecordingPolicy{Enabled: true, RetentionDays: 7, KeepLongTermCount: 2}}, ErrUnboundedRetention},
		{"ok", FamilyPolicy{TimeZone: "UTC", CallSchedule: Daily(Clock(20, 0), Clock(8, 0))}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.p.Validate(); !errors.Is(err, tc.err) {
				t.Fatalf("Validate() = %v, want %v", err, tc.err)
			}
		})
	}
}

func TestJSONRoundTrip(t *testing.T) {
	in := `{"time_zone":"Europe/Paris","messaging":{"text":true,"voice_messages":false,"images":false,"attachments":false},` +
		`"audio":{"enabled":true},"video":{"enabled":false},` +
		`"call_schedule":{"windows":[{"days":[1,2,3,4,5],"from":"08:00","until":"20:00"}]},` +
		`"max_call_minutes":30,"daily_call_minutes":0,"require_incoming_acceptance":false,` +
		`"recording":{"enabled":true,"retention_days":7,"keep_recent_count":5,"keep_long_term_count":2,"long_term_max_age_days":90}}`
	var p FamilyPolicy
	if err := json.Unmarshal([]byte(in), &p); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.CallSchedule.Windows[0].Until != Clock(20, 0) {
		t.Fatalf("until = %v", p.CallSchedule.Windows[0].Until)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip mismatch:\n got %s\nwant %s", out, in)
	}

	for _, bad := range []string{`"8:00"`, `"24:30"`, `"12:60"`, `"ab:cd"`} {
		var c ClockTime
		if err := json.Unmarshal([]byte(bad), &c); err == nil {
			t.Errorf("accepted bad clock time %s", bad)
		}
	}
}

// Property: Intersect never grants what either side denies, and is symmetric.
func TestIntersectionProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	zones := []string{"UTC", "Europe/Paris", "America/New_York", "Asia/Tokyo"}
	randPolicy := func() FamilyPolicy {
		from := ClockTime(r.IntN(1440))
		until := ClockTime(r.IntN(1441))
		if until == from {
			until = (from + 1) % 1440
		}
		return FamilyPolicy{
			TimeZone:         zones[r.IntN(len(zones))],
			Messaging:        MessagingPolicy{Text: r.IntN(2) == 0, Attachments: r.IntN(2) == 0},
			Audio:            CallPolicy{Enabled: r.IntN(4) != 0},
			Video:            CallPolicy{Enabled: r.IntN(2) == 0},
			CallSchedule:     Schedule{Windows: []Window{{Days: EveryDay[:1+r.IntN(7)], From: from, Until: until}}},
			MaxCallMinutes:   r.IntN(3) * 15,
			DailyCallMinutes: r.IntN(3) * 30,
		}
	}
	for range 500 {
		a, b := randPolicy(), randPolicy()
		ab, ba := mustIntersect(t, a, b), mustIntersect(t, b, a)
		ea, eb := mustIntersect(t, a, a), mustIntersect(t, b, b)
		at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(r.IntN(14*24*60)) * time.Minute)
		used := [2]time.Duration{time.Duration(r.IntN(60)) * time.Minute, time.Duration(r.IntN(60)) * time.Minute}
		for _, c := range []Capability{SendText, SendAttachment, AudioCall, VideoCall} {
			dab := ab.Authorize(Request{Capability: c, At: at, CallTimeUsedToday: used})
			dba := ba.Authorize(Request{Capability: c, At: at, CallTimeUsedToday: [2]time.Duration{used[1], used[0]}})
			if dab != dba {
				t.Fatalf("not symmetric for %s: %+v vs %+v\na=%+v\nb=%+v", c, dab, dba, a, b)
			}
			if !dab.Allowed {
				continue
			}
			da := ea.Authorize(Request{Capability: c, At: at, CallTimeUsedToday: [2]time.Duration{used[0], used[0]}})
			db := eb.Authorize(Request{Capability: c, At: at, CallTimeUsedToday: [2]time.Duration{used[1], used[1]}})
			if !da.Allowed || !db.Allowed {
				t.Fatalf("%s allowed by intersection but denied by a family\na=%+v\nb=%+v", c, a, b)
			}
			if dab.MaxDuration > da.MaxDuration || dab.MaxDuration > db.MaxDuration {
				t.Fatalf("%s: intersection max %v exceeds a family's %v / %v", c, dab.MaxDuration, da.MaxDuration, db.MaxDuration)
			}
		}
	}
}
