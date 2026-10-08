# 0042 — One repository, one release train: audit moves into sluis and is installed by preset to destinations

**Status:** Proposed (the owner's decisions of 2026-10-08; nothing is built before the owner accepts it). Companion of
[0041](0041-the-secret-contract.md), which decides secrets, storage, keys and deployment shapes for both products.
**Date:** 2026-10-08

sluis and audit are released from two repositories on two version lines (sluis 1.x, audit 0.x), and sluis consumes
audit as a pinned module. Every change that touches both (a key, a storage backend, a deployment shape, the catalogue
sluis records against) is two pull requests, two releases and a pin bump, and the two documentation sites describe one
installation from two sides. audit's compliance "presets" are profiles in all but name, and an installation has no
way to say how much of audit it wants: today it gets every part or assembles the parts by hand.

## Decisions

1. **audit moves into this repository as a snapshot.** truvity/audit's current tree is imported under `audit/` on top
   of sluis's master, already adapted (new module paths, presets renamed to profiles). Its git history is not
   imported: it stays in the archived truvity/audit repository. The module paths become
   `github.com/truvity/sluis/audit…`, and the shared module `github.com/truvity/sluis/storage` (packages `state` and
   `keys`, [0041](0041-the-secret-contract.md)) sits beside it.

2. **Imports go one way.** audit never imports sluis, and storage imports neither; both rules are enforced by the
   linter and by a test. sluis may import audit and storage.

3. **One version for the whole repository.** It continues sluis's 1.x line; the first unified release is v1.74.0.
   Every Go module tag (`vX`, `deploy/pulumi/vX`, `storage/vX`, `audit/vX`, `audit/sdk/vX`,
   `audit/deploy/pulumi/vX`), every image and every Helm chart is released together at one version. There is one
   changelog with a section per product, and CI runs by path.

4. **audit is installed per product, with sluis by default.** An installation of sluis installs its own audit unless
   it says `audit: {use: <installation>}` (record into another installation's audit) or `audit: {enabled: false}`. A
   shared installation serving several products is set aside.

5. **Compliance presets are renamed profiles.** `history`, `security`, `billing-nl`, `dora`, `pci-dss`, `nen-7513`
   and `evidence-etsi` are profiles: what a framework asks of a trail. The word "preset" now means only an install
   preset.

6. **Three install presets, derived from the profiles.** `operational` (writer, archive, deduplication, queue or HTTP
   ingest; no notary, no seal, lock or pseudonym keys; identifiers stay opaque; alarms off), `standard` (adds the
   notary, seals and alarms) and `attested` (adds compliance-mode Object Lock and pseudonym keys). The deployment
   chooses profiles; the preset is derived as the lowest that satisfies every chosen profile's integrity block. A
   stronger preset may be set; a weaker one is refused.

7. **Destinations: one bucket, one prefix each.** A destination has a prefix in the installation's archive bucket, its
   profiles, its retention (a lifecycle rule per prefix), the key alias its objects are encrypted under, and its reader
   grants. Object Lock is used only by an attested destination, and only on S3 (R2 has none).

8. **Routing by category.** The catalogue declares a category per action (`security`, `metering`, `activity`,
   `access`, …). The emitter sends each record once, with the union of the fields any destination needs; the writer
   routes it by category and stores, per destination, the projection that destination's profiles allow.

9. **Extensions use the existing slots.** A consumer's own fields go in `data`, `targets[].attributes`,
   `actor.attributes`, `context.areas.<area>` and `meter.dimensions`; nothing new is added to the record. sluis is the
   reference consumer.

10. **One contract, two SDKs per language.** The contract is `audit/proto/audit/v1` over Connect. Go, TypeScript,
    Kotlin and Python each get an **emitter** SDK and a **query** SDK, plus `@truvity/audit-react` (the history
    widget). Shared conformance fixtures hold every SDK to the contract. For now TypeScript and Kotlin publish to
    GitHub Packages, and Python wheels are attached to the release.

11. **One documentation tree and one ADR series.** `docs/sluis`, `docs/audit` and `docs/storage`, each with
    getting-started, architecture, deployment (per shape), operations, how-to, reference and explanation. audit's ADRs
    are renumbered into this series after sluis's, with a "formerly audit ADR" table. "People and agents" (the
    interactive and agent client classes, the three sign-out scopes, [0040](0040-agent-class-sessions.md)) becomes a
    topic of its own. The site is truvity.github.io/sluis.

## Details for implementers

### Modules and tags

| Module | Path | Tag |
|---|---|---|
| sluis | `github.com/truvity/sluis` | `vX` |
| sluis's Pulumi library | `github.com/truvity/sluis/deploy/pulumi` | `deploy/pulumi/vX` |
| the Cloudflare edge ([0041](0041-the-secret-contract.md)) | `github.com/truvity/sluis/deploy/pulumi/edge/cloudflare` | `deploy/pulumi/edge/cloudflare/vX` |
| storage | `github.com/truvity/sluis/storage` | `storage/vX` |
| audit | `github.com/truvity/sluis/audit` | `audit/vX` |
| audit's Go SDK | `github.com/truvity/sluis/audit/sdk` | `audit/sdk/vX` |
| audit's Pulumi library | `github.com/truvity/sluis/audit/deploy/pulumi` | `audit/deploy/pulumi/vX` |

The Cloudflare edge is a separate module because [0041](0041-the-secret-contract.md) keeps the Cloudflare provider
out of the core library's dependencies; it is not in the owner's list of tags and is added to the train here because
a Go module needs its own tag.

- **One tag push, every tag at one commit.** The release recipe tags every module at the same commit and version. A
  module that requires another of the repository's modules requires the same version, as `hack/pin-pulumi-require.sh`
  enforces today for `deploy/pulumi`; the check covers every module.
- **Images and charts** (sluis's and audit's) carry the release's version; a product with no change still gets a
  release.
- **The changelog** has one heading per version and, under it, a section per product (sluis, audit, storage, the
  libraries). The `Breaking:` rule of [0007](0007-breaking-changes-inside-1x.md) applies to audit from v1.74.0.
- **CI by path.** A change under `audit/` runs audit's jobs, under `storage/` storage's and both products' (both
  import it), elsewhere sluis's; the required check is one aggregate job, so the gate is the same whatever ran.
