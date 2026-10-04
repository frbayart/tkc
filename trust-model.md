# Trust Model

## Status
Draft — v0.1

## Objective

The platform creates a closed communication environment in which children communicate only with explicitly trusted contacts under rules controlled by their parents.

The service provider is trusted to operate the infrastructure and enforce access policy, but is **not trusted with communication plaintext**.

## Actors

### Child

A child can communicate only through relationships authorized for that child.

Depending on parental policy, a child may be able to:

- exchange messages;
- exchange attachments;
- make or receive audio calls;
- make or receive video calls;
- propose an in-person contact;
- block an existing contact.

### Parent / Guardian

A parent manages one or more children and may:

- approve or reject contacts;
- establish trusted relationships with other families;
- add a contact for a child;
- define communication capabilities and limits;
- review messages involving their child;
- review retained call recordings involving their child;
- revoke contacts;
- revoke devices;
- participate in key recovery procedures.

A parent is a cryptographic participant, not merely a privileged server-side user.

### Provider

The provider operates:

- authentication and identity services;
- relationship and policy services;
- message transport;
- signaling;
- WebRTC infrastructure;
- encrypted storage;
- metadata storage;
- push-notification integration.

The provider must not possess sufficient key material to decrypt messages or retained media.

### External authority

The provider cannot independently provide communication plaintext that it cannot decrypt.

Where legally required and applicable, access to decrypted content must involve a parent or another legitimate holder of the required cryptographic keys.

This is an architectural objective, not a statement about legal obligations in any particular jurisdiction.

## Trust boundaries

```text
                  PROVIDER INFRASTRUCTURE
       ┌───────────────────────────────────────┐
       │ Identity / Policy / Signaling / SFU  │
       │ Metadata / Ciphertext / Object Store │
       │                                       │
       │           NO CONTENT KEYS             │
       └──────────────────┬────────────────────┘
                          │ encrypted data
            ┌─────────────┴─────────────┐
            │                           │
        FAMILY A                    FAMILY B
     Parent devices              Parent devices
     Child devices               Child devices
          │                           │
          └──── authorized relationship ────┘
```

## Fundamental invariants

### No public child discovery

A child identity must not be searchable through a public directory by:

- display name;
- username;
- email address;
- telephone number;
- internal UUID.

Knowledge of an internal identifier must not be sufficient to create a contact request.

### Explicit relationship establishment

A relationship requires an approved trust path. Examples include:

- in-person child-to-child exchange followed by parental approval;
- parent-to-parent trust established through a deliberate invitation;
- direct creation by parents after both families have established trust.

### Least privilege

A relationship grants only explicitly allowed capabilities. Friendship is not equivalent to unrestricted communication.

### Parent visibility without provider visibility

Parents may inspect communication content involving their children while the provider remains cryptographically unable to do so.
