package policy

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// ClockTime is a local wall-clock time of day, in minutes after midnight.
// Valid values are 0 (00:00) to 1440 (24:00, only meaningful as an end).
type ClockTime int

// Clock builds a ClockTime from hours and minutes.
func Clock(h, m int) ClockTime { return ClockTime(h*60 + m) }

func (c ClockTime) String() string { return fmt.Sprintf("%02d:%02d", int(c)/60, int(c)%60) }

// MarshalJSON encodes the time as "HH:MM".
func (c ClockTime) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }

// UnmarshalJSON decodes "HH:MM".
func (c *ClockTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || len(s) != 5 || m < 0 || m > 59 || h < 0 || h > 24 || (h == 24 && m != 0) {
		return fmt.Errorf("%w: bad clock time %q", ErrInvalidWindow, s)
	}
	*c = Clock(h, m)
	return nil
}

// Weekdays is a set of days of the week.
type Weekdays []time.Weekday

// EveryDay contains all seven days.
var EveryDay = Weekdays{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday}

// Has reports whether d is in the set.
func (w Weekdays) Has(d time.Weekday) bool { return slices.Contains(w, d) }

// Window is a recurring local time window. If Until <= From, the window
// crosses midnight and ends on the following day; Days refers to the day the
// window starts.
type Window struct {
	Days  Weekdays  `json:"days"`
	From  ClockTime `json:"from"`
	Until ClockTime `json:"until"`
}

// Schedule is a set of recurring windows. An empty schedule allows nothing.
type Schedule struct {
	Windows []Window `json:"windows"`
}

// Daily returns a schedule allowing [from, until) every day.
func Daily(from, until ClockTime) Schedule {
	return Schedule{Windows: []Window{{Days: EveryDay, From: from, Until: until}}}
}

// Validate reports whether all windows are well formed.
func (s Schedule) Validate() error {
	for i, w := range s.Windows {
		if w.From < 0 || w.From >= 1440 || w.Until < 0 || w.Until > 1440 {
			return fmt.Errorf("%w: window %d out of range", ErrInvalidWindow, i)
		}
		if w.From == w.Until {
			return fmt.Errorf("%w: window %d is empty", ErrInvalidWindow, i)
		}
		if len(w.Days) == 0 {
			return fmt.Errorf("%w: window %d has no days", ErrInvalidWindow, i)
		}
		for _, d := range w.Days {
			if d < time.Sunday || d > time.Saturday {
				return fmt.Errorf("%w: window %d has invalid weekday %d", ErrInvalidWindow, i, d)
			}
		}
	}
	return nil
}

// Interval is a half-open interval of absolute time [Start, End).
type Interval struct {
	Start, End time.Time
}

// Contains reports whether t is in [Start, End).
func (iv Interval) Contains(t time.Time) bool { return !t.Before(iv.Start) && t.Before(iv.End) }

// Duration returns the length of the interval.
func (iv Interval) Duration() time.Duration { return iv.End.Sub(iv.Start) }

// Periods expands the schedule, interpreted in loc, into absolute, sorted,
// non-overlapping intervals clipped to [from, to).
//
// Local wall-clock times are converted with time.Date, so windows follow
// daylight-saving transitions (a 08:00–20:00 window stays 08:00–20:00 local).
func (s Schedule) Periods(loc *time.Location, from, to time.Time) []Interval {
	if !from.Before(to) {
		return nil
	}
	var out []Interval
	// Start one day early to catch windows crossing midnight into `from`.
	lf := from.In(loc)
	day := time.Date(lf.Year(), lf.Month(), lf.Day()-1, 0, 0, 0, 0, loc)
	for ; day.Before(to); day = time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc) {
		for _, w := range s.Windows {
			if !w.Days.Has(day.Weekday()) {
				continue
			}
			start := localAt(day, 0, w.From, loc)
			endOffset := 0
			if w.Until <= w.From {
				endOffset = 1
			}
			end := localAt(day, endOffset, w.Until, loc)
			iv := clip(Interval{start, end}, from, to)
			if iv.Start.Before(iv.End) {
				out = append(out, iv)
			}
		}
	}
	return merge(out)
}

func localAt(day time.Time, addDays int, c ClockTime, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day()+addDays, int(c)/60, int(c)%60, 0, 0, loc)
}

func clip(iv Interval, from, to time.Time) Interval {
	if iv.Start.Before(from) {
		iv.Start = from
	}
	if iv.End.After(to) {
		iv.End = to
	}
	return iv
}

// merge sorts intervals and merges overlapping or adjacent ones.
func merge(ivs []Interval) []Interval {
	if len(ivs) == 0 {
		return nil
	}
	slices.SortFunc(ivs, func(a, b Interval) int { return a.Start.Compare(b.Start) })
	out := []Interval{ivs[0]}
	for _, iv := range ivs[1:] {
		last := &out[len(out)-1]
		if !iv.Start.After(last.End) {
			if iv.End.After(last.End) {
				last.End = iv.End
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

// IntersectIntervals intersects two sorted, non-overlapping interval lists.
func IntersectIntervals(a, b []Interval) []Interval {
	var out []Interval
	for i, j := 0, 0; i < len(a) && j < len(b); {
		start := a[i].Start
		if b[j].Start.After(start) {
			start = b[j].Start
		}
		end := a[i].End
		if b[j].End.Before(end) {
			end = b[j].End
		}
		if start.Before(end) {
			out = append(out, Interval{start, end})
		}
		if a[i].End.Before(b[j].End) {
			i++
		} else {
			j++
		}
	}
	return out
}