- **The import rules.** A linter rule (`depguard`) refuses `github.com/truvity/sluis` and its non-audit packages in
  `audit/…`, and both products in `storage/…`; a test runs `go list -deps` on each module and fails on a forbidden
  edge, so the rule holds even with the linter off.
- **In-repository use.** sluis's recipes that run audit's tools (`go run github.com/truvity/audit/cmd/audit@<version>`
  in the Justfile) run `./audit/cmd/audit` instead.

### Install presets

| Profile | Integrity block | Lowest preset |
|---|---|---|
| `history` | digest recommended, no lock | `operational` |
| `security`, `billing-nl` | digest required, no lock | `standard` |
| `dora`, `pci-dss`, `nen-7513`, `evidence-etsi` | digest required, compliance lock | `attested` |

The table is not maintained by hand: the derivation reads each profile's `integrity` block, so a new or changed
profile moves its preset with it, and a test pins today's rows.

| Part | `operational` | `standard` | `attested` |
|---|---|---|---|
| writer, archive, deduplication, queue or HTTP ingest | yes | yes | yes |
| notary and seals (key purpose `seal`) | no | yes | yes |
| alarms | off | on | on |
| pseudonym keys (`pseudonym`, `conceal`); identifiers otherwise opaque | no | no | yes |
| Object Lock, compliance mode, on S3 | no | no | yes |

### Destinations and routing

```yaml
audit:
  destinations:
    - name: security
      prefix: security/
      profiles: [security]
      categories: [security, access]
      retention: 400d
      key: alias/audit-<instance>-archive
      readers: [<a role or OpenBao policy the estate names>]
    - name: history
      prefix: history/
      profiles: [history]
      categories: [activity]
      retention: 90d
```

- **One bucket.** Each destination's lifecycle rule is scoped to its prefix; the archive key of
  [0041](0041-the-secret-contract.md) is per destination by alias (one alias for all is fine).
- **The preset is per installation**, derived from the union of every destination's profiles. Object Lock is set per
  object (the bucket has Object Lock enabled, without a default retention), so only an attested destination's objects
  are locked. An attested destination on R2 is refused.
- **Categories.** Every action in a catalogue declares one category; a catalogue without one is refused when the
  catalogue is checked. A destination lists the categories it takes.
