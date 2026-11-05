# ADR 0001 — Backend stack: Go + PostgreSQL, modular monolith

## Status
Accepted — 2026-11-05

## Context

`architecture.md` leaves the backend language and framework open. Implementation
work (policy engine, identity, relationships, messaging transport, signaling)
needs a concrete stack. Requirements:

- server-side enforcement of security-relevant policy;
- no possession of content-decryption keys;
- WebRTC signaling and, later, an SFU able to forward E2EE media;
- simple, auditable, reproducible deployment for an open project;
- established cryptographic libraries (signature verification, random tokens).

## Decision

- **Language:** Go. Single static binary, strong standard library
  (`crypto/*`, `net/http`), mature WebRTC ecosystem (Pion; LiveKit is Go-based),
  easy to read for external reviewers.
- **Database:** PostgreSQL for relational metadata and opaque ciphertext envelopes.
- **Shape:** a modular monolith under `server/`, one internal package per
  logical service (`identity`, `relationship`, `policy`, `retention`,
  `messaging`, `signaling`). Packages communicate through Go interfaces so they
  can be split into separate deployables later if justified.
- **Pure domain logic first:** policy intersection and retention planning are
  implemented as pure, deterministic functions with no I/O, so they can be
  tested exhaustively and reused by every enforcement point.

## Still open (not decided by this ADR)

- messaging E2EE protocol (see the dedicated research issue);
- SFU implementation and WebRTC E2EE mechanism;
- encrypted recording format;
- push notification strategy;
- key recovery design.

## Consequences

- Contributors need Go ≥ 1.24.
- The Flutter client and the server share no code; interoperability is defined
  by specifications in `protocol/`.
- Any future split into microservices must keep enforcement in the backend.
