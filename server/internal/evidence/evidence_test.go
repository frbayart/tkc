package evidence

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// chain builds a valid chain of n events for a subject.
func chain(t *testing.T, subject string, n int) []Event {
	t.Helper()
	var out []Event
	var prev []byte
	at := time.Date(2026, 11, 6, 10, 0, 0, 123456000, time.UTC)
	for i := 1; i <= n; i++ {
		user := "00000000-0000-4000-8000-000000000001"
		e := Event{
			SubjectType: SubjectRelationship, SubjectID: subject, Seq: int64(i),
			OccurredAt: at.Add(time.Duration(i) * time.Minute), Type: "test_event",
			ActorUserID: &user, Data: json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`), PrevHash: prev,
		}
		h, err := computeHash(e)
		if err != nil {
			t.Fatal(err)
		}
		e.Hash = h
		prev = h
		out = append(out, e)
	}
	return out
}

func TestVerifyValidChains(t *testing.T) {
	a := chain(t, "00000000-0000-4000-8000-00000000000a", 4)
	b := chain(t, "00000000-0000-4000-8000-00000000000b", 2)
	if err := Verify(append(append([]Event{}, a...), b...)); err != nil {
		t.Fatal(err)
	}
	if err := Verify(nil); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	subject := "00000000-0000-4000-8000-00000000000a"
	cases := map[string]func([]Event) []Event{
		"altered data": func(es []Event) []Event {
			es[1].Data = json.RawMessage(`{"i":9}`)
			return es
		},
		"altered type": func(es []Event) []Event {
			es[2].Type = "other"
			return es
		},
		"altered actor": func(es []Event) []Event {
			other := "00000000-0000-4000-8000-000000000002"
			es[0].ActorUserID = &other
			return es
		},
		"altered time": func(es []Event) []Event {
			es[3].OccurredAt = es[3].OccurredAt.Add(time.Second)
			return es
		},
		"removed event": func(es []Event) []Event { return append(es[:1], es[2:]...) },
		"removed first": func(es []Event) []Event { return es[1:] },
		"reordered":     func(es []Event) []Event { es[1], es[2] = es[2], es[1]; return es },
		"rehashed alone": func(es []Event) []Event {
			// Recomputing one event's hash after altering it still breaks the
			// link with the next event.
			es[1].Data = json.RawMessage(`{"i":9}`)
			es[1].Hash, _ = computeHash(es[1])
			return es
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Verify(tamper(chain(t, subject, 4))); !errors.Is(err, ErrBrokenChain) {
				t.Fatalf("Verify = %v, want ErrBrokenChain", err)
			}
		})
	}
}
