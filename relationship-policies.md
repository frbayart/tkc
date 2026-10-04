# Relationship Policies

## Principle

A child relationship is a set of capabilities and restrictions, not a Boolean `friend` flag.

Each family may define its own policy for the relationship.

The effective policy is the **intersection of both families' policies**: the most restrictive applicable rule wins.

## Example

```yaml
relationship:
  child_a: alice
  child_b: bob

  messaging:
    enabled: true
    attachments: false

  audio:
    enabled: true
    allowed_from: "08:00"
    allowed_until: "20:00"
    max_duration_minutes: 30

  video:
    enabled: false

  recording:
    enabled: true
    retention_days: 7
    keep_recent_count: 5
    keep_long_term_count: 2
```

This is illustrative rather than a final schema.

## Policy intersection

Suppose Family A defines:

```text
Audio: yes
Video: yes
Calls: 08:00–20:00
Maximum call: 30 minutes
```

and Family B defines:

```text
Audio: yes
Video: no
Calls: 09:00–19:00
Maximum call: 20 minutes
```

The effective policy is:

```text
Audio: yes
Video: no
Calls: 09:00–19:00
Maximum call: 20 minutes
```

Neither family can use its policy to grant a permission denied by the other family.

## Candidate capabilities

Policies may eventually control:

- text messaging;
- voice messages;
- images;
- file attachments;
- audio calls;
- video calls;
- allowed calling hours;
- maximum individual call duration;
- daily call-duration allowance;
- allowed days of week;
- whether incoming calls require acceptance;
- group participation;
- recording retention parameters.

## Server enforcement

Security-relevant policy must be enforced by backend services.

For example, Flutter may disable the Video Call button, but the signaling service must independently reject a video session if video is forbidden.

For call duration, the infrastructure must terminate or stop authorizing the call when the effective maximum duration is reached.

## Time zones

Time restrictions must have explicit time-zone semantics.

A recommended model is to evaluate each family's restriction in that child's configured home time zone, then intersect the resulting allowed periods.

The final behavior must be clearly visible to parents to avoid ambiguous rules during travel or daylight-saving changes.
