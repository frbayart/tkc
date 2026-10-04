# Technical Architecture

## Status
Candidate architecture — v0.1

## Client

The reference application is planned in **Flutter** and targets:

- Android phones;
- Android tablets;
- iPhone;
- iPad.

The client is responsible for:

- user experience;
- local cryptographic operations;
- secure local storage;
- message encryption/decryption;
- WebRTC media handling;
- parental review UI;
- contact-exchange UI;
- local policy presentation.

## Candidate backend decomposition

```text
Flutter clients
      │
      ├──────── HTTPS/API ────────┐
      │                           ▼
      │                     API / Identity
      │                           │
      │                     Relationship
      │                           │
      │                        Policy
      │
      ├──────── Messaging ───────► Message transport
      │                              │
      │                              ▼
      │                           PostgreSQL
      │                         (ciphertext + metadata)
      │
      └──────── WebRTC ─────────► Signaling
                                     │
                                     ▼
                                    SFU
                                     │
                                     ▼
                             encrypted recordings
                                     │
                                     ▼
                                Object Storage
```

Redis or another ephemeral data service may be used for presence, signaling or transient coordination where justified.

## Logical services

### Identity

Manages users, families, devices and authentication without becoming a holder of content-decryption keys.

### Relationship service

Manages:

- family trust;
- child relationships;
- invitations;
- approvals;
- suspension/revocation.

### Policy service

Stores each family's policy and computes the effective intersection.

### Messaging service

Transports and persists encrypted envelopes.

### Signaling service

Authorizes call establishment according to relationship policy and coordinates WebRTC sessions.

### Media/SFU

Routes audio/video without gaining plaintext access under the intended E2EE design.

### Encrypted recording storage

Stores opaque recording objects and associated lifecycle metadata.

## Enforcement boundary

The mobile application is not considered a sufficient security enforcement point.

The backend must independently enforce:

- relationship existence;
- relationship status;
- communication type;
- calling windows;
- maximum call duration;
- authorization to retrieve encrypted content;
- recording retention.

## Technology choices not yet fixed

The following should remain open until prototypes/security analysis are complete:

- backend language/framework;
- database details beyond relational requirements;
- messaging E2EE protocol;
- WebRTC E2EE implementation;
- SFU implementation;
- encrypted recording format;
- push notification strategy;
- key recovery design.

Avoid locking the public specification to an implementation detail unless interoperability requires it.
