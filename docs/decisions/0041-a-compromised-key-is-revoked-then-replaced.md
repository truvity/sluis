# 0041 — A compromised key is revoked, then replaced: it leaves at once, its replacement signs at once, and only a person declares

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #1341](https://github.com/truvity/sluis/issues/1341).

## Context

A key is replaced on a suspected compromise, and a compromise is answered by revocation and a transition (see the [principles](/principles/) on rotation and on layered trust). This record says how, for every kind of key that sluis holds. Which keys exist and what each is for is [0006](/decisions/0006-keys-and-sealing/)'s.

The forces:

- **A healthy key's schedule is the wrong schedule for a stolen key.** A new token-signing key is pre-published for the lead time and an old one stays in the key set for 65 minutes. Applied to a stolen key, forged tokens would verify for the lead time plus 65 minutes after the decision.
- **A compromised node must not decide whether its own key leaves.** Only a node's own `signer` moves its key through the ordinary states.
- **A restore must not bring a revoked key back.** Roll-forward re-applies revocations of sessions, clients and certificates ([0010](/decisions/0010-backup-and-recovery/)); the state of a key needs the same.
- **Data keys differ in what a thief gains.** For a key that protects integrity, a thief forges values; for a key that protects confidentiality, the thief already has the plaintext key, and refusing opens only takes the data offline.
- **A stolen board is a PIN away from being useful.** A board that reconnects asks its peers for the PIN, so the peer must check that the board is still listed.
- **A verifier that breaks a standard is detected and fixed, not accommodated,** so the emergency path has no workaround for one.
- **A tightening takes effect at once and stays,** a person declares and the machine acts, a check that cannot run is reported as not run, and a time is measured and dated.

## Decision

### A person declares, and a suspicion counts

- **A compromised key is revoked first and replaced second.** Revoked means it leaves every published set and signs, opens or wraps nothing more. `key@` refuses a Sign with a revoked key of any signing purpose, an SSH CA included. The pre-publication period and the 65 minutes exist so that a healthy key can move without breaking a verifier ([0006](/decisions/0006-keys-and-sealing/)); they do not apply to a revoked key.
- **A suspected compromise is treated as a compromise.** A false alarm costs one refresh per client. A wait costs forged tokens.
- **Only a person declares.** An operator declares the keys and a reason in a declaration signed with a publisher key, and `sluis key revoke` delivers it. A signal raises an alert and never revokes: an alarm that revoked on its own would let an attacker stop sign-in.
- **A revocation is an audit fact and raises an alert,** naming the key, the reason and the operator.
- **Making the replacement is automatic.** The new key is generated in its store, published and, where a board's intermediate certifies it, certified without a ceremony ([0006](/decisions/0006-keys-and-sealing/)). The declaration is the one human step.

### The declaration

- **A declaration is a signed document under a publisher key,** on the trust path of the key plan and the boundary document ([0006](/decisions/0006-keys-and-sealing/)): a JWS with the header `alg`, `kid`, `seq` and `kind`, the kind `KeyRevocationDeclaration`, verified by `key@` under the publisher keys of its own copy of the bootstrap file ([Signed config](/reference/signed-config/)). The kind is not a config kind, so `store@` never loads a declaration and a config document is never taken as one. The operator is the publisher key that signed it, and the audit fact names it.
- **It needs neither the token keys nor sign-in.** A compromise may have taken the token-signing key or the issuer, so the authority of a declaration is the publisher's signature, which neither of them can make.
- **It names exact keys, never a purpose.** A declaration lists the RFC 7638 thumbprints of the keys to revoke, the boundary and a reason from a closed list. A whole purpose is never revoked by a cutoff in time: a key that a ceremony put on a card has no age to compare with, so a cutoff would refuse its replacement too.
- **`key@` takes a declaration once and completes it on a retry.** It keeps the highest `seq` it took, with the digest of the payload, in a mark of its own state directory, saved before the first item is written. An older `seq`, and the same `seq` with another payload, are refused, also after a restart. The same declaration again is a retry: an item that exists counts as revoked, so a declaration that a `store@` failure stopped half way is completed by delivering it again. It is refused as `partial`, with the keys not yet revoked, and the writes and the audit fact run to their end even when the operator's command goes away.
- **`key@` refuses a declaration that names a key it does not know, unless the publisher allows it.** A key is known when it is a signing key that this `key@` holds, a token key that the signing ring lists for any node, or a key that a certificate of a signing purpose certifies, so a declaration for another node's key is taken on any node. A mistyped key id would otherwise be written as a revocation that stops nothing while the operator believes the key revoked. Some keys no source knows: a lost board's own keys, or a token key that a node's `signer` removed from the ring. A declaration that says `allow_unknown: true`, which the publisher signs, revokes them anyway, and the audit fact marks them unknown. A wrapping key is refused whatever the declaration says, because its revocation is a recipient's. A declaration for another boundary is refused.
- **The declaration reaches `key@` on its admin socket,** `<run-dir>/admin.sock`, which only root on the node reaches, as the card driver's unlock socket. The operator signs the declaration where the publisher key lives, copies it to a node, and runs `sluis key revoke --declaration` there through a shell on the node. The channel carries the declaration; the signature is its authority, so the `signer` of any node may relay the same bytes.
- **The revocation stands whatever the audit transport does.** `key@` writes the items, then makes the audit fact durable, and says when it could not; a tightening does not wait for the audit log. The fact names the keys, the reason, the publisher key, the node and the uid that delivered it.

