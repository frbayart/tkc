# Messaging and Cryptography

## Security objective

Messages and attachments must be readable by:

- the participating children as authorized;
- authorized parent/guardian devices for those children.

They must not be readable by the service provider solely from server-side data.

## Conceptual model

```text
Conversation Alice ↔ Bob

Conversation key material
       │
       ├── accessible to Alice authorized device(s)
       ├── accessible to Bob authorized device(s)
       ├── accessible to Alice authorized parent device(s)
       └── accessible to Bob authorized parent device(s)

Provider:
       └── ciphertext only
```

## Parent access

Parental supervision is part of the cryptographic model.

The parent is therefore not a server administrator bypassing encryption. The parent's authorized device receives legitimate cryptographic access to conversations involving the supervised child.

## Server-side representation

A stored message might conceptually contain:

```text
message_id
conversation_id
sender_id
sender_device_id
timestamp
ciphertext
cryptographic metadata
```

The exact metadata must be minimized because encryption does not hide metadata automatically.

## Key management requirements

The final protocol must specify:

- device identity keys;
- session/conversation keys;
- parent access;
- key rotation;
- new-device enrollment;
- device revocation;
- relationship revocation;
- forward secrecy expectations;
- recovery mechanisms;
- historical-message behavior after device enrollment;
- backup strategy.

## New parent device

A newly authenticated device must not automatically receive historical decryption capability merely because the user knows an account password.

Historical access should require an explicit cryptographic authorization or recovery operation.

## Provider compromise

A database/object-storage compromise should reveal ciphertext and unavoidable metadata, but not message plaintext.

A compromised provider must also be considered capable of attempting active attacks. The final protocol therefore needs mechanisms for clients to detect or prevent unauthorized key/device insertion.

## Legal access

The provider is intentionally not a decryption-key escrow service.

If communication plaintext must be provided under a valid legal process, the technical architecture anticipates that an authorized key holder, such as a parent, may need to participate.

Actual legal obligations depend on jurisdiction and require specialist legal review.

## Cryptographic caution

No custom cryptographic primitive should be invented for this project.

The final design should use established, independently reviewed protocols and libraries. The exact messaging protocol remains an open design decision in v0.1.
