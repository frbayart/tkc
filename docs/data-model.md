# Data Model

## Status
Draft — v0.1 — schema in `server/internal/store/migrations/`

## Principles

- **Random identifiers.** Every primary key is a random UUID. No username, phone number or e-mail is stored, and nothing is publicly enumerable.
- **No secret material.** Devices store public keys only. Private keys never leave the device.
- **No plaintext content.** Messages are opaque ciphertext envelopes; content type and attachment references are inside the ciphertext. User profiles (display names, avatars) are encrypted client-side.
- **Database-enforced invariants.** Security-relevant rules are also enforced by constraints, so a bug in the application layer cannot create an invalid state.

## Entity-relationship diagram

```mermaid
erDiagram
    families ||--o{ users : "has members"
    users ||--o{ devices : owns
    devices |o--o{ devices : "approved by"
    families ||--o{ family_connections : "family_a / family_b"
    families ||--o{ invitations : issues
    users |o--o{ invitations : "child (in-person exchange)"
    users ||--o{ relationships : "child_a / child_b"
    relationships ||--|{ relationship_policies : "one per family"
    families ||--o{ relationship_policies : defines
    relationships ||--o{ messages : carries
    devices ||--o{ messages : sends
    relationships ||--o{ calls : has
    calls ||--o| recordings : "may produce"

    families {
        uuid id PK
        timestamptz created_at
    }
    users {
        uuid id PK
        uuid family_id FK
        text role "parent | child"
        text time_zone "IANA"
        bytea profile_ciphertext "client-side encrypted"
        text status
    }
    devices {
        uuid id PK
        uuid user_id FK
        text device_type "phone | tablet"
        text platform "android | ios"
        bytea identity_public_key
        bytea encryption_public_key
        int key_version
        text status "pending | active | revoked"
        uuid approved_by_device_id FK
    }
    family_connections {
        uuid id PK
        uuid family_a FK "family_a < family_b"
        uuid family_b FK
        text status "pending | active | revoked"
    }
    invitations {
        uuid id PK
        text kind "family_connection | child_exchange"
        uuid issuer_family_id FK
        uuid child_id FK
        bytea token_hash "hash only"
        timestamptz expires_at
        timestamptz consumed_at
        timestamptz revoked_at
    }
    relationships {
        uuid id PK
        uuid child_a FK "child_a < child_b"
        uuid child_b FK
        text status "proposed | pending | active | suspended | revoked"
        text origin "in_person | parent"
        timestamptz approved_a_at
        timestamptz approved_b_at
    }
    relationship_policies {
        uuid relationship_id PK
        uuid family_id PK
        jsonb policy "internal/policy FamilyPolicy"
        int version
    }
    messages {
        uuid id PK
        uuid relationship_id FK
        uuid sender_device_id FK
        bytea ciphertext "opaque E2EE envelope"
        bytea crypto_metadata
    }
    calls {
        uuid id PK
        uuid relationship_id FK
        uuid caller_user_id FK
        text call_type "audio | video"
        timestamptz authorized_until
        text termination_reason
    }
    recordings {
        uuid id PK
        uuid call_id FK
        text object_key "encrypted object"
        timestamptz expires_at "from internal/retention"
        text deletion_reason
    }
```

## Invariants enforced by the database

| Table | Invariant |
|---|---|
| `users` | role is `parent` or `child` |
| `devices` | `revoked` ⇔ `revoked_at` set; identity keys are unique |
| `family_connections` | one row per pair of families, canonical order `family_a < family_b` |
| `invitations` | only the token hash is stored; `child_exchange` ⇔ a child **of the issuer family**; cannot be both consumed and revoked; finite expiry |
| `relationships` | each child belongs to the stated family; canonical order `child_a < child_b`; `active`/`suspended` require both parental approvals; at most one non-revoked relationship per pair |
| `calls` | `authorized_until` (from the policy engine) is after the start; `ended_at` ⇔ `termination_reason` |
| `recordings` | one per call; `deleted_at` ⇔ `deletion_reason` |

## Metadata visible to the provider

Encryption does not hide metadata. With this schema, the provider can see:

- family membership (who is a parent or child of which family), and device counts and platforms;
- the relationship graph between children and between families;
- message timing, size and sender device per relationship;
- call timing, type, duration and termination reason;
- recording sizes and lifecycle dates.

The provider **cannot** see names, profile pictures, message content, attachment content or media. Reducing the metadata above (e.g. sealed sender, padding) is tracked as future work in the threat model.

## Migrations

Migrations are plain SQL files embedded in the server binary (`NNNN_description.sql`). `store.Migrate` applies the pending ones, each in its own transaction, under a PostgreSQL advisory lock, so several instances can start at the same time.

Integration tests run when `TKC_TEST_DATABASE_URL` is set; each test uses a throw-away schema.
