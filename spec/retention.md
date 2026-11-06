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

## Evidence retention

Communication involving minors may later be needed as **evidence**: legal proceedings, harassment, or psychological follow-up. Evidence matters to **both families** of a relationship, not only to the family that ends it.

How evidence is structured — no anonymization, versioned profiles, append-only event log — is specified in [evidence.md](evidence.md).

Rules implemented so far:

- **Ending a relationship never deletes it.** Revoking, blocking, or revoking the family connection keeps the relationship, its history and its (encrypted) messages; both families' parents can still see it, with who revoked it and why.
- **Removing a member does not delete data immediately.** The account is retired at once, but its data is kept until `purge_after = removed_at + evidence retention period`, then permanently deleted with everything attached to it. A background job runs the purge every hour.
- The evidence retention period is configurable (`tkc-server -evidence-retention`, default **365 days**). **This default is provisional and requires legal review**, possibly per jurisdiction.

The provider only keeps ciphertext: evidence is readable only by the parents' devices that hold the keys, which is consistent with the legal-access model in [messaging-cryptography.md](messaging-cryptography.md).

Open points:

- **Preservation hold.** A parent of either family can place a hold on a relationship, including an ended one, while a concern is being investigated. While a hold is active:
  - **no call recording of the relationship is deleted**, whatever the recording policy (reason `preservation_hold`, see `retention.ComputeWithHold`);
  - the **purge of a removed member** involved in the relationship is deferred.

  Each family manages its own hold and only it can release it. **The other family is never informed**: each family's privacy matters, and a family that places a hold because of a doubt must not have to alert the other one. In a real dispute, lawyers decide. A hold lasts **365 days by default and can be renewed**, so that none lasts forever by oversight (provisional value, pending legal review). When it ends, normal retention applies again.
- **Message retention.** Messages are kept as long as their relationship exists (and therefore during the evidence retention period of a removed member). A technical maximum is likely to be introduced later; it must not apply while a preservation hold is active.
- **Deletion requests do not shorten evidence retention.** A family can delete its data from its own view and devices (and remove a member), but this never purges evidence earlier. **Even if both families agree**, evidence is kept until the end of the evidence retention period, to protect the child from the families' decisions. No API allows an early purge; only the scheduled purge deletes evidence, at the end of the period.
- How this interacts with statutory erasure rights or a court order still needs legal review, possibly per jurisdiction.

## Open questions

Final retention defaults require both product and legal review, particularly because the retained content concerns minors and may contain highly private domestic audio/video.

## Reference implementation notes

`server/internal/retention` implements retention as a pure function `Compute(recordings, policy, now)`. Recordings are ranked from the most recent (ties broken by ID):

- **recent:** kept while younger than `retention_days` and, if `keep_recent_count > 0`, ranked below it;
- **long term:** kept while ranked below `keep_long_term_count` and younger than `long_term_max_age_days`;
- otherwise deleted, with reason `count_exceeded` (still young enough but outside the count) or `expired`;
- if recording is disabled by the effective policy, all recordings are deleted (`recording_disabled`).

Each kept recording carries its reason and an `expires_at` (deletion at the latest), shown to parents. A zero age limit keeps nothing under that rule, so retention is always finite. Deletion is final: a recording deleted at one instant is never kept at a later instant under the same policy.
