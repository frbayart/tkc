# ADR 0002 — Encrypted profiles and one conversation per relationship

## Status
Accepted — 2026-11-05

## Context

The data model (`docs/data-model.md`) had to decide how user profiles and conversations are represented. Both choices affect what the provider can see.

## Decisions

1. **Profiles are encrypted client-side.** Display names and avatars are stored only as `users.profile_ciphertext`, encrypted by the application for the members of the family and its authorized relationships. The server never knows a name.
2. **One conversation per relationship.** A child-to-child conversation is identified by its relationship (`messages.relationship_id`). There is no separate `conversations` table until group conversations are designed.

## Amendment — 2026-11-06

Members of the same family communicate by default without any relationship (spec/identity-family.md). "One conversation per relationship" therefore covers conversations between children of different families only; the messaging design (#11) must also provide family conversations, identified by the family rather than by a relationship.

## Amendment 2 — 2026-11-06

Profiles are not updated in place: every change creates a new immutable, signed, encrypted profile version linked to the previous one, and messages record the sender's profile version, so that evidence shows the name in use at the time (spec/evidence.md). The server still never sees a name.

## Consequences

- Server-side features cannot search, sort or display names; all name rendering happens on devices.
- Support and abuse handling cannot rely on names stored by the provider.
- Introducing groups will require a `conversations` table and a migration of `messages`; this is accepted as future work.
