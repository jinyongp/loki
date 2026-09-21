# Authenticated Release and Toolchain Metadata Selection

Status: accepted for A13 WI-001  
Evaluated: 2026-09-21

## Decision

Loki will use The Update Framework (TUF) as the authoritative update metadata and client-verification protocol for releases and independently updateable toolchain metadata.

The implementation target is TUF 1.0 semantics using the maintained Go implementation `github.com/theupdateframework/go-tuf/v2`. The evaluation used TUF specification 1.0.36 and go-tuf v2.4.2, which were current at the time of this decision.

Build provenance is a separate evidence layer. Release artifacts will carry in-toto/SLSA provenance authenticated in a Sigstore bundle. Provenance verification is required by the release gate, but Sigstore is not the update freshness or rollback-protection root of trust.

This split is intentional:

- TUF answers whether a release/toolchain target is authorized, current, role-correct and rollback-safe.
- in-toto/SLSA records what produced an artifact.
- Sigstore authenticates that provenance and can package the verification material for offline verification.
- Artifact digests remain content identities and never substitute for metadata authenticity.

## Requirements fit

| Requirement | Selected mechanism |
| --- | --- |
| Release and toolchain metadata authenticity | TUF signed targets/delegated-targets metadata |
| Scoped release vs toolchain authority | Separate delegated TUF target roles |
| Freshness/freeze protection | TUF metadata expiry, especially timestamp metadata |
| Rollback protection | Persisted trusted metadata versions and TUF monotonic-version checks |
| Mix-and-match protection | TUF snapshot metadata |
| Root/key rotation and recovery | Versioned TUF root metadata with threshold root keys and sequential root updates |
| Target immutability | TUF target length + SHA-256 plus immutable Loki release/toolchain manifests |
| Build provenance | in-toto Statement/Attestation Framework with SLSA provenance predicate |
| Provenance authentication | Sigstore bundle with pinned signer identity/issuer policy |
| Offline verification | TUF cached trust metadata plus self-contained Sigstore bundle verification material |
| Source-free bootstrap | Bootstrap ships a trusted TUF root and obtains authenticated release targets without a source checkout |

## TUF repository model

The repository uses the four TUF top-level roles:

- `root`: long-lived trust root, kept offline for normal publishing.
- `timestamp`: short-lived online freshness metadata.
- `snapshot`: binds a coherent set of target metadata versions.
- `targets`: delegates release and toolchain namespaces.

Consistent snapshots are enabled.

The top-level targets role delegates at least these disjoint namespaces:

- `releases`: Loki release manifests, bootstrap artifacts and immutable release artifacts.
- `toolchains`: toolchain catalogs and independently updateable provider artifacts.

Release authorization and toolchain authorization use separate delegated keys. A compromised toolchain publisher therefore cannot authorize a Loki release, and a release publisher cannot silently broaden toolchain metadata authority.

The client persists trusted metadata and version state. A client must never reset its trusted version counters merely because a fetch fails or because a new bootstrap invocation starts. Expired, older, wrong-role, improperly signed or internally inconsistent metadata fails closed.

## Root and signer policy

The initial root policy is a 2-of-3 threshold for the root role. Root private keys are offline during normal publishing.

Root rotation follows the TUF sequential root-update algorithm. Recovery does not replace the local trusted root with an arbitrary remotely supplied root; each next root version must be accepted through the existing trusted root chain.

Online metadata roles use separate keys. The implementation keeps signer backends outside the metadata model so online keys can later live behind the established signing/KMS boundary without changing the TUF verification contract.

Exact expiry durations remain an operator/release policy value, but WI-002 must enforce bounded expiry and reject expired metadata. Timestamp metadata is intentionally the shortest-lived role.

## Release target layout

A release manifest is itself an immutable TUF target. It binds the release generation to concrete content identities, including as applicable:

- Loki host binaries and bootstrap binaries;
- OCI image digests;
- enabled optional-component artifacts;
- configuration/policy schema compatibility;
- migration and rollback compatibility;
- required toolchain/catalog generation identities;
- provenance bundle identities;
- notices/license bundle identities.

Mutable Git tags, mutable release URLs and registry tags are discovery conveniences only. They are not trusted identities.

