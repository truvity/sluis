---
title: Commands
description: "The sluis command tree of the core: conventions, global flags, each command, the JSON output and the exit codes."
status: partial
sidebar:
  order: 2
---

sluis has one command-line tool, `sluis`, a Go binary with subcommands for operators and users ([0013](/decisions/0013-release-train-packaging-and-distribution/#one-version), [0012](/decisions/0012-the-language-follows-the-executable/#per-actor-table)). This page fixes the commands of the core, their flags, their output and their exit codes ([#1097](https://github.com/truvity/sluis/issues/1097)). Every command in the table below is built. The rest of the tree belongs to the parity milestone and follows the same conventions ([Later commands](#later-commands)).

The exit codes and the JSON output follow ritueel's [commands](https://ritueel.id/reference/cli/#exit-codes), so an operator who runs both tools learns one scheme.

The CLI owns no mechanics that a neighbour owns. The card driver behind `sluis node` is atoom's, the audit log behind `sluis audit verify` is audit's, and each command here says what sluis adds and links the owner.

## Conventions

- **Noun, then verb:** `sluis key request`, `sluis config check`. A command that acts on the whole installation has no noun (`sluis doctor`).
- **Long flags only,** written `--flag value` or `--flag=value`. Positional arguments are used only where this page shows one.
- **Results go to standard output, everything a person reads goes to standard error:** prompts, progress, warnings. A script reads standard output and the exit code.
- **A PIN, a passphrase or a secret is never a flag or an environment variable.** It is typed at the terminal, or read from a file descriptor the caller names (`--secret-fd N`). A command that needs one and has no terminal and no descriptor exits 2.
- **Offline by default.** Only `--live`, `sluis login`, `sluis token` and the confirmation of `sluis key revoke --declaration` talk to a running sluis; `config check`, `config explain`, `doctor` and the key commands read the host.
- **Colour** is off when standard output is not a terminal, with `--no-color`, or when `NO_COLOR` is set.
- **Errors name a path, never a value** ([0004](/decisions/0004-the-data-model/#schemas-are-protobuf)): `clients.example-app.redirects[0]: not an https URL`, not the URL.

## Global flags

| Flag | Meaning |
|---|---|
| `--bootstrap FILE` | The bootstrap file ([0005](/decisions/0005-the-store/#the-bootstrap-file)). The commands that read the host need it. |
| `--boundary NAME` | The boundary a command acts on. Needed when the host serves more than one boundary and the command is per boundary (the key commands, `token`). |
| `--output text\|json` | `text` by default. `json` prints one object on standard output ([JSON output](#json-output)). |
| `--log-level LEVEL` | `debug`, `info`, `warn` or `error`; `info` by default. |
| `--no-color` | No colour in text output. |
| `--help` | The command's help, on standard output, exit 0. |

## The first working version's commands

| Command | Reads | Writes | Exit codes |
|---|---|---|---|
| `sluis version` | Nothing | The version and the commit it was built from | 0 |
| `sluis config check DIR [--bootstrap FILE] [--helper PATH] [--runtime RUNTIME] [--against OLD]` | The bootstrap file and the signed config documents in `DIR`; with `--against`, the previous bootstrap file | Nothing | 0, 1, 2, 3 |
| `sluis config explain DIR [--bootstrap FILE] [--helper PATH] [--field PATH]` | As `check` | Nothing | 0, 1, 2, 3 |
| `sluis doctor [--socket PATH]` | The host: kernel, randomness, the platform's floor, the clock; with `--boundary`, key@'s driver and the intermediates, through key@ | Nothing | 0, 1, 2, 3, 10, 11, 12 |
| `sluis key request [--purpose PURPOSE] [--out DIR] [--trust-domain HOST] [--dns-zone NAME] [--ip-range CIDR] [--common-name NAME] [--ca-bundle FILE] [--replace] [--socket PATH]` | The key of the purpose, through key@'s admin socket; the boundary's names from the flags | The request and the proof in `DIR`; the key is signed for through key@ | 0, 1, 2, 4 |
| `sluis key install DIR --ca-bundle FILE [--root-list FILE (--root-pin SHA256 [--min-sequence N] \| --accept-unanchored-list)] [--replace] [--requests DIR] [--socket PATH]` | The answers in `DIR`, the requests `key request` wrote, the CA bundle and, when sluis holds one, the signed root list | The certificate, as the kind `KeyCertificate`, through key@ | 0, 1, 2, 3, 4 |
| `sluis key status [--socket PATH]` | The keys and their certificates, through key@ | Nothing | 0, 1, 2, 10, 11, 12 |
| `sluis login [--issuer URL]` | The group issuer; the boundaries' token endpoints | Tokens in the user's cache | 0, 1, 2, 3 |
| `sluis token --audience CLIENT_ID` | The user's cache; the token endpoint | A refreshed or exchanged token in the cache; the access token on standard output | 0, 1, 2, 3 |
| `sluis audit verify DIR --key VKEY` | The files of a tiled audit log in `DIR` | Nothing | 0, 1, 2, 3 |
| `sluis node unlock NODE [--socket PATH] [--secret-fd N]` | The PIN, from standard input or the descriptor; the card driver's status | The PIN into the locked card driver of the node it runs on | 0, 1, 2, 3 |
| `sluis node status NODE [--socket PATH]` | The card driver's status: whether it holds the PIN | Nothing | 0, 1, 2, 3 |
| `sluis key revoke --kid KID --reason REASON [--out FILE]` | Nothing | A key revocation declaration, to `FILE` or standard output | 0, 2 |
| `sluis key revoke --declaration FILE [--socket PATH] [--issuer URL] [--entry-socket PATH] [--wait DURATION]` | The signed declaration; the issuer's discovery document and key set, and `entry`'s | The revocations, through key@ on the node it runs on, and whether the keys left the key sets | 0, 1, 2, 3, 5 |
| `sluis key revoke --check (--declaration FILE \| --kid KID ...) [--issuer URL] [--entry-socket PATH] [--wait DURATION]` | The declaration file or the keys; the issuer's discovery document and key set, and `entry`'s | Whether the keys left the key sets; nothing is delivered | 0, 1, 2, 5 |

### How each command is tested

Every row has a test in CI against the local installation that `just local-up` starts ([REQ-TEST-018](/requirements/test/#REQ-TEST-018)). The job `smokes` runs them; `tests/e2e/cli/commands.go` lists the test of each row, and `just go-test` fails for a row of the table that has none. The commands that need a card are attested from a run on real hardware, because CI has no card.

| Command | Test in CI | Attested on hardware |
|---|---|---|
| `version` | `just local-cli-smoke`: text and JSON, on the host and as root in a container, and the usage errors | |
| `config check`, `config explain` | `just local-cli-smoke`: the signed set the installation runs, with the store executable of the build; a set with one payload changed is refused with exit 3 and explained as nothing | |
| `doctor`, `key status` | `just local-cli-smoke`: on the host and as root in a container, and as a user that is not root | |
| `key request`, `key install` | `just local-up` ends with the hand-off against key@, and `just local-handoff-check` runs it again | [REQ-PKI-028](/requirements/pki/#REQ-PKI-028), [REQ-KEYS-246](/requirements/keys/#REQ-KEYS-246) |
| `login`, `token` | `just local-person-smoke`, against the upstream identity provider's sign-in | |
| `audit verify` | `just local-smoke` and `just local-person-smoke` verify the log of the run, and the first also refuses a copy with every entry bundle changed | |
| `node unlock`, `node status` | `just local-cli-smoke`: where no card driver runs, the commands fail and claim no state, and `unlock` sends no PIN it was not given | [REQ-KEYS-086](/requirements/keys/#REQ-KEYS-086), [REQ-DEPLOY-045](/requirements/deploy/#REQ-DEPLOY-045) |
| `key revoke --kid`, `--declaration`, `--check` | `just local-cli-smoke`: the signed declaration is delivered for a key that does not exist (revoked, audited, absent from the public key set), delivered again as `already revoked`, and `--check` as the forwarder's user confirms it absent from both key sets and exits 5 for a key the installation publishes | |

On the local installation the confirmation cannot read both key sets in one process: key@'s admin socket serves root only and `entry`'s socket serves only the forwarder's user. `--declaration` run as root therefore ends with exit 5 and `entry`'s key set `unknown`, and `--check` run as the forwarder's user reads both.

### `sluis config check` and `explain`

`check` validates the bootstrap file and the signed config documents in `DIR` the way store@ does at start, with store@'s own code: the CLI runs the store executable of its release as a helper, so the check and store@ cannot disagree ([0012](/decisions/0012-the-language-follows-the-executable/#per-actor-table)). The documents, their signatures and the loading rules are atoom's `state` block; [0005](/decisions/0005-the-store/#configuration-loading) says what sluis adds. Open: atoom has no page for signed configuration documents yet.

`check` tests:

- the strict profile and the rules of each kind, the signature of each document against the publisher keys the bootstrap file pins, the sequence, and then the whole set against the registries;
- the installation: a driver that does not fit the runtime, a unit that holds a holder and a work actor, an enabled key purpose with no key or slot, and a production backup plan without a schedule or a retention ([0005](/decisions/0005-the-store/#drivers), [0003](/decisions/0003-units-transports-and-fusion/#fusion), [0006](/decisions/0006-keys-and-sealing/#key-purposes), [0010](/decisions/0010-backup-and-recovery/#what-the-archive-holds));
- the card slot map of the `KeyPlan`: two purposes in one slot, an enabled card-key purpose with no slot, a slot that is no slot of a board and a purpose listed twice are a `fail` of the `set` line that names the entry (`default.purposes[1].slot: shared with wrapping`). The slot rules are atoom's [slot plan](https://github.com/truvity/atoom/blob/master/key/slotplan/docs/slot-plan.md), shared with key@'s own check on the same vectors ([REQ-PKI-037](/requirements/pki/#REQ-PKI-037), [REQ-PKI-038](/requirements/pki/#REQ-PKI-038), [REQ-PKI-042](/requirements/pki/#REQ-PKI-042)).

It prints one line per check: `ok`, `warn`, `fail` or `not-run`. A `fail` exits 3; warnings, such as an audit log with no witness, exit 0. For a set that declares an `AuditLog`, the line `lock-mode` reports the lock mode the document declares, `none`, `removable` or `compliance` ([REQ-AUDIT-028](/requirements/audit/#REQ-AUDIT-028)); no file names the blob driver yet, so it is the declared mode and not one read from the driver. `DIR` defaults to the directory of the `file` config source that the bootstrap file names. The CLI finds the helper through `--helper`, the `SLUIS_STORE_HELPER` variable or the install path beside it, refuses a helper of another release, and `config check` runs on Linux only ([0013](/decisions/0013-release-train-packaging-and-distribution/#one-version)).

**The installation.** No file carries the runtime of an installation, so the caller names it: `--runtime bare-metal`, `serverless` or `kubernetes` ([0011](/decisions/0011-runtimes-and-installations/#runtime-kinds)). With it `check` tests each driver of the bootstrap file against what the driver declares it fits and prints one `installation` line per port, with the path of its `driver` field. A driver that does not fit is a `fail` that names the field and the runtime, never the driver ([REQ-DATA-072](/requirements/data/#REQ-DATA-072)). Without `--runtime` the line is `not-run`, with the flag to give. `not-run` is not a `fail`, so the exit code stays 0 when every other check passes: a pipeline that must have the drivers checked passes `--runtime`, or reads the `result` of each line in the JSON output and refuses a `not-run`. An empty `--runtime` is a usage error (exit 2), so an unset variable in `--runtime "$RUNTIME"` cannot turn the check off. The model is that one fact and a table of drivers in code (`actors/store/src/install.rs`). The roads not taken are a file of its own for the installation, a `runtime` key in the bootstrap file and the host's own state.

**`--against OLD`** compares the bootstrap file with the previous version and prints one `warn` line for each changed path, `added`, `changed` or `removed`, with the path and never the value, because a changed bootstrap file is a restart of store@. It compares the settings that `explain` prints, so writing a default is no change. When nothing differs the line is `ok against`. A previous file that cannot be read is a `fail` of the `against` check ([REQ-OPS-130](/requirements/ops/#REQ-OPS-130)). A signed config document needs no such line: every config change reloads as a whole snapshot.

**`--live`** is planned. It reads the durable kinds through the console API with the operator's own token. The first version has no console API, so both commands refuse the flag with a usage error (exit 2) that says why and run nothing: a pipeline never reads the answer of the files as the answer of a running installation.

**`explain`** prints what each setting resolves to and where the value comes from. It runs the same helper as `check`, with `--explain`, so the values are those store@ would run with and the CLI reads no document itself. It explains a set that passes `check` and nothing else: a set that fails is not explained, its failed lines are printed as `check` prints them, and the command exits 3. It names no runtime and takes no `--runtime`. `DIR` is required, and `--bootstrap` names the file (or `--boundary` finds it in `/etc/sluis/bootstrap.d/`). Planned: a default `DIR`. Each setting has:

| Member | Meaning |
|---|---|
| `path` | Where the setting is. In the bootstrap file it is the path `check` names (`durable.driver`, `publishers[0].kid`). In a document it is `Kind.item.path` (`Upstream.workforce.issuer`); a kind with one item has the item `default`, and a list element is `[0]`. A `check` line and a path of `explain` name the same place, so one can be pasted into `--field`. |
| `value` | The effective value, as text, printed by the rules below. |
| `class` | The storage class of the kind, `config`. A setting of the bootstrap file and a built-in have `-`. |
| `layer` | The layer that set the value: `bootstrap`, `config` (a signed document) or `built-in` (what the binary carries) ([0004](/decisions/0004-the-data-model/#layers-and-the-direction-rule)). |
| `version` | The `seq` of the document, or the release for a built-in; empty for the bootstrap file. |
| `source` | `bootstrap` for the file, `store@` for a built-in, or the document as the audit fact of a start names it, `Kind:seq:sha384` ([0005](/decisions/0005-the-store/#configuration-loading)). The text output shows the first twelve digits of the digest. |
| `checks` | JSON only: the checks of `check` that cover the setting. |
| `secret` | JSON only, and only when `true`: the value is the name of a credential in the secret source. |
| `digest` | JSON only, and only when `true`: the value is `sha384:` and the digest of what the file holds, not the value. |

`--field PATH` limits the output to the setting at `PATH` and the ones under it, at a boundary of the path (a `.` or a `[`). A path that names no setting is a usage error (exit 2), and so is an empty one. A setting that a file leaves out has no entry. The text output is a table; `--output json` prints the `fields` and the `checks` that ran.

**What `explain` prints, and what it never prints.** It fails closed: store@ holds a closed table of the fields of each kind it reads, each with the way it is printed (`actors/store/src/config/explain.rs`), and a field the table does not list is printed as its digest until someone decides that it may be shown. A test fails when the payload reads a key that the table does not cover. The ways are:

- **Shown:** a name, a number, an identifier. A duration is shown in its largest whole unit (`60s` is `1m`).
- **A credential is its reference.** The `secret` of an `Upstream` or an `InstallationClient` is the name of a credential, printed and marked. The command opens no socket and never holds a secret's value.
- **A URL has no query and no fragment:** `https://app.example.com/callback?<elided>`. A URL that holds user information is refused by `check` at load.
- **A public key is its pin,** the SHA-384 of its bytes, and an attestation is its digest.
- **A value that is longer than 256 bytes or holds an unsafe character is its digest,** `sha384:` and the SHA-384 of its bytes: a control character, an invisible format character or a line or paragraph separator. A Cedar text and a grant vocabulary, which store@ does not interpret, are one setting each. The CLI also shows such a character as `?` in text.
- **A refusal names a path and a rule, never a value,** as in `check`.

### `sluis doctor`

`doctor` checks the platform before a key is created: the kernel is 5.18 or newer, a hardware random generator is present or deliberately absent, the entropy pool is initialised, the systemd floor or its declared exception, the clock, and key@'s driver ([0006](/decisions/0006-keys-and-sealing/#algorithms)). It also prints the expiry warnings of `key status`. It exits 3 on a `fail`, then 1 when `kernel` or `entropy` did not run, otherwise with the highest finding: a `warn` line is a notice (10), and an expiry warning is its level. The version of systemd comes from the host's `systemctl` at a fixed path (`/usr/bin/systemctl`, then `/bin/systemctl`) and never from `$PATH`, so a script earlier in the path, run as root under `sudo -E`, cannot decide the floor check.

It only reads. It writes nothing, sets nothing (not the clock either), makes no key and prints no value of a key, a credential or a file. It takes no argument.

| Check (`name`, `path`) | `ok` | `warn` | `fail` | `not-run` |
|---|---|---|---|---|
| `kernel`, `kernel.release` | 5.18 or newer | older: do not make a key on a clone of the host | | not Linux, or a release it cannot read |
| `entropy`, `random.crng` | the kernel's generator is initialised (`getrandom` without waiting) | | not initialised: make no key until it is | not Linux, or the call was refused |
| `hwrng`, `random.hwrng` | always: it names the hardware generator, or says that there is none and that the kernel's own generator is then the only source | | | not Linux |
| `systemd`, `systemd.version` | 252 or newer | older: the declared exception, not a supported platform | | systemd is not the init (a container), or the version cannot be read |
| `clock`, `clock.sync` | the kernel says its clock is synchronised, with the offset | not synchronised (below) | | the kernel does not answer, as under a system-call filter that refuses `adjtimex`, or not Linux |

A missing hardware generator is a note and never a warning: the kernel's own generator is then sluis's only source of randomness. A check that cannot run is `not-run` and is never read as passed. If it is `kernel` or `entropy`, the checks that decide whether a key may be made here, the command exits 1 after it prints every line, so that `sluis doctor && make-key` cannot pass on a host where the generator was never asked. A `not-run` of another check changes no exit code; a pipeline that must have it run reads its `result` in the JSON output.

**The clock.** A host whose backup clock lost its time starts at an old date until the time service sets it. The check does not trust the kernel's flag alone: a date before sluis existed is a `warn` whatever the flag says, because the flag can say synchronised before the time service has set the clock. An unsynchronised clock is a `warn` (exit 10) and never a `fail`: the line says that this is the usual state of the first minute after a power cut and to run `doctor` again, and that a clock that stays so means the host has no working time source. The check asks the kernel with `adjtimex` and an empty request, which reads and sets nothing. A clock matters because audit's writer seals with its time ([REQ-AUDIT-017](/requirements/audit/#REQ-AUDIT-017)).

**Key@.** With `--boundary` (or `--socket PATH`), `doctor` also asks key@ on this node, over the status call of [`key status`](#the-key-commands) (root only), for where its keys are held and for the expiry of their certificates:

| Check (`name`, `path`) | `ok` | `warn` | `fail` |
|---|---|---|---|
| `key-store`, `key.store` | a card, or software in a development boundary | software in a staging boundary: the keys are not hardware-held | software in a production boundary, which key@'s floor refuses |
| `expiry`, the purpose | a certificate with time left, with the days | a notice, an alert or a critical alert, as `key status` says it; the exit code is its level (10, 11 or 12) | |
| `certificate`, the purpose | | the intermediate (`x509-intermediate`) has no certificate installed | |

With no `--boundary`, or where key@ has no admin socket on this host, the `key-store` check is `not-run` and says why: `doctor` is also run before key@ exists. A key@ that has a socket and cannot answer ends the command with exit 1 after every line is printed, never as a pass. The exit code is 3 for a `fail`, then 1 for a key@ that could not be asked, then the highest level among the `warn` lines and the expiry levels.

### The key commands

The hand-off is ritueel's [hand-off contract, version 1](https://ritueel.id/reference/hand-off-contract/), which owns the files, the profiles, the reason codes and the test vectors; sluis refuses a version it does not know ([0024](/decisions/0024-root-custody-and-the-signing-api/#the-hand-off)). The commands run as root on the node, because key@'s admin socket is root's.

- **`sluis key request`** asks key@ on this node for the key of the boundary's X.509 intermediate, has key@ sign a PKCS#10 request for it, and writes the request and the proof into `DIR` (default `/var/lib/sluis/keys/request/`), after a self-check: the files must pass the certifier's checks 1 to 5, or nothing is written. It prints the key's fingerprint, the SHA-256 of its DER `SubjectPublicKeyInfo`, which the person at the ceremony compares with the one the certifier shows. The files are `<stem>.csr.pem` and `<stem>.proof.json`, with the stem `<boundary>-<purpose>`; a stem that exists is not replaced without `--replace`, because an answer to the old request would then be refused. The request carries the profile the contract fixes: critical basic constraints (a CA, path length 0), key usage, name constraints and extended key usage. The subject is the common name: `sluis <boundary> intermediate` by default, or `--common-name` (1 to 64 printable ASCII characters). With `--ca-bundle` it also refuses a name that a root of the bundle has and the key of a root. The permitted names are given by `--trust-domain` and `--dns-zone`, each repeated as needed, in the contract's strict lower-case form; at least one name is required, and the flags stand in until the `Boundary` kind declares the names. `--ip-range` is unusable with contract version 1, which refuses a permitted IP range; the flag is kept for a certifier that accepts one ([0023](/decisions/0023-certificate-authorities/#the-intermediates-name-constraints)). A key on a card brings the attestation of its slot, which the card driver has verified; `software` brings none. `--purpose` takes `x509-intermediate` only: the release-signing subkey joins with the signing API ([#890](https://github.com/truvity/sluis/issues/890)), and a key in AWS KMS is not requested yet ([#1069](https://github.com/truvity/sluis/issues/1069)).
- **`sluis key install DIR`** reads, for each request in `--requests`, the answer in `DIR` (`<stem>.cert.pem`, and `<stem>.result.json` unless the CA is on [the floor](/decisions/0024-root-custody-and-the-signing-api/#the-floor)), and hands the certificate to key@ with the root certificate of the CA bundle that issued it, only after the requester's checks of the contract pass: the contract version and `request_sha256`, the certificate verifies to an anchor of `--ca-bundle`, its key is the request's, its subject and extensions are what the request asked, its validity is 12 to 14 months and has not ended, and the digests in the result file match. When sluis holds a signed root list (`--root-list`, with `root-list.jws` beside it), the CA bundle must be the bundle of that list and an answer whose `trust_sequence` is above the list's sequence is refused. The list is read by the checks of ritueel's reference against the initial pins given by `--root-pin`, which `--root-list` requires; `--accept-unanchored-list` reads it anyway, with the trust sequence and the signers reported as not run. `--min-sequence N` is the lowest sequence to accept, so an old list that is still signed cannot come back (`rollback`). A certificate does not replace an installed one that ends no later, or whose trust sequence is lower, unless `--replace` says so; a renewal needs nothing. Files are opened without following links and must be regular files of at most 1 MiB. A check that cannot run is listed in `not_run` of the JSON output and never reported as passed. A refusal exits 3 with the contract's code (`issuer`, `key-mismatch`, `profile`, `validity`, `version-unsupported`, `request-mismatch`, `malformed`, `ca-bundle-mismatch`, and the codes of the root list) and installs nothing; key@'s refusal exits 4. A request with no answer in `DIR` is `no-answer`. The token certificate is then issued on the node by the new intermediate ([0006](/decisions/0006-keys-and-sealing/#the-token-signing-key)).
- **`sluis key status`** lists each enabled key: its purpose, its slot on a card, its store, its fingerprint, its expiry and its warning level. It exits with the highest finding: a notice 60 days before an expiry (10), an alert 30 days before (11) and a critical alert 14 days before (12) ([0024](/decisions/0024-root-custody-and-the-signing-api/#lifetimes-and-the-warning)). It asks key@ over its admin socket (`/run/sluis/key/BOUNDARY/admin.sock`, `--socket PATH` names another) and reads and writes nothing, so a status can be run as often as a person likes.
  - **The expiry of a key** is the earliest end of validity among the certificates of its installed chain, in UTC, with the whole days left (negative once it has ended). A chain that has ended is critical (12): the renewal is overdue. A key with no chain has no expiry and no level; the intermediate with no chain is named on standard error and the exit stays 0, because the window between `key request` and `key install` is part of the hand-off.
  - **The fingerprint** is the SHA-256 of the DER public key, the one `key request` prints. The text output cuts it to 16 digits; the JSON object holds all 64.
  - **A clock that is not set is not judged.** When the date is before 2026, or before a certificate of an installed chain began, `key status` judges no expiry: it lists the keys with no days left and no level, says why on standard error and exits 1. Run it again in a minute.
  - **A certificate is installed or it is not:** `certificate` in the JSON object is `installed`, `none` or `invalid`. `invalid` is a certificate that key@ holds for the key and that cannot be used; the command exits 1 so that it is not read as `none`. An empty list of keys is a failure too, because key@ always holds at least its wrapping key.
  - **A key@ that cannot be asked is a failure (exit 1),** never an empty list. Warnings go to standard error and the table to standard output.
  - **Planned:** the metric `sluis.key.expiry` and its alert rule ([REQ-PKI-031](/requirements/pki/#REQ-PKI-031), [#1068](https://github.com/truvity/sluis/issues/1068)) wait for the telemetry kit. The OpenPGP subkey joins with the signing API.

The root list is part of `TrustAnchors`, the config kind that holds the root list, the CA bundle and the SSH CA key list ([0024](/decisions/0024-root-custody-and-the-signing-api/#the-hand-off)); `sluis trust sync` fetches it at parity.

### `sluis node unlock`

A board starts locked: the card driver has no PIN. The PIN rules, the locked start and the driver's admin socket are the card driver's, which is atoom's ([slots, PIN and touch](https://github.com/truvity/atoom/blob/master/key/yubikeypiv/docs/slots-pin-touch.md#the-locked-start-and-the-admin-socket)); [0006](/decisions/0006-keys-and-sealing/#touch-and-pin) says how key@ uses it. `sluis node unlock NODE` gives the driver the PIN. It runs on the node itself, as root, because the driver's admin socket is mode `0600`. An operator reaches it through a shell on the node and pipes the PIN, so the PIN crosses the network only inside that session.

- **The PIN is read from standard input** (one line), or from the descriptor named by `--secret-fd`. It is never a flag or an environment variable, and a terminal is refused because this tool cannot turn the echo off.
- **`NODE` must be the node the driver serves.** The command asks the driver first, and a different name exits 3 with the reason `node` before any PIN is sent.
- **A wrong PIN is tried once.** The card blocks after three wrong attempts. The command exits 3 with the reason `pin` and prints how many attempts the card still allows. With one attempt left the PIN is not sent at all: it exits 3 with `pin_retries`. A blocked card exits 3 with `pin_blocked`; the PUK is a ceremony of the owners and not a command.
- **`--socket PATH`** names the admin socket; by default it is `/run/sluis-drv-key-yubikey-piv-BOUNDARY/admin.sock`, from `--boundary`.

### `sluis node status`

`sluis node status NODE` says whether the card driver of the node it runs on holds the PIN. It is the read-only half of `unlock`: it asks the same status call on the admin socket and sends no PIN. It prints how many PIN attempts the card has left (`node NODE: PIN attempts left: 3`; `unknown while unlocked` while the driver holds the PIN; with one left, `ONE MORE WRONG PIN BLOCKS THE CARD`), then the last line, `node NODE is locked` or `node NODE is unlocked`, which scripts match whole. With `--output json` it prints `{ "node", "locked", "pin_retries" }`, where `pin_retries` is left out when it is unknown. The counter is read with a `VERIFY` that carries no PIN, so the command never burns an attempt. It runs on the node, as root, for the reason `unlock` does.

- **`NODE` must be the node the driver serves.** A different name exits 3 with the reason `node` and prints no state.
- **A driver that cannot be asked is a failure** (exit 1), never `locked` or `unlocked`.
- **`--socket PATH`** names the admin socket, as for `unlock`.

### `sluis key revoke`

An operator revokes keys in two steps and a check, with the publisher's signature between them, because the publisher key lives where the config is signed and never on a node ([0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/#the-declaration)). Neither step needs the token keys or sign-in.

1. **`sluis key revoke --boundary B --kid KID --reason REASON [--out FILE]`** writes the declaration for the keys: `--kid` is an RFC 7638 thumbprint and is repeated for each key, and `REASON` is `compromise`, `suspicion`, `lost-board` or `drill`. It names keys, never a purpose. A key id that is not a thumbprint, a key named twice or another reason exits 2. `--allow-unknown` lets key@ revoke keys it does not know, such as a lost board's own keys.
2. **The publisher signs it** as a `KeyRevocationDeclaration`, with the key that signs the `KeyPlan`. On the local installation the harness does it (`tools/harness/sign-config`).
3. **`sluis key revoke --boundary B --declaration FILE.jws`** runs on any node of the boundary, as root, and delivers the signed declaration to key@'s admin socket (`--socket PATH` names another). It prints each key with `revoked` or `already revoked`. A refusal of key@ exits 3 with its reason and path: `declaration`, `seq`, `schema`, `other-boundary`, `wrapping-key` or `unknown-kid`. A key@ that cannot be reached, or an audit fact that key@ could not make durable, exits 1; in the second case the keys are revoked. A declaration that key@ took in part exits 1 and prints each key as `revoked` or `not yet revoked`: deliver the same declaration again to complete it.

**The confirmation.** After the delivery the command checks that the keys have left every key set a verifier can be given ([REQ-KEYS-088](/requirements/keys/#REQ-KEYS-088), [0009](/decisions/0009-export/#confirmation), [0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/#confirmation)). It does not trust a report: it fetches two addresses, the way a relying party does, and needs every revoked key absent from both:

- **The public key set,** the `jwks_uri` that the issuer's OpenID discovery document names, found at `ISSUER/.well-known/openid-configuration`. It is fetched with no credential, and its URL must have the issuer's origin: a `jwks_uri` on another origin is `could not tell`. `--issuer URL` names the issuer (no query); by default it is the issuer that `entry` reports on its socket. The issuer is `https`, or `http` on `127.0.0.1` or `[::1]` for development. <!-- terms-allow: origin (the web origin of RFC 6454) -->
- **The live key set of `entry`,** read from `entry`'s socket, which is what the edge falls back to. On bare-metal it is `/run/sluis/entry/BOUNDARY.sock`, and `--entry-socket PATH` names another. A socket is reached only on a node that runs `entry`, so the command runs there.

Both are checked by default, because each alone leaves a hole: the public address cannot see that `entry` still lists the key, and the socket cannot see a stale copy at the edge, which is what relying parties read.

**What a clean result proves.** One round that finds the keys absent is one sample of one address from this host. It does not speak for every node of a CDN, and the socket is the socket of one node. On a multi-node boundary, run the check again on each node with `--entry-socket`. If the purge failed, the stale window of an edge copy is 65 minutes ([REQ-EXPORT-023](/requirements/export/#REQ-EXPORT-023)) and can outlast `--wait`: check again later with `--check`.

**How the request is made.** Neither request is a cache-buster: a plain `GET` with no query, no `Cache-Control`, `Pragma` or conditional header, no credential and no `Accept` header, so a cache that has not dropped the key answers as it answers a verifier. A new HTTP/1.1 connection is opened for each request, with no proxy from the environment. The trust roots are the system's. A redirect is not followed. The answer is read up to 1 MiB after decompression.

**What is looked for.** The keys of the declaration, read from its payload, together with any key that key@ reported besides. A key is found by its `kid` and by its key: the RFC 7638 thumbprint of every listed EC, OKP or RSA key is compared with the revoked kids, so a key listed under another `kid` is `still listed`. A key of a type that sluis uses whose members cannot be read, a duplicate member, or a member in another case (`Keys`, `KID`) is `could not tell`. With no key to look for the result is `not confirmed`.

The command reads both addresses again every 2 seconds, for up to `--wait DURATION` (default 2 minutes, which covers the purge target of 60 seconds; `0` checks once; at most 30 minutes), and stops as soon as one round finds every key absent from both. It prints one line for each address with one of three results, then the warnings and the verdict:

- `confirmed absent`: the answer was a key set and none of the revoked keys is in it;
- `still listed: KID`: the key set lists the key; a copy that carries an `Age` header shows it (`the copy is 1200 s old`), which points at a stale edge;
- `could not tell: REASON`: the address is unreachable, answered other than 200, was too slow, was not a JSON key set, or its discovery document has no `jwks_uri`, names another issuer or sends the check to another origin. The command never reads a check that did not run as passed ([principle 4](/principles/#4-verify-never-trust-a-claim)). <!-- terms-allow: origin (the web origin of RFC 6454) -->

Warnings do not fail the check: an `--issuer` that differs from the issuer `entry` reports, or a public key set that shares no key with a non-empty live key set of `entry`. The last line is `confirmed` and the exit code 0 when every address is `confirmed absent`, otherwise `not confirmed` and exit code 5. The result is the last complete round. When `exporter@` could not read `KeyRevocation`, the published set stays as it was and the command says `not confirmed` ([REQ-EXPORT-024](/requirements/export/#REQ-EXPORT-024)). An audit fact that key@ could not make durable exits 1 and takes the place of exit 5. The command knows keys, not purposes: a key that was never published is `confirmed absent`, which says nothing about the purpose.

```text
AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8 revoked
public key set (https://sluis.example.com/jwks, issuer https://sluis.example.com from entry, 4 keys read): still listed: AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8 (the copy is 1200 s old)
entry's live key set (/run/sluis/entry/dev.sock, issuer https://sluis.example.com from entry): could not tell: timed out
not confirmed
```

This ends with exit code 5 and the line `sluis key revoke: the keys are revoked in key@, but are not confirmed gone from every key set a verifier can be given` on standard error. With `--output json` the `revoke` object has a `confirmation` member ([JSON output](#json-output)). The flags `--issuer`, `--entry-socket` and `--wait` belong to `--declaration` and `--check`; with `--kid` and no `--check` they are a usage error.

**`sluis key revoke --boundary B --check (--declaration FILE | --kid KID ...)`** is the second look. It delivers nothing and asks key@ nothing: it reads the keys from the payload of the declaration file (not verified again: key@ verified it when it was delivered) or from `--kid`, and runs the confirmation. A delivery again with the same declaration is a retry only while that declaration has the highest `seq` key@ took; after a later one key@ refuses it, and `--check` is the way to look again. The same flags and exit codes apply: 0, 5, and 2 for a usage error.

:::note[Proposed]
Two points are open. On the serverless and Kubernetes runtimes `entry` has no socket on the node, and the command reaches its live key set as those runtimes are built ([#1714](https://github.com/truvity/sluis/issues/1714)); until then `--entry-socket` is the only way and the address is `could not tell` without it. Where `exporter@` publishes the static document, the command is to name that address apart from `entry`'s key set behind the forwarder, and to read the purge from the report ([#1453](https://github.com/truvity/sluis/issues/1453), [#1454](https://github.com/truvity/sluis/issues/1454)).
:::

### `sluis login` and `sluis token`

- **`sluis login`** signs a person in at the group issuer with the authorization code flow, PKCE `S256` and a loopback redirect on `127.0.0.1` (RFC 8252), as the public client that config declares for the CLI, an `InstallationClient` ([0021](/decisions/0021-clients/#the-client-declaration)). It then exchanges that token silently (RFC 8693) at each boundary's own token endpoint, so the front never sees a boundary's tokens ([0017](/decisions/0017-identity-and-sign-in/#step-up)). `--issuer` names the group issuer the first time; afterwards it comes from the cache. A browser opens; with no browser the command prints the URL to standard error.
- **`sluis token --audience CLIENT_ID`** prints an access token for that audience at the boundary given by `--boundary`, from the cache while it is valid, otherwise by a refresh or an exchange. The exchange is the public client's own: the CLI names itself with `client_id` and sends no secret ([0022](/decisions/0022-workload-identity-and-host-enrolment/#proofs-and-the-exchange)). A refusal exits 3 and looks the same for an unknown and a refused audience ([Contracts and errors](/reference/contracts/#oauth-and-openid-connect-endpoints)).
- **The flags** `--issuer URL` (login and token), `--client-id ID` (the client the CLI signs in as; `sluis-cli` unless named), `--no-browser` (print the URL, open nothing) and `--timeout DURATION` (how long login waits for the browser; 5 minutes by default). An issuer that registers the CLI as a confidential client needs its secret on `--secret-fd N` for both commands. An `http://` issuer is accepted only on `127.0.0.1` or `[::1]`; otherwise the issuer must be `https://`, and the trust roots are the system's, extended by `SSL_CERT_FILE`.
- **The cache** is `$XDG_CONFIG_HOME/sluis/tokens/` (`~/.config/sluis/tokens/` when the variable is unset), one file per issuer, mode `0600`. A file that another user can read is refused, not used.

### `sluis audit verify`

`audit verify` is audit's command: it checks a tiled log from its files alone, offline, with no server. The reference is [audit's `audit verify`](https://github.com/truvity/audit/blob/master/docs/reference/audit-verify.md) and the decision is [audit 0014](https://github.com/truvity/audit/blob/master/docs/decisions/0014-the-front-the-console-page-and-audit-verify.md).

Built: `sluis audit verify DIR --key VKEY [--key-file FILE] [--origin LINE]` runs it from sluis's CLI for a log in a local directory with one log key, and prints `ok` with the origin, the size and the root hash. It exits 3 for a refused log (the file at fault is in `reasons`) and 1 for a directory it cannot read. Planned: the command moves to audit and is renamed there, and sluis's CLI keeps no copy of it. <!-- terms-allow: origin (the C2SP checkpoint origin line) -->

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success; for `key status` and `doctor`, nothing due. |
| 1 | Failure: something that should have worked did not, such as an unreadable file or a store that did not answer. |
| 2 | Usage error: an unknown command or flag, a missing argument, or a secret with no terminal and no descriptor. |
| 3 | Refused by a check or by policy: a schema error, a failed `doctor` or `config check` line, a hand-off answer that fails the requester's checks, a refused sign-in or token. |
| 4 | Refused by key@ or the card: a card that is locked or blocked, a PIN not delivered, a store that refuses the key's policy. |
| 5 | Not confirmed: `sluis key revoke` delivered the declaration, and a key set a verifier can be given still lists a revoked key or could not be checked. The `outcome` is `failed`. |
| 10, 11, 12 | `key status` and `doctor` only: notice, alert and critical. The highest finding wins. |

## JSON output

With `--output json`, a command prints **one JSON object on standard output** when it ends, followed by a newline, whatever its outcome. Only arguments so broken that `--output json` cannot be read print no object; the exit code is then 2.

Every object has these members:

| Member | Meaning |
|---|---|
| `output` | The version of this format, `1`. |
| `command` | The command, such as `key install`. |
| `exit_code` | The exit code the command returns. |
| `outcome` | `done`, `refused`, `usage` or `failed`, matching the exit code; `done` for 10 to 12, and `failed` for 5. |
| `reasons` | When refused: the codes, such as the hand-off contract's or a schema path. |
| `error` | When failed or a usage error: a message for a person. Its wording is not part of the format. |

And these where they apply:

| Member | Commands | Meaning |
|---|---|---|
| `version`, `commit` | `version` | The release version and the commit. |
| `checks` | `config check`, `config explain`, `doctor` | A list of `{ "name", "result", "path" }`; `result` is `ok`, `warn`, `fail` or `not-run`, and `path` names the field a check is about. `doctor` adds `note`, a sentence about what it found; `config check` adds `rule` to a failed line. |
| `fields` | `config explain` | A list of `{ "path", "value", "class", "layer", "version", "source", "checks" }`, with `"secret": true` when the value is a credential's reference and `"digest": true` when it is a digest ([`explain`](#sluis-config-check-and-explain)). |
| `keys` | `key request`, `key status` | A list of `{ "purpose", "boundary", "store", "slot", "fingerprint", "certificate", "not_after", "days_left", "level" }`; `certificate` is `installed` or `none` (`key status`); `level` is `none`, `notice`, `alert` or `critical`. |
| `requests` | `key request` | A list of `{ "stem", "kind", "files": [{ "path", "sha256" }] }`. |
| `installed` | `key install` | A list of `{ "stem", "outcome", "reasons" }`. |
| `token` | `token` | `{ "access_token", "expires_at", "audience", "issuer" }`. |
| `login` | `login` | `{ "issuer", "client_id", "subject", "expires_at" }`; no token. |
| `audit` | `audit verify` | The result of audit's command: `{ "origin", "size", "root_hash", "checkpoints", "entries" }` ([audit's reference](https://github.com/truvity/audit/blob/master/docs/reference/audit-verify.md)). | <!-- terms-allow: origin (the C2SP checkpoint origin line) -->
| `node` | `node unlock`, `node status` | `{ "node", "locked", "pin_retries" }`; no PIN. `node unlock` prints it after the PIN was taken, so `locked` is `false` (and has no `pin_retries`); `node status` prints what the driver says, with `pin_retries` left out when the driver cannot read the counter. |
| `revoke` | `key revoke` | `{ "written" }` for the declaration; for the delivery `{ "seq", "keys": [{ "kid", "already" }], "audited", "confirmation" }`. `confirmation`, which `--check` prints alone, is `{ "confirmed", "rounds", "kids", "warnings", "addresses": [{ "name", "address", "state", "issuer", "issuer_source", "keys", "listed", "found_by_key", "age", "reason" }] }`: `name` is `public` or `entry`, `state` is `absent`, `listed` or `unknown`, `issuer_source` is `entry` or `flag`, `keys` is the number of keys the set listed, and `reason` is a short sentence about what went wrong, never a value of the answer. |

Members are lower-case words with underscores; times are RFC 3339 in UTC; the object is not in canonical form. A new member is not a new version, so a reader ignores a member it does not know; a member renamed, removed or changed in meaning is version 2.

## Later commands

These come with the parity milestone and follow the conventions above ([#1097](https://github.com/truvity/sluis/issues/1097)); none is built:

- `sluis trust sync` and `trust enroll` ([0022](/decisions/0022-workload-identity-and-host-enrolment/#the-trust-bundle));
- `sluis restore`, a command of store@ with a plan by default, `--apply` to write and `--overwrite` to replace ([0010](/decisions/0010-backup-and-recovery/#restore)); the backup is a job of exporter@, not a command ([0009](/decisions/0009-export/));
- `sluis pki` and `sluis ssh`, the commands for integrations, clients and credentials, and the replacements for v1's `sluisctl` sign-in helpers (kubeconfig, cloud profiles, database and vendor wrappers).

The rest of the audit commands (`audit read`, `audit search`, `audit reindex`) are audit's ([audit 0014](https://github.com/truvity/audit/blob/master/docs/decisions/0014-the-front-the-console-page-and-audit-verify.md)). Until a command is built, the integration pages that show one show the shape, not decided names ([Examples are illustrative](/integrations/#examples-are-illustrative)).
