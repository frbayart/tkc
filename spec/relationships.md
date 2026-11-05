# Contacts and Relationships

## Principle

A child-to-child contact is an explicitly authorized relationship. There is no open contact-request mechanism for children.

## Relationship establishment paths

### In-person child exchange

Two children who are physically together may initiate a contact exchange using an ephemeral mechanism such as:

- QR code;
- NFC;
- Bluetooth-based proximity exchange.

Example:

```text
Child A                Child B
   │                       │
   └──── ephemeral QR ────►│
                           │
                 Pending relationship
                           │
             ┌─────────────┴─────────────┐
             ▼                           ▼
        Parent A                     Parent B
          approve                      approve
             └─────────────┬─────────────┘
                           ▼
                  Active relationship
```

The exchange token should be short-lived and single-use.

### Parent-to-parent connection

Parents may need to establish trust without meeting physically.

A proposed mechanism is an out-of-band, deliberate invitation:

1. Parent B generates a temporary invitation.
2. The invitation is sent through an existing external channel such as SMS, email or another messenger.
3. Parent A opens or enters the invitation.
4. Parent B receives a confirmation request.
5. Both sides confirm the family connection.

The invitation should be:

- high entropy;
- time limited;
- single use;
- revocable;
- unusable to enumerate family or child identities.

### Parent-created child relationship

Once two families have established sufficient trust, parents may propose a relationship between specific children.

Both families can be required to approve it before activation.

## Relationship lifecycle

Suggested states:

```text
proposed
   ↓
pending_family_a
   ↓
pending_family_b
   ↓
active
   ↓
suspended
   ↓
revoked
```

The exact workflow may allow both approvals to occur in either order.

## Revocation

A relationship can be blocked or revoked.

Revocation must prevent future communication immediately at the authorization layer.

The cryptographic consequences of revocation — especially access to historical content — must be explicitly defined in the key-management specification.
