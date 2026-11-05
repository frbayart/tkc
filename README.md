# Trusted Kids Communications

## Status
Draft specification — v0.1 — 2026-10-04

## Purpose

Trusted Kids Communications is an open, privacy-first communication platform for children and their families.

The application provides:

- private text messaging;
- audio calls;
- video calls;
- parent-controlled contact relationships;
- per-relationship communication rules;
- parental access to their children's communications;
- encrypted short-term retention of call recordings;
- no provider access to communication content.

The project is intended for Android and iOS phones and tablets. The reference client is planned in Flutter.

## Core principles

1. **Children are not publicly discoverable.**
2. **A child cannot receive an unsolicited contact request from an unknown person.**
3. **Parents control which relationships their children may establish.**
4. **Every child-to-child relationship has an explicit communication policy.**
5. **The effective policy is the intersection of the policies imposed by both families.**
6. **Messages, attachments and retained media are end-to-end encrypted.**
7. **The service provider has no key capable of decrypting communication content.**
8. **Authorized parents are cryptographic participants and can inspect their child's communications.**
9. **Important restrictions are enforced by the service, not merely by the client UI.**
10. **The architecture, protocols, threat model and cryptographic design are publicly documented.**

## Documents

- [Architecture](docs/architecture.md)
- [Trust Model](docs/trust-model.md)
- [Threat Model](docs/threat-model.md)
- [Data Model](docs/data-model.md)
- [Identity and Families](spec/identity-family.md)
- [Contacts and Relationships](spec/relationships.md)
- [Relationship Policies](spec/relationship-policies.md)
- [Messaging and Cryptography](spec/messaging-cryptography.md)
- [Calls and Recording](spec/calls-recording.md)
- [Retention](spec/retention.md)
- [Open Project Principles](docs/open-project.md)
- [Architecture Decision Records](docs/adr/)

## Repository layout

```text
docs/        architecture, trust/threat models, ADRs
spec/        normative product and protocol specifications
server/      Go backend (modular monolith)
apps/flutter reference mobile client (planned)
protocol/    wire/cryptographic protocol definitions (planned)
security/    security reviews and reports (planned)
```

## Development

```bash
cd server
make test
```

Database integration tests run when `TKC_TEST_DATABASE_URL` points to a PostgreSQL ≥ 13 database, for example:

```bash
TKC_TEST_DATABASE_URL=postgres://tkc:tkc@localhost:5432/tkc_test?sslmode=disable make test
```

## Important status note

This documentation describes the intended security model. Cryptographic mechanisms, key management, WebRTC E2EE and recording must receive dedicated security review before production use.
