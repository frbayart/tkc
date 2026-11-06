// Package evidence implements the append-only, hash-chained event log
// described in spec/evidence.md.
//
// Events are chained per subject (a user, a relationship, a family
// connection, a proposal). Each event's hash covers the previous event's hash
// and a canonical encoding of the event, so removing, reordering or altering
// an event is detectable. Events are written in the same transaction as the
// change they record. The database rejects updates, and deletes while the
// subject still exists.
package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Subject types.
const (
	SubjectUser         = "user"
	SubjectRelationship = "relationship"
	SubjectConnection   = "connection"
	SubjectProposal     = "proposal"
)

// Actor is who caused an event; empty for system events.
type Actor struct {
	UserID   string
	DeviceID string
}

// Entry is an event to append.
type Entry struct {
	SubjectType string
	SubjectID   string
	Type        string
	Data        map[string]any
}

// Event is a stored event.
type Event struct {
	ID             string          `json:"id"`
	SubjectType    string          `json:"subject_type"`
	SubjectID      string          `json:"subject_id"`
	Seq            int64           `json:"seq"`
	OccurredAt     time.Time       `json:"occurred_at"`
	Type           string          `json:"event_type"`
	ActorUserID    *string         `json:"actor_user_id"`
	ActorDeviceID  *string         `json:"actor_device_id"`
	ActorProfileID *string         `json:"actor_profile_id"`
	Data           json.RawMessage `json:"data"`
	PrevHash       []byte          `json:"prev_hash"`
	Hash           []byte          `json:"hash"`
}

// hashInput is the canonical encoding of an event, without its hash.
// Field order is fixed by the struct; map keys in Data are sorted by
// encoding/json.
type hashInput struct {
	SubjectType    string          `json:"subject_type"`
	SubjectID      string          `json:"subject_id"`
	Seq            int64           `json:"seq"`
	OccurredAt     string          `json:"occurred_at"`
	Type           string          `json:"event_type"`
	ActorUserID    *string         `json:"actor_user_id"`
	ActorDeviceID  *string         `json:"actor_device_id"`
	ActorProfileID *string         `json:"actor_profile_id"`
	Data           json.RawMessage `json:"data"`
}

// computeHash returns SHA-256(prev_hash ‖ canonical event).
func computeHash(e Event) ([]byte, error) {
	in, err := json.Marshal(hashInput{
		SubjectType: e.SubjectType, SubjectID: e.SubjectID, Seq: e.Seq,
		OccurredAt: e.OccurredAt.UTC().Format(time.RFC3339Nano), Type: e.Type,
		ActorUserID: e.ActorUserID, ActorDeviceID: e.ActorDeviceID, ActorProfileID: e.ActorProfileID,
		Data: e.Data,
	})
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(e.PrevHash)
	h.Write(in)
	return h.Sum(nil), nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Append records events in tx at time now. Events on the same subject are
// chained in the given order. Subjects are locked in a fixed order, so
// concurrent appends cannot deadlock or fork a chain.
func Append(ctx context.Context, tx pgx.Tx, now time.Time, actor Actor, entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	// PostgreSQL stores microseconds: hash exactly what is stored.
	now = now.Truncate(time.Microsecond)

	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.SubjectType+":"+e.SubjectID)
	}
	slices.Sort(keys)
	for _, k := range slices.Compact(keys) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "evidence:"+k); err != nil {
			return err
		}
	}

	var profileID *string
	if actor.UserID != "" {
		if err := tx.QueryRow(ctx, `SELECT current_profile_id FROM users WHERE id = $1`, actor.UserID).
			Scan(&profileID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}

	for _, en := range entries {
		data := en.Data
		if data == nil {
			data = map[string]any{}
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		e := Event{
			SubjectType: en.SubjectType, SubjectID: en.SubjectID, OccurredAt: now, Type: en.Type,
			ActorUserID: optional(actor.UserID), ActorDeviceID: optional(actor.DeviceID), ActorProfileID: profileID,
			Data: raw,
		}
		err = tx.QueryRow(ctx, `SELECT seq, hash FROM evidence_events WHERE subject_type = $1 AND subject_id = $2
			ORDER BY seq DESC LIMIT 1`, e.SubjectType, e.SubjectID).Scan(&e.Seq, &e.PrevHash)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		e.Seq++
		if e.Hash, err = computeHash(e); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO evidence_events
			(subject_type, subject_id, seq, occurred_at, event_type, actor_user_id, actor_device_id,
			 actor_profile_id, data, prev_hash, hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			e.SubjectType, e.SubjectID, e.Seq, e.OccurredAt, e.Type, e.ActorUserID, e.ActorDeviceID,
			e.ActorProfileID, string(e.Data), e.PrevHash, e.Hash); err != nil {
			return err
		}
	}
	return nil
}

// Querier is satisfied by a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Timeline returns the events of the given subjects, chain by chain, each
// chain in order. Callers authorize access to the subjects.
func Timeline(ctx context.Context, q Querier, subjectType string, subjectIDs ...string) ([]Event, error) {
	if len(subjectIDs) == 0 {
		return []Event{}, nil
	}
	rows, err := q.Query(ctx, `SELECT id, subject_type, subject_id, seq, occurred_at, event_type,
			actor_user_id, actor_device_id, actor_profile_id, data, prev_hash, hash
		FROM evidence_events WHERE subject_type = $1 AND subject_id = ANY($2::uuid[])
		ORDER BY subject_id, seq`, subjectType, subjectIDs)
	if err != nil {
		return nil, err
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		var data string
		err := r.Scan(&e.ID, &e.SubjectType, &e.SubjectID, &e.Seq, &e.OccurredAt, &e.Type,
			&e.ActorUserID, &e.ActorDeviceID, &e.ActorProfileID, &data, &e.PrevHash, &e.Hash)
		e.Data = json.RawMessage(data)
		return e, err
	})
	if events == nil {
		events = []Event{}
	}
	return events, err
}