## Toolchain metadata transition

A09's release-packaged local catalog remains a valid deterministic seed during the migration to A13. A13 moves independently updateable provider catalogs and artifacts under the delegated `toolchains` role.

Project selector files continue to select only from administrator-permitted metadata. TUF metadata changes the authenticated candidate set; project files never alter TUF roots, delegated keys, role thresholds, expiry policy or artifact origins.

## Provenance selection

Release builds use the in-toto Attestation Framework and the SLSA provenance predicate. Provenance subjects are immutable artifact digests.

The release workflow produces a Sigstore bundle for required released artifacts or release manifests. Verification policy pins the expected build identity/issuer rather than accepting any valid Sigstore identity.

Where the publishing environment provides a transparency service, inclusion evidence is required and is carried in the bundle. Verification must be possible from the distributed bundle without requiring a live transparency-log lookup during bootstrap or recovery.

GitHub Artifact Attestations are an acceptable producer for GitHub-hosted release workflows because they use Sigstore and expose offline verification, but Loki's stored provenance contract is the standard attestation/bundle evidence rather than a dependency on the GitHub CLI.

## Why alternatives were not selected

### Raw checksum files or detached signatures

They authenticate a blob but do not provide TUF's rollback, freeze, mix-and-match, delegated-role and root-rotation verification workflow. They remain useful as target content identities but are insufficient as the update protocol.

### Sigstore alone

Sigstore provides strong signer identity, transparency and artifact/provenance authentication. It does not by itself define the release-index freshness, monotonic metadata state, snapshot consistency and delegated update-role model required by A13. It is therefore complementary to TUF rather than a substitute.

### in-toto/SLSA alone

Provenance describes how artifacts were produced and supports downstream policy decisions. It is not a software-update freshness/rollback protocol.

### Custom Loki metadata signatures

Rejected. A13 explicitly requires an established update/signing primitive and there is no benefit in designing a new cryptographic metadata protocol when TUF directly covers the required attack classes.

## Implementation boundary for WI-002

WI-002 should:

1. introduce a TUF client/verifier boundary using `go-tuf/v2`;
2. persist trusted metadata/cache state in host-owned state outside project workspaces;
3. embed or package only the initial trusted root needed for bootstrap;
4. model `releases` and `toolchains` as delegated target roles with independent keys;
5. enforce metadata and target size limits before parsing/downloading;
6. enable consistent snapshots;
7. fail closed on expiry, rollback, signature threshold, delegated-role and target hash/length failures;
8. cover sequential root rotation plus compromised-key recovery fixtures;
9. expose verified target descriptors to lifecycle/toolchain consumers rather than passing them unverified URLs;
10. keep publishing/signing authority separate from the verification client.

WI-003 then defines Loki's immutable release-manifest target schema. WI-004 adds provenance/notices generation and verification. WI-005 consumes verified TUF targets in the source-checkout-free bootstrap.

## Validation required by A13

At minimum, deterministic fixtures must reject:

- forged root, targets, snapshot and timestamp metadata;
- a correctly signed target under the wrong delegated role;
- expired timestamp/snapshot/targets metadata;
- replay of older trusted metadata versions;
- mix-and-match target metadata against the wrong snapshot;
- target hash/length mismatch;
- skipped root versions;
- root rotation without the required old/new trust transition;
- delegated release metadata signed with a toolchain key and vice versa.

Positive fixtures must prove sequential root rotation/recovery and independent release/toolchain publication.

## References

- The Update Framework Specification 1.0.36: https://github.com/theupdateframework/specification/blob/master/tuf-spec.md
- go-tuf releases (v2.4.2 current at evaluation): https://github.com/theupdateframework/go-tuf/releases
- Sigstore bundle format: https://docs.sigstore.dev/about/bundle/
- Sigstore verification: https://docs.sigstore.dev/cosign/verifying/verify/
- in-toto Attestation Framework: https://github.com/in-toto/attestation/blob/main/spec/README.md
- SLSA Build Provenance: https://slsa.dev/spec/v1.2/build-provenance
- GitHub Artifact Attestations: https://docs.github.com/en/actions/concepts/security/artifact-attestations
