# Retention Policy

## Goals

Retention must balance:

- parental ability to investigate recent concerns;
- child privacy;
- storage cost;
- exposure in the event of compromise;
- regulatory obligations.

## Call history vs call content

Call metadata and recorded content are distinct.

Example history:

```text
2026-10-03 16:42  Bob    Video  12m34s
2026-10-03 15:21  Alice  Audio   3m12s
2026-10-02 18:03  Bob    Video  42m07s
```

The metadata may remain visible after the encrypted recording has been deleted.

## Candidate recording policy

The initial product idea is:

- retain recent calls for approximately seven days;
- optionally limit this to the most recent five calls;
- retain the two most recent calls for a longer period.

This should be expressed as deterministic policy rather than informal cleanup logic.

Example candidate:

```yaml
recording_retention:
  recent:
    duration_days: 7
    maximum_count: 5

  extended:
    keep_latest_count: 2
    maximum_age_days: 90
```

The values above are examples, not final defaults.

## Absolute expiration

Keeping the latest two recordings forever is discouraged. A finite maximum age should be considered so that old sensitive media is eventually destroyed.

## Deletion

Deletion semantics must cover:

- primary object storage;
- replicas;
- caches;
- database references;
- backups;
- encryption-key destruction.

Cryptographic erasure may complement physical object deletion when appropriate.

## Parent visibility

Parents should be able to understand:

- what is currently retained;
- when each recording will expire;
- why a recording is retained;
- whether only metadata will remain afterward.

## Open questions

Final retention defaults require both product and legal review, particularly because the retained content concerns minors and may contain highly private domestic audio/video.

## Reference implementation notes

`server/internal/retention` implements retention as a pure function `Compute(recordings, policy, now)`. Recordings are ranked from the most recent (ties broken by ID):

- **recent:** kept while younger than `retention_days` and, if `keep_recent_count > 0`, ranked below it;
- **long term:** kept while ranked below `keep_long_term_count` and younger than `long_term_max_age_days`;
- otherwise deleted, with reason `count_exceeded` (still young enough but outside the count) or `expired`;
- if recording is disabled by the effective policy, all recordings are deleted (`recording_disabled`).

Each kept recording carries its reason and an `expires_at` (deletion at the latest), shown to parents. A zero age limit keeps nothing under that rule, so retention is always finite. Deletion is final: a recording deleted at one instant is never kept at a later instant under the same policy.
