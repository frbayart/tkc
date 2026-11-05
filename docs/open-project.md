# Open Project Principles

## Public-by-design

The project should be developed as an openly documented system rather than a closed product whose security claims depend on secrecy.

Public documentation should include:

- architecture;
- protocol specifications;
- trust assumptions;
- threat model;
- cryptographic design;
- key lifecycle;
- relationship model;
- parental-control semantics;
- data-retention behavior;
- metadata collected by the provider;
- security limitations.

## Security principle

> Trust the protocol, not the provider.

The security argument should explain why possession of server infrastructure, databases and object storage is insufficient to decrypt protected content.

## Open source

Where practical, publish:

- Flutter client;
- protocol definitions;
- backend implementation;
- deployment examples;
- test vectors;
- cryptographic interoperability tests;
- threat-model updates.

## Reproducibility

Long-term goals should include reproducible or independently verifiable client builds where feasible, because an open protocol does not protect users if a distributed client silently changes key-handling behavior.

## Security review

Security-sensitive changes should be reviewable independently.

Cryptographic designs should prefer established protocols and libraries and should undergo specialist review before being presented as production-safe.

## Documentation versioning

Specifications should be versioned independently from application releases when appropriate.

Breaking protocol/security changes should have explicit migration documentation.

## Suggested repository layout

```text
/
├── README.md
├── docs/
│   ├── architecture.md
│   ├── trust-model.md
│   └── threat-model.md
├── spec/
│   ├── identity-family.md
│   ├── relationships.md
│   ├── relationship-policies.md
│   ├── messaging-cryptography.md
│   ├── calls-recording.md
│   └── retention.md
├── apps/
│   └── flutter/
├── server/
├── protocol/
└── security/
```

The exact repository structure can evolve; separation between normative protocol specifications and implementation documentation is more important than the directory names.