// ErrBrokenChain reports a gap, a reordering or an alteration in a chain.
var ErrBrokenChain = errors.New("evidence: broken chain")

// Verify checks the hash chains of events as returned by Timeline.
func Verify(events []Event) error {
	var prev *Event
	for i := range events {
		e := events[i]
		if prev == nil || prev.SubjectType != e.SubjectType || prev.SubjectID != e.SubjectID {
			if e.Seq != 1 || e.PrevHash != nil {
				return fmt.Errorf("%w: %s %s does not start at 1", ErrBrokenChain, e.SubjectType, e.SubjectID)
			}
		} else if e.Seq != prev.Seq+1 || !bytes.Equal(e.PrevHash, prev.Hash) {
			return fmt.Errorf("%w: %s %s at seq %d", ErrBrokenChain, e.SubjectType, e.SubjectID, e.Seq)
		}
		h, err := computeHash(e)
		if err != nil {
			return err
		}
		if !bytes.Equal(h, e.Hash) {
			return fmt.Errorf("%w: %s %s seq %d altered", ErrBrokenChain, e.SubjectType, e.SubjectID, e.Seq)
		}
		prev = &events[i]
	}
	return nil
}

// PurgeOrphans deletes the chains whose subject no longer exists. It is run
// by the evidence purge, after the subjects themselves were purged.
func PurgeOrphans(ctx context.Context, tx pgx.Tx) (int64, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM evidence_events e WHERE
		(e.subject_type = 'user' AND NOT EXISTS (SELECT 1 FROM users WHERE id = e.subject_id))
		OR (e.subject_type = 'relationship' AND NOT EXISTS (SELECT 1 FROM relationships WHERE id = e.subject_id))
		OR (e.subject_type = 'connection' AND NOT EXISTS (SELECT 1 FROM family_connections WHERE id = e.subject_id))
		OR (e.subject_type = 'proposal' AND NOT EXISTS (SELECT 1 FROM relationship_proposals WHERE id = e.subject_id))`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// TimelineView is a timeline with its verification result.
type TimelineView struct {
	Events     []Event `json:"events"`
	ChainValid bool    `json:"chain_valid"`
}

// View loads and verifies a timeline.
func View(ctx context.Context, q Querier, subjectType string, subjectIDs ...string) (TimelineView, error) {
	events, err := Timeline(ctx, q, subjectType, subjectIDs...)
	if err != nil {
		return TimelineView{}, err
	}
	return TimelineView{Events: events, ChainValid: Verify(events) == nil}, nil
}
