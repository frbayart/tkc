# Threat Model

## Status
Initial threat model — incomplete by design.

## Assets

High-value assets include:

- message plaintext;
- attachments;
- audio/video media;
- retained recordings;
- cryptographic keys;
- child identity information;
- relationship graph;
- call metadata;
- parental-control policy;
- device authorization state.

## Adversaries

### Unknown external adult or child

Goal: discover or contact children without authorization.

Controls:

- no public child directory;
- no arbitrary contact requests;
- high-entropy temporary invitations;
- parental approval;
- server-side relationship authorization.

### Malicious or compromised provider infrastructure

Goal: read communications or inject unauthorized devices.

Controls/objectives:

- E2EE;
- provider holds no content keys;
- client-verifiable device authorization;
- auditable key changes;
- minimal metadata;
- encrypted object storage content.

### Compromised child device

Goal: access conversations or impersonate the child.

Controls:

- device-specific keys;
- revocation;
- limited local storage;
- secure platform key storage;
- optional local authentication controls.

### Compromised parent device

This is particularly severe because parent devices intentionally have broad decryption rights for supervised children.

Controls must include:

- strong device authentication;
- hardware/platform-backed key storage where available;
- remote device revocation;
- explicit new-device enrollment;
- security notifications/audit trail.

### Malicious authorized contact

An authorized child/contact may behave abusively despite legitimate enrollment.

Controls include:

- immediate blocking;
- parent revocation;
- relationship-level capabilities;
- recent supervised content where enabled;
- clear reporting/escalation UX.

### Enumeration attacker

Goal: infer which children/families use the service.

Controls:

- non-enumerable identifiers;
- rate limits;
- invitation tokens that disclose minimal information before acceptance;
- careful account-recovery responses.

## Important non-goals / limitations

E2EE cannot prevent an authorized endpoint from:

- photographing the screen;
- recording audio externally;
- exporting plaintext after decryption;
- revealing content voluntarily.

Parental access also means the system does not provide confidentiality from the child's authorized guardians.

## Metadata risk

Even without plaintext, the provider may learn information such as:

- who communicates with whom;
- timestamps;
- call duration;
- IP/network information;
- device activity;
- approximate media sizes.

Metadata minimization is therefore a first-class security requirement.

## Required future analysis

A production threat model must additionally cover:

- authentication attacks;
- invitation interception;
- SIM/email account takeover;
- malicious push notifications;
- key substitution;
- rollback attacks;
- replay attacks;
- compromised build/update pipeline;
- Flutter dependency supply-chain compromise;
- SFU compromise;
- object-storage compromise;
- backup compromise;
- abuse by a guardian;
- family/guardian disputes;
- denial of service;
- traffic analysis.