- **Projections.** The writer stores, per destination, the record with the fields that destination's profiles keep
  (the profile's field rules decide), and seals each destination's objects separately.
- **Reader grants** are per prefix: an S3 grant on `<prefix>*` and its key alias, or the matching OpenBao policy.

### Repository layout

```text
audit/            the former truvity/audit tree (cmd, writer, query, keys, profiles, charts, proto, ts, …)
storage/          state and keys (0041)
deploy/pulumi/    sluis's library, installs audit by default through audit/deploy/pulumi
docs/sluis/       docs/audit/       docs/storage/       docs/decisions/   (one series)
```

### Documentation and ADRs

- Each product's tree has the same seven sections; a deployment page per shape of
  [0041](0041-the-secret-contract.md) (AWS, AWS behind Cloudflare, Kubernetes with OpenBao).
- audit's records 0001–0025 are renumbered, in their order, after the last sluis record at the time of the merge;
  their text is not edited, only their links. `docs/decisions/README.md` gains a table "formerly audit ADR NNNN →
  NNNN". No number is reserved for them before the merge.
- "People and agents" moves out of the session pages into its own topic and links 0040.
- The site is built from `docs/` and published at truvity.github.io/sluis; audit's old site redirects there.

## Consequences

- **One pull request per change**, whichever product it touches, and no pin bump between them.
- **audit's version jumps from 0.x to 1.74.0** and its module paths change, so every consumer of audit changes its
  imports once. audit is held to [0007](0007-breaking-changes-inside-1x.md)'s rules from then on, which are stricter
  than 0.x's.
- **A release of one product releases the other**, with a changelog section that may say "no changes".
- **History before the import lives only in the archived repository.** `git blame` in `audit/` stops at the import
  commit; a reader follows the link in `audit/README.md` to the archive.
- **CI cost grows** for changes under `storage/`, which run both products' jobs.
- **An installation that wants only an operational trail gets one** without the notary, seals or keys, and is told by
  the derivation when a profile it chose needs more.
- **One record, several shapes.** The emitter's payload is the union of the fields, so it carries more than any one
  destination keeps; a destination's projection, not the emitter, decides what is stored.
- **An attested destination needs S3.** An installation on R2 cannot hold an attested trail.

## Migration

1. **Import.** One pull request adds the adapted snapshot under `audit/`, the storage module and the import rules;
   truvity/audit is archived with a README pointing here.
2. **First unified release, v1.74.0.** Every tag, image and chart of the table above.
3. **Consumers switch imports** from `github.com/truvity/audit…` to `github.com/truvity/sluis/audit…`, and their
   TypeScript and Kotlin dependencies to the new packages; sluis itself switches in the import pull request.
4. **Existing audit installations** keep their bucket. An installation's current archive becomes its first
   destination; how its existing keys map to a destination prefix (a destination at the empty prefix, or a copy into
   a prefix) is settled in the implementation and documented in the release that ships destinations.
5. **Profiles and presets in configuration.** The deployment document's former `presets` key is read as `profiles`
   with a deprecation warning for one minor release, then refused.

## Rollout

The reference estate first, then the second estate, with the secret migration of [0041](0041-the-secret-contract.md).
On the reference estate, recorded:

- sluis and audit at v1.74.0 from one release, the infrastructure preview clean after the upgrade;
- a sign-in's record in its destination's prefix, readable through the query SDK by that destination's reader and
  refused to another destination's reader;
- for a `standard` or `attested` destination, an hour's seal written and verified;
- the derived preset matches the table for the profiles the estate chose.

## Rejected alternatives

- **Keep two repositories and pin.** Every cross-cutting change stays two releases and a bump; the storage module
  would have no natural home.
- **Import audit with its history.** The owner chose a snapshot; the archived repository keeps the history, and the
  import stays one reviewable commit.
- **Independent versions per module.** A reader would need a compatibility table between products released from one
  commit.
- **Choose the preset directly, without profiles.** A deployment could then claim a framework its trail does not
  satisfy; deriving the preset makes that impossible, and setting a stronger one is still allowed.
- **A bucket per destination.** More infrastructure per installation for what a prefix, a lifecycle rule and a grant
  already separate.
- **One emit per destination.** The emitter would need to know the deployment's destinations; routing belongs to the
  writer.
- **New record fields for consumers' extensions.** The existing slots carry them, and a new field is a contract change
  for every SDK.
- **A shared audit installation for several products.** Set aside, not rejected: `audit: {use: <installation>}`
  covers the case of one product recording into another's.