### The token-signing key

- **A token-signing key has four states:** `pre-published`, `active`, `retiring` and `revoked`. `revoked` is reachable from any state and is terminal: it is the presence of the key's item in `KeyRevocation` ([The tighten-only record](/decisions/0041-a-compromised-key-is-revoked-then-replaced/#the-tighten-only-record)). A restore, a roll-forward or a write by the key's own node never moves a key out of it, because `store@` refuses every update, delete and apply on an existing `KeyRevocation` item. `signer` never promotes or signs with a revoked `kid`.
- **The operator command, and the `signer` of any node, may relay a declaration to `key@` so that it revokes any node's token key.** A `signer` never asks for a revocation on its own initiative. Only the owning node moves its own key through the other states. A tightening is open to all and a loosening to the owner, so a compromised owner can neither keep its key nor bring it back. Built: the relay by the operator command. Planned: the relay by the `signer` of any node.
- **The key leaves the key set at once.** The exporter and `entry` omit every `kid` listed in `KeyRevocation` from every key set they publish, as a filter at publication, so the omission does not depend on `signer` or on the node that owns the key. The exporter publishes the new set and reads it back within 60 seconds of seeing the revocation ([0009](/decisions/0009-export/)). The purge of the edge copy is the Cloudflare add-on's; the core holds no Cloudflare credential. In the core an edge copy expires with its cache time, which the edge rules set below the pre-publication time of a key.
- **Forged tokens stop verifying when a verifier's copy of the key set expires.** With the add-on and a readable store that is the 60 seconds of the purge target plus the key set's `max-age` of 300 seconds, about 6 minutes; if the purge fails, the edge copy lives its time to live of at most 300 seconds more, about 11 minutes. If the exporter cannot read the store, see [Accepted risks](/decisions/0041-a-compromised-key-is-revoked-then-replaced/#accepted-risks). No drill has measured these figures yet; the incident page records the figure a drill measures. Tokens that the key signed legitimately stop verifying too. Their holders refresh, and refresh tokens are opaque rows that a key compromise does not touch ([0018](/decisions/0018-sign-in-sessions/)).
- **Sessions and sealed data are untouched.** The attacker with a token key cannot mint a refresh token or open a sealed value.
- **sluis can tell the forged tokens from the real ones,** because every signature is an audit fact and an access token carries a `jti` ([0020](/decisions/0020-grant-vocabulary-and-token-claims/)). A relying party that logs `jti` can do the same.
- **The CRL is not a control for tokens.** A standard verifier ignores `x5c`, so a revocation entry for the token certificate protects no one. Removal from the key set is the only revocation a token key has.
- **The named exceptions follow the same path,** each with its own key: the per-audience RS256 key, the FAPI keys, the cloud-federation keys and the NATS nkeys ([0006](/decisions/0006-keys-and-sealing/)). A NATS operator or account key is also reissued on the NATS side, and the incident page lists that step.
- **The workload-issuer key of `sluis-node` follows it too** ([0022](/decisions/0022-workload-identity-and-host-enrolment/)). A cloud provider fetches the key set on its own schedule, outside the cache budget of sluis, so a drill measures how fast each cloud forgets a removed key and the incident page states the figure per cloud.

### The replacement

- **A replacement signs soon, not after the lead time, when no other key of the issuer is active.** Its key set is published, read back and purged where a purge exists, before its first token. A verifier that meets an unfamiliar `kid` fetches the key set again (OpenID Connect Core 1.0, section 10.1.1), and one that does not is broken against the standard and is fixed, not accommodated. Built.
- **A multi-node installation needs no new key to go on.** The other nodes' active keys are already published and cached, so verifiers see no unfamiliar `kid`. This holds only when the compromise is local to one node.
- **Where the compromise is at `key@`, the key service role, the key policy or the card, another key under the same access is no transition.** The replacement is made in a new store or under a new role first, because a key is born in its store and never leaves it, and redundancy is no defence against compromise. When the compromise is in the key service itself, the first step is outside sluis: the operator removes the attacker's access there, and the incident page names the order.

### Confirmation

- **`sluis key revoke` prints "not confirmed" until it has fetched the published key set from its public address and the live key set of `entry`, and found the key absent from both.** A stale edge copy is therefore reported, not assumed away. When the exporter cannot read `KeyRevocation`, the published set stays as it was and the command says "not confirmed". For a purpose that publishes nothing, the command reports what it could and could not check. Built.
- **A drill measures the time from declaration to a verifier's refusal,** and the incident page states it with its date and versions. Until the first drill the figure is empty.

### The tighten-only record

- **Every revocation, a token-signing key's included, is an item of the durable kind `KeyRevocation`.** `key@` is its one writer, and it writes an item only on a declaration signed with a publisher key: the operator command delivers it, and the `signer` of any node may relay it when the owner is down, but none acts on its own. The item names the revoked target (a `kid`, a generation or a recipient) and a reason code from a closed list in plain fields that the publishers and `signer` read. The operator and any free-text reason are in the audit fact, not in the kind, which is therefore plain: a sealed kind could not be read by the publishers. The kind is backed up, and on a restore the archive's items and the target's are merged by union.
- **`key@` reads the kind at every Sign and fails closed.** A `KeyRevocation` that cannot be read refuses the Sign with the reason `revocation-unreadable` and audits it, so a `store@` outage stops token signing and audit checkpoints until it ends. That is the price of "not revoked" being verified and not assumed: a cache that kept signing through an outage would sign with a key an operator had revoked a moment before.
- **`store@` enforces that the kind only grows.** It is create-only: for every caller, `key@` included, `store@` refuses an update, a delete and an apply on an existing item. The lease kind's rule refuses delete and apply only and allows an update, so it is not the model, and the direction rule of [0004](/decisions/0004-the-data-model/) alone would permit a delete. The kind is the run-time layer that the rule permits because it can only tighten.
- **It works while the compromised node is down:** the `key@` of any other node writes it. Making a removal permanent, such as dropping a recipient from the list, goes through the reviewed key-plan change afterwards. A revocation itself is permanent.
- **A revocation survives a restore.** The kind is backed up, and the roll-forward re-applies every key revocation that audit records after the archive was taken, also into an empty target ([0010](/decisions/0010-backup-and-recovery/)). The kind is built and backed up; the roll-forward step is planned.

### Data keys

Data keys are made, wrapped and rotated by generation in atoom's `seal` block, which `key@` hosts ([atoom 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md)). This section is the rule that sluis gives its host for a declared compromise. The rotation mechanics are atoom's.

- **A data key declared compromised ends its generation, and what happens to its opens depends on what the key protects.** `key@` makes a new generation that every later seal uses and refuses every seal under the compromised one. Whether it also stops opening it is decided per purpose:
  - **`grant`, `cookie` and `chain` protect integrity,** because a forged value is the threat. `key@` stops opening the compromised generation at once, so forged values stop working.
  - **`data`, `credential`, `pseudonym` and `evidence` protect confidentiality,** and the thief already holds the key, so refusing opens protects nothing and only takes the data offline. `key@` keeps opening the compromised generation until nothing sealed under it remains. For `data` and `credential` that is until the re-seal job has rewritten every value, and then `key@` deletes the old wraps. For `evidence` the log is append-only and cannot be re-sealed, so the generation stays for the retention (an accepted risk, below).
  - **The re-seal job is paginated, asynchronous and idempotent,** because it runs over a store of any size and is run again after a failure. A compromised `data` generation is retired only when the job ends, so ordinary opens during the job raise no alarm; a compromised `evidence` generation stays openable as an accepted risk and raises none; after retirement the alarm stands.
- **`grant` needs no re-seal.** Nothing durable holds a grant ([0007](/decisions/0007-policy-and-sealed-grants/)); the old generation is deleted at once and grants are issued again under the new one.
- **`cookie` ends every browser session of the boundary** at the next request. People sign in again, silently while the upstream session lives ([0018](/decisions/0018-sign-in-sessions/)). Refresh-token chains are not affected.
- **`chain` ends that tenant's chains,** because it is a tenant's key. The blast radius is one tenant, and its people sign in again.
- **`pseudonym` starts a new epoch at once.** It makes keyed hashes and seals nothing, so there is nothing to re-seal. Past records keep their pseudonyms ([audit 0011](https://github.com/truvity/audit/blob/master/docs/decisions/0011-evidence-encryption-and-pseudonyms.md)).
- **The tenant's `data` key and the boundary's `data` key are re-sealed,** and keep opening until the job ends. Re-sealing cannot reach a copy that a backup archive holds.
- **An archive keeps the wraps and the values it held.** The archive's retention is the bound on that exposure, and the incident page says so. Erasure has the same limit.
- **`mac` is an accepted risk,** and so is what `evidence` already sealed ([Accepted risks](/decisions/0041-a-compromised-key-is-revoked-then-replaced/#accepted-risks)).
- **On Lambda the delay is the cache's time to live,** as it is for erasure ([0006](/decisions/0006-keys-and-sealing/)). It matters for the integrity keys, whose opens must stop; for the others the compromised generation goes on opening by design.

Planned: the generation drop, the re-seal job and the declaration of a generation.

### Wrapping keys, recipients and archives

- **A compromised AWS wrapping key is replaced, every wrap is rewrapped under the new one, and then the old key is disabled.** KMS automatic rotation keeps the old key material and so does not answer a compromise.
- **On AWS every data key is also wrapped for the backup recovery key.** Disabling the old KMS key would otherwise make every old archive unreadable. The key is sluis's own, a cloud KMS key in the boundary's anchor account, and using it stays a recovery step of the operator ([0006](/decisions/0006-keys-and-sealing/)). The cost is one key per environment.
- **An old archive stays readable by whoever held the old key until the archive expires.** After the old key is disabled the thief cannot read it through KMS, but a copy of the unwrapped key from before the disable can. The incident page says so; retention bounds it and nothing removes it.
- **A compromised backup recovery key is replaced by the operator in the anchor account,** and every wrap is rewrapped for the new one. Its old archives stay openable by the thief until they expire.
- **The backup archive key is replaced and the archives are made again;** the old archives expire.
- **A customer-account KMS key is the customer's lever** ([0006](/decisions/0006-keys-and-sealing/)): a compromise on the customer's side is the customer's act, and sluis fails closed for that tenant.
- **The enrolment key of `exchange` is replaced by a bundle change** ([0022](/decisions/0022-workload-identity-and-host-enrolment/)).

Planned.

### Boards

- **A lost, stolen or suspect board is removed at once.** A `KeyRevocation` item removes its recipient from the wraps and its keys from the slot map, without waiting for a reviewed commit. `key@` makes no new wrap for it and deletes its existing wraps. The revocation is permanent. A board comes back only with a new key, through a reviewed key-plan change that lists the new key's thumbprint. Planned.
- **A peer releases a PIN only to a board that the key plan lists and `KeyRevocation` does not revoke.** The card's PIN policy is not the control while a board can reach its peers: a stolen board that reconnected would otherwise be given its PIN. Built.
- **A suspected PIN is changed with a command,** which delivers the new PIN to the board's `key@` over the unlock channel. Until it exists, the remedy is to replace the board. Planned.
- **The board's token-signing key is revoked as above,** and the other boards go on signing with theirs.

### Log keys

The audit log, its checkpoint keys, its witnesses and the key-change record are audit's ([audit 0008](https://github.com/truvity/audit/blob/master/docs/decisions/0008-the-log.md), [audit 0009](https://github.com/truvity/audit/blob/master/docs/decisions/0009-witnesses-and-verification.md), [audit 0011](https://github.com/truvity/audit/blob/master/docs/decisions/0011-evidence-encryption-and-pseudonyms.md)). A log key rotates on compromise only, by a key-change record that audit writes and a verifier reads.

Where audit runs beside sluis, `key@` holds audit's keys (the log, witness, `evidence` and `pseudonym` purposes). For these keys sluis does this:

- **A declaration names a log or witness key like any signing key.** `key@` writes the `KeyRevocation` item and refuses to sign with the key at once, so the writer cannot sign another checkpoint with it.
- **`key@` makes the replacement key,** and audit signs the key-change record with it.
- **The `evidence` and `pseudonym` keys follow the data-key rule above.**
- **sluis does not decide what a verifier trusts.** The verifier's list of keys, with the size from which each applies, and the loss of fork detection when a witness key is revoked are audit's rules.

### Intermediates and SSH CA keys

Planned, with the certificate authorities ([0023](/decisions/0023-certificate-authorities/)).

- **sluis acts first on an intermediate.** `ca` reads and watches `KeyRevocation`, so a revocation of the intermediate's key makes it stop issuing under the intermediate and removes its certificate from the `TrustBundle` it renders. Hosts take the next bundle, and the root's CRL, which ritueel signs, follows ([0024](/decisions/0024-root-custody-and-the-signing-api/)). sluis does not wait for ritueel.
- **What the bundle controls rests on an assumption.** `sluis trust sync` writes the certificates of the bundle as the system's anchors and drops a certificate that does not chain to a pinned root ([0022](/decisions/0022-workload-identity-and-host-enrolment/)). **Assumed:** the sync replaces the anchor set with the bundle, and the anchors are the intermediates. If a host anchors a root, removing an intermediate from the bundle stops nothing for it, and the control is ritueel's root CRL and the short lifetimes of leaves, because leaves carry no CRL distribution point and most TLS and SPIFFE clients never fetch a CRL ([0023](/decisions/0023-certificate-authorities/)). The assumption is checked when the sync is built, and the incident page is corrected to the result ([#1368](https://github.com/truvity/sluis/issues/1368)).
- **The CRL of an intermediate's leaves is published, not optional,** so that a database that configures one can refuse a revoked leaf; it is the control where the bundle is not.
- **A stolen SSH CA key is not answered by a revocation list.** A list cannot name certificates that nobody knows exist. The key is replaced on ritueel's signed list of SSH CA keys, `sluis-host` fetches it at a stated interval and alerts when its copy is older than twice that interval, and a host CA also needs a change of the clients' `@cert-authority` lines, which the incident page describes.
- **The release-signing subkey is revoked under both OpenPGP primaries** (REQ-SUPPLY-012); this record changes nothing there.

### What each kind does

| Kind | Declared as | What stops | What is replaced | What consumers see | How fast |
|---|---|---|---|---|---|
| Token-signing key, per node or per boundary | `sluis key revoke`, key id | The key signs nothing and leaves the key set | A new key, active soon when none is | Forged and old tokens fail; clients refresh; an unfamiliar `kid` makes a verifier fetch the set again | The key set's `max-age` plus the edge copy's life (about 6 minutes with the add-on and a readable store; a drill measures it) |
| Per-audience RS256, FAPI, cloud-federation and NATS keys | The same, with their key ids | The same | The same; NATS also on the NATS side | The same | The same |
| Workload-issuer key (`sluis-node`) | The same | The same | A new key | A cloud provider's cache decides | Measured per cloud in a drill |
| Audit log, issuance log and witness keys | The same, key id and reason | `key@` signs nothing with the key | A new key; audit writes the key-change record | A verifier given both keys trusts each up to its size | Next checkpoint |
| X.509 intermediate | The same | `ca` stops issuing; the certificate leaves the bundle | A new intermediate through the hand-off | Hosts that sync the bundle refuse its leaves; otherwise the root's CRL and leaf lifetimes bound it | The next bundle sync; the root's CRL follows |
| SSH user CA, SSH host CA | The same | Hosts stop listing it once the signed list changes | A new CA key on ritueel's list | Hosts take the list at the stated interval; a host CA needs client edits | The stated refresh interval |
| `grant` | `sluis key revoke`, generation | Seals and opens under the old generation are refused | A new generation | Nothing visible; a grant lives about 60 seconds | About 60 seconds; on Lambda the cache's time to live |
| `cookie` | The same | Seals and opens under the old generation are refused | A new generation | Every browser session ends; sign in again | The next request; on Lambda the cache's time to live |
| `chain` (a tenant's key) | The same, tenant | Seals and opens under the old generation are refused; the tenant's chains end | A new generation | That tenant's people sign in again | The next open; on Lambda the cache's time to live |
| `data` (tenant, boundary) | The same | Seals under the old generation are refused; it keeps opening until the re-seal ends, then is deleted | A new generation and a re-seal | Nothing, apart from latency during the job | At once for new seals; hours for the re-seal |
| `credential` (a tenant's key) | The same, tenant | As `data` | A new generation and a re-seal | As `data` | As `data` |
| `pseudonym` | The same | The epoch ends; no new hash under it | A new epoch | Past records stay linkable to the thief | At once |
| `evidence` | The same | Seals under the old generation are refused; it keeps opening for the retention | A new generation for new evidence | Nothing; what is already sealed stays exposed (accepted) | At once for new seals |
| `mac` | Not replaced in place | Nothing | Nothing | Nothing | An accepted risk ([below](/decisions/0041-a-compromised-key-is-revoked-then-replaced/#accepted-risks)) |
| AWS wrapping key | The same | Old key disabled after rewrap | A new KMS key; every wrap rewrapped | Nothing | Hours, with the rewrap |
| Board (its key-agreement key and card) | The same, node | Its recipient and wraps are removed; its PIN release is refused | A new board and card | Other boards rewrap | At once for the removal |
| Backup recovery key, archive key | Operator, anchor account | Rewrap for the new recipient | A new recipient or key | Old archives stay openable until they expire | At the operator's step |

### Accepted risks

These are accepted as risks and not as solutions. Each is written in the [threat model](/security/threat-model/) and revisited at the stated point.

- **`mac` is not replaced in place.** A new key would change every link id, upstream session id and index entry, and a migration needs the sealed inputs. If it leaks, the thief can confirm guessed e-mail addresses or upstream ids offline against any id seen. Revisited when a migration has been measured.
- **What `evidence` already sealed stays exposed.** New evidence is sealed under a new generation, but the old one keeps opening for the retention, because shredding it would destroy evidence that the law may require and the log it protects is append-only. If it leaks, the thief reads the tenant's audit and usage evidence from before the declaration. The retention is the bound. Revisited when the first tenant with an evidence retention exits, with a per-epoch evidence key as the option.
- **The Lambda delay is the cache's time to live.** A revoked data key generation can still open values in a warm function until its entries expire. Revisited after a drill has measured it: the options then are a shorter time to live, which costs key service calls, and a revocation marker read on every cache hit, which costs a read per request.
- **The static key set follows a revocation only as far as the exporter can see it.** `entry` and `signer` fail closed. For the static copy, the revocation takes effect as far as the exporter can read `KeyRevocation`: about 6 minutes while it can. If it cannot, the static set keeps the key until it can, `sluis key revoke` says "not confirmed", and the live key set of `entry` is already clean. When the bucket and `entry` both fail, the edge's stale window of at most 65 minutes can serve an old copy. The price of keeping the object is that a `store@` blip does not stop verification for every relying party ([0009](/decisions/0009-export/)). Revisited after a drill has measured how long the exporter goes blind in practice.

### Detection and the drill

- **An alarm is raised when the key service records a signature or an unwrap by a principal other than `key@`, or more signatures for a key than the audit log records.** Cards have no sign counter, so a board's detection stays audit-based. Planned.
- **The incident page states, for each kind, the command, the effect, what consumers see and the measured time** ([Incident response](/security/incident-response/)). A compromise drill of the token-signing key runs at least once a year and after a change to the key path; the recovery sign-in and the backup restore have drills of their own. The page is a target until a drill has filled its figures.

### Built and planned

Built: the declaration, `KeyRevocation` and its create-only rule, the refusal to sign with a revoked key, the omission from every published key set, the replacement of a token-signing key, `sluis key revoke` with its confirmation, the revocation relayed by the operator command, and the PIN-release check. Planned: the re-application of revocations after a restore, the relay by any node's `signer`, the declaration of a data key generation and of a recipient, the re-seal job, board removal and wraps, AWS wrapping-key replacement, the intermediate and SSH CA procedures, detection from the key service, the drill and the filled incident page. Revoke and replace belong to the core and are part of its definition of done ([roadmap](/roadmap/)); the intermediate and SSH CA procedures follow the PKI add-on.

## Consequences

- A compromised token-signing key stops verifying in about 6 minutes while the exporter can read the store (about 11 if the purge fails, longer for the static copy if it cannot), instead of after about 1 hour 45 minutes, which a healthy rotation would take (the 30 minute lead time, the 65 minutes of retention and 10 minutes of cache). The cost is one refresh per live client in the first minutes, and an unfamiliar-key window on a single-key installation for verifiers that refetch with a throttle.
- The guarantee that a revoked key stays revoked lives in `store@`, not in `signer`: a fourth state, `revoked`, a new durable kind, `KeyRevocation`, with one writer, and a new command. Revoking needs a publisher key and a shell on a node, and not the token keys or sign-in.
- A re-seal job is the first background job over sealed data, and it needs a pagination and a resume rule like any job of that size.
- Every data key on AWS is also wrapped for the backup recovery key, so a boundary needs one such key once, and the wrap count per data key becomes two.
- A board's removal no longer waits for a reviewed commit, and it is permanent. A board comes back only as a new key with a new thumbprint.
- Four risks are written down and not solved, and one measurement is left to the first drill. The incident page is part of the product.

## Roads not taken

- **A standing pre-published spare key per issuer.** It removes the unfamiliar-key window, but it costs a key and a slot per boundary and is useless when the compromise is at the key service or role level, where the spare shares the compromised access.
- **Wait the lead time before the replacement signs.** A single-key installation would refuse every sign-in for 25 to 30 minutes, a self-made outage on top of the compromise.
- **Lengthen the overlap or keep serving the old key for verifiers that cache too long.** A verifier that breaks a standard is detected and fixed, and every minute of overlap is a minute of forged tokens.
- **Revoke automatically on an alert.** An attacker who can trigger the alert could stop sign-in at will.
- **Let only the owning node revoke its key.** A compromised node would decide whether its own key leaves.
- **Mark the token certificate in a CRL.** Standard verifiers ignore `x5c`, so the entry protects no one.
- **Shred the `evidence` wrap on compromise.** It destroys evidence the law may require.
- **A per-epoch `evidence` key now.** It bounds a loss, but it adds a key per epoch before a threat is shown; it returns when the retention is revisited.
- **Keep the old KMS key enabled, scheduled for deletion.** Old archives would stay readable by the thief for as long as the key exists.
- **Start a new log when a log key is compromised.** It is the simplest, but it breaks "one continuous history" and loses the checkpoints a witness already cosigned.
- **Leave the data keys to the same overlap as a healthy rotation.** A generation that opens for hours after a declaration is a generation the thief can open.
- **Declare with a short-lived token that carries fresh multi-factor authentication.** The token is signed by the issuer whose key may be the compromised one, and it needs sign-in to be up during the incident.
- **Declare through a step-up in `signin`.** It ties revocation to the sign-in path it may have to stop, and `signin` is not on the trust path of `key@`.
- **Revoke a purpose with a cutoff in time, refusing the keys made before the declaration.** A key a ceremony put on a card has no known age, so the cutoff refuses the replacement too; a declaration names the keys instead.

## Follow-ups

- All are parts of [#1341](https://github.com/truvity/sluis/issues/1341), with their own issues [#1361](https://github.com/truvity/sluis/issues/1361) to [#1373](https://github.com/truvity/sluis/issues/1373).
- Production-blocking: the relay by any node's `signer`; the re-application of revocations after a restore; the incident page and the first token-key drill.
- Planned: the declaration of a data key generation and of a recipient; board removal and the PIN change; the generation drop and the re-seal job per data key; detection from the key service; AWS wrapping-key replacement and the recovery wrap; the intermediate and SSH CA procedures, with what the bundle holds ([#1368](https://github.com/truvity/sluis/issues/1368)).
- Open: the compromise rule of the KV store key (purpose `secret`). KV's rewrap job is its re-seal ([0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/)); the rule for a declared compromise of that key is not written down yet ([#798](https://github.com/truvity/sluis/issues/798)).
- Open: revisit `mac` and `evidence` at the points named under Accepted risks.
