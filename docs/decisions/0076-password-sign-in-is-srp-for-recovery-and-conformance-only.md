# 0076 — A password signs in by SRP, for recovery and for the conformance harness only

**Status:** Accepted (2026-10-19). Extends [0075](0075-recovery-is-turned-off-from-the-console-and-on-by-configuration.md)
(the recovery switch closes the new door too) and [0072](0072-storage-layout-v5-module-first.md) (two new parameters in
the issuer's namespace, and a deny on the password). Owner decisions D688 (SRP for this password), D709 (keep the
hand-written implementation, with the review's fixes), D710 (the recovery switch shuts the conformance account too),
D711 (one client key, `harness`), D712 (a password recovery says `amr` `pwd`).
**Date:** 2026-10-19

## Context

Two things were wrong with the password door.

1. **The issuer had none.** On Lambda `/authorize` leads to the upstream identity provider and nowhere else. The only
   password prompt was the console's own recovery page, so the OpenID conformance harness
   ([`hack/conformance_drive.py`](../guides/sluis/run-conformance.md)), which drives a browser to the issuer's
   `/login`, could not sign in on Lambda.
2. **The password crossed the wire.** The console's recovery page posted it in a form (or a JSON body, or a bearer), and
   the service read the password parameter from SSM to compare it. Anything that logs a request body, or reads the
   service's parameters, sees the password.

The owner's instruction (D688): *follow the SRP RFC for this password-based, recovery-and-conformance-tests-only
username and password*.

## Decision

### 1. The protocol is SRP-6a

[RFC 2945](https://www.rfc-editor.org/rfc/rfc2945) is the protocol and
[RFC 5054](https://www.rfc-editor.org/rfc/rfc5054) fixes the arithmetic, over the **3072-bit group** of its Appendix A
(`g = 5`). The server stores a salt `s` and a verifier `v = g^x mod N` and never the password. With `H` = SHA-256, `PAD`
the big-endian form left-padded to `|N|` = 384 bytes, and `|` concatenation:

| value | definition |
|---|---|
| `k` | `H(N \| PAD(g))` (RFC 5054 2.5.3) |
| `x` | `H(s \| PBKDF2-HMAC-SHA256(H(I \| ":" \| P), s, R))`: **the username `I` is part of `x`** (RFC 5054 2.6), and the inner hash is stretched (below) |
| `u` | `H(PAD(A) \| PAD(B))` |
| `B` | `k·v + g^b mod N`; `A = g^a mod N`; `a`, `b` are 256 random bits |
| `S` | `(A·v^u)^b` at the server, `(B − k·g^x)^(a + u·x)` at the client; both mod N |
| `K` | `H(PAD(S))` |
| `M1` | `H(H(PAD(N)) ⊕ H(PAD(g)) \| H(I) \| s \| PAD(A) \| PAD(B) \| K)` (RFC 2945) |
| `M2` | `H(PAD(A) \| M1 \| K)` |

**Deviations, stated.** RFC 5054 says SHA-1; this uses SHA-256 throughout. RFC 2945 gives `K` as a SHA-1 interleave of
`S` and leaves group elements unpadded in `M1`; here `K = H(PAD(S))` and every group element has the fixed width of `N`,
so the browser and the server cannot disagree on a leading zero. `x` is stretched with PBKDF2 (below), which the RFC
does not do. The two proofs are mutual: the page checks `M2` before it moves on. The server refuses `A` when it is
zero modulo `N` or not below `N`, before it computes anything from it; the client does the same for `B`; both refuse
`u = 0`. Proofs are compared in constant time. The arithmetic is `math/big` and `BigInt`, which are not constant-time;
`b` lives for two minutes and each source gets a few tries.

**Stretched.** `R = 600000` rounds of PBKDF2-HMAC-SHA256 (OWASP's figure), so a stolen verifier costs 600000 hashes and
a 3072-bit exponentiation a guess, where RFC 5054's `x` costs one hash. `R` is a protocol constant (`srp.Iterations`)
and is **recorded in the credential** (`"iterations"`) and sent to the client with the salt, so it can be raised later
without a flag day; the page refuses a number below 100000 or above 10 million, so a server cannot switch the
stretching off. The salt is **random and independent of the password** (32 bytes): the server hands it to anyone who
starts an exchange, and a salt made from the password would let that anyone test guesses against it with no verifier
at all. The page's cost: PBKDF2 at 600000 rounds measured 70 ms in headless Chrome and 67 ms for the whole proof under
node; a sign-in is well under a second. Generated passwords (40 characters of a 57-letter alphabet, over 200 bits) are
the design point; **the tools refuse a weak one** (`srp.CheckPassword`: printable ASCII only, at least 20 characters
and 10 distinct, estimated at 128 bits or more) wherever an operator supplies a password: `sluisctl recovery verifier`
and the hub that derives its verifier from `recovery.passwordSecret`.

**The username and the password are used as the person typed them, except the username's case.** The server spells the
username (lower case, spaces trimmed) and returns it with the salt; the page uses that spelling in `x` and in `M1`, so
`Recovery` signs in and burns no refusal. The password is never trimmed or normalised, by the page, the hub or
`sluisctl` (which removes the trailing line ending of its standard input and nothing else; a file or environment
source removes the trailing newline it always has).

**Test vectors.** Package `srp` takes the hash, the group and the rounds as parameters (none is RFC 5054's own `x`), so
RFC 5054 Appendix B (`alice`, `password123`, the 1024-bit group, SHA-1) runs through the code the default runs through:
`k`, `x`, `v`, `A`, `B`, `u` and the premaster secret match the RFC byte for byte, and the exchange goes on through
`Server.Verify`. A pinned known-answer vector for the default parameters (`srp/testdata/default-vector.json`: `x` with
PBKDF2, `v`, `A`, `B`, `u`, `S`, `K`, `M1`, `M2` for fixed secrets) was computed independently in Python and is checked
in Go and in the page's script. A Go↔JS test also signs in with the script against the Go server, with a mixed-case
username.

**The library.** `srp` is written here (about 250 lines), not taken from `github.com/1Password/srp`, which is maintained
but cannot meet the interop the owner asked for: it fixes SHA-256, derives its session key from the hexadecimal text of
the premaster secret, builds `M1` from unpadded values and has its own `x`, so no RFC 5054 vector can pass through it,
and the browser would have to copy its conventions. It is a public package because the Pulumi library, which imports
nothing internal, computes verifiers with it.

**The browser half** is one self-hosted file, `/login/srp.js` (and `<console>/login/srp.js`): WebCrypto for SHA-256,
`BigInt` for the arithmetic, no dependency and no inline script, under the pages' `default-src 'self'`. The password
input has no `name`, so a browser that does not run the script submits nothing.

**Not stretched.** `x` is a fast hash, as RFC 5054 has it, so a stolen verifier can be attacked offline at the speed of
SHA-256. That is acceptable for the passwords here, which are generated, 40 characters of a 57-letter alphabet (over 200
bits). A memorable password does not belong behind this door, and the installation that supplies its own should make it
as long.

### 2. Two accounts, nothing else

The usernames are `recovery` and `conformance`. Any other username gets the same answer an account would, and fails.

- **`recovery`** keeps every rule it had. It completes as a ServiceAccount subject (`system:serviceaccount:<ns>:<name>`,
  from `recovery.serviceAccount` in the namespace the issuer runs in; `sluis:recovery` where there is none), so the
  policy's `service_account` matchers decide what it is in, as for a recovery token. Its sign-in records the method
  `recovery-password` (the token door's is `recovery`; both are recovery in every rule below), its tokens carry `acr`
  recovery, `amr` `["pwd"]` (RFC 8176; a recovery token's is `swk`) and `sluis_recovery`, it opens interactive sessions
  only, and its sign-in is recorded durably as `roster.recovery.signed_in` and refused without the record. At the
  console it is an operator by construction. The recovery switch of 0075 ends it everywhere.
- **`conformance`** is new: a test subject for the harness, with no privilege.
  - **Off by default.** `conformance.enabled` turns it on, in the installation document and the service document. A
    Lambda stack then also generates its password and verifier.
  - **Harness clients only.** The policy marks a client `harness: true` (there was no such mark; this adds it, refused
    on an exchange client). The account is offered on the sign-in page only for a request of such a client, a start for
    any other client gets the answer an unknown username gets, and the sign-in, every token and every refresh re-check
    the mark. The client's `requires` is not consulted: the account holds no group.
  - **Never an operator.** It completes as `system:serviceaccount:sluis:conformance`. The service **refuses to start**
    when the policy puts that subject in an operator group; the console never takes a browser signed in by it as a
    principal; the session service reads it as holding no group; and it cannot be a bearer or an exchange proof.
  - **Its own markers.** Method `conformance`, `acr` `urn:truvity:sluis:acr:conformance` (not offered in discovery),
    `amr` `["pwd","conformance"]`. The sign-in is recorded as the ordinary `roster.person.signed_in` with `how:
    conformance`; the catalogue's `how` is a free string, so the catalogue version is unchanged.
  - **Shut by the recovery switch as well.** The switch of 0075 is the one runtime control an operator has over every
    way in that skips the directory. An account that survived it would be a door the operator believes closed, and
    keeping the two together costs nothing: a conformance run is a deliberate act that repeats with a new
    `recovery.generation`. The switch does not turn the account on, and `recovery.enabled: false` does not turn it off
    (its own flag does). The sweep that follows the console's turn-off ends the account's sign-ins and sessions with
    recovery's.

### 3. The doors

- **The issuer's `/login`**, on Lambda and in a cluster alike, offers a password block when the recovery account is
  effective (recovery is not off, and a verifier is configured) and, for a harness client, when the conformance account
  is on. The routes are `GET /login/srp.js`, `POST /login/srp/start` and `POST /login/srp/finish`. A cluster that names
  no verifier keeps its recovery token door and gets nothing new.
- **The console's recovery sign-in** (`<console>/login`) is the recovery account only, by the same two routes.
- **No plaintext path remains.** The console's `PasswordRecovery` has no `Verify`: `/login/recovery` takes a token and
  answers `400` to a password in a form, a body or a bearer, without reading the body. The exchange takes JSON only.

### 4. State between the two requests

`start` takes the username and answers `{handle, username, salt, B, iterations}`; `finish` takes `{handle, A, M1}` and answers `{M2, next}`
and signs the browser in. The server's `b` is kept in the State port as `srp.<handle>` (a new key family of the oidc
module, layout 4 `issuer-srp`), under a 128-bit random handle, for **two minutes and one use**: `finish` reads the record
and deletes it at its revision, so of two containers asked at once one wins, and the record is spent before anything is
checked. The record holds a digest of the page's signed state, which the recovery cookie already pins to the browser; a
`finish` under another state spends the handle and fails. The record is never carried by a backup or written by a restore.

An unknown username, an account this request may not use (the recovery switch, a client that is not a harness's, the
flag off) and a username longer than 64 bytes get a **decoy** salt and `B` derived from the username and a key every
container shares (an HMAC of the sign-in state key): the same on every ask, the same shape as a real one, and a `finish`
that does the same work and ends the way a wrong password does. Refusals are counted exactly as before, per source and
overall (5 and 100 at the issuer, 10 and 100 at the console, a minute), and a blocked source is also refused at `start`.
Starts are counted separately (30 and 600) because each costs a State write. **Only a refusal that ran the proof check
counts against the ceiling across all sources**; a finish naming a handle that is unknown, spent or expired does no
work and counts against its source alone, so about twenty sources cannot use up the ceiling and lock recovery out. The
decoy verifier of a username is cached (a thousand names, then made each time), so a repeated ask costs what a real
start does. The residual: the first ask for a name costs one more modular exponentiation, which a precise clock could
in principle see; the accounts' names are fixed and documented, so there is little to learn.

### 5. Verifier custody

The issuer reads `{salt, verifier}` and never the password.

| | layout v5 | v4 and v4+v5 |
|---|---|---|
| recovery verifier | `internal/oidc/recovery-verifier`, written by the Pulumi library | none: the service derives it from the password it already reads (`recovery.passwordSecret`), in memory |
| conformance | `internal/oidc/conformance-password` (the operator's) and `.../conformance-verifier`, when `conformance.enabled` | needs `conformance.verifierSecret`; the stack does not make it |

The value is the secret store's document of `{"salt":"<hex>","verifier":"<hex>","iterations":600000}`. The library
computes it in the program from the generated password under a salt of its own: a `RandomBytes` of 32 bytes whose
keeper is the password, so it is stable across applies and replaced when the password is, and an apply that does not
rotate the password rewrites nothing. Where the hub derives the verifier itself (layouts v4 and v4+v5, a file), the
salt is an HMAC of the session key the containers share, which is the same everywhere and tells nothing of the
password. `sluisctl recovery verifier` makes a fresh random salt each run, for an installation that is not a Pulumi
stack, and `sluisctl recovery password --account conformance` reads the generated password.

On **layout v5** the renderer no longer names `recovery.passwordSecret` and names `recovery.verifierSecret`, and the
issuer's role is **denied** the two password parameters, in its policy (`SluisDenyPasswords`, with the decryption of
their values) and in the parameter key's (`SluisKeyDenyPasswords`). A deny is needed because the role holds the whole
`internal/oidc/` namespace. Read-all roles (backup) keep what they had. The IAM matrix pins both halves. On
**layouts v4 and v4+v5** the deny is cosmetic: the hub there reads the v4 password to derive its verifier, so the
role keeps the v4 parameter and the v5 password parameters, which nothing reads, are denied for nothing. The
guarantee that the issuer never reads the password is a layout v5 one.

### 6. The harness

`hack/conformance_drive.py` still drives headless Chrome; the page's script does the SRP. It signs in as `conformance`
with the password `RECOVERY_PASSWORD_CMD` prints (`sluisctl recovery password --account conformance` on Lambda). The
clients of a run carry `harness: true`, and `requires` is no longer what lets the account in.

## Consequences

- **Consumers.** New optional keys: `recovery.verifierSecret`, `conformance.enabled`, `conformance.verifierSecret` and the
  client key `harness`. A Lambda stack on layout v5 gains two parameters (three with conformance), two outputs
  (`RecoveryVerifierParameter`, `ConformancePasswordParameter`) and the deny. The service and the library must be the
  same version. **The recovery account at the issuer needs a policy row**: a `service_account` matcher for
  `sluis/recovery` (or the named account) in the group the console client requires, which the renderer's bootstrap row
  already writes in a cluster and which a Lambda installation did not need until now. The console's own
  `<console>/login` recovery is unchanged for the operator, who still reads the password with `sluisctl`.
- **The issuer's `/login` shows a chooser.** With the recovery account's door on, which on Lambda is the default, the
  page offers the provider button and a collapsed *Password sign-in*, where it used to forward at once to the only
  provider. A cluster with recovery already behaved so.
- **The plaintext password path is removed**, including the one-line `curl` runbook with a bearer. The token door for
  clusters keeps its bearer: a short-lived token is not a password.
- **A script is required.** There is no sign-in with the password without the page's script, by design.
- **The conformance clients need `harness` in the sluis policy.** In gitops `harness` already means something else:
  a client with no portal card whose secret is not put in the cluster. The sluis policy's `harness: true` is a third
  meaning (a client the conformance account may sign in to), and gitops does not pass `harness` into the sluis policy
  yet. Until it does, an estate that renders its policy from gitops cannot mark a client for the account: the gitops
  renderer must emit `harness: true` into the sluis client for the clients it calls harness.
- **What it does not cover.** A stolen verifier is an offline target at 600000 hashes a guess (section 1). The page's
  arithmetic and the server's are not constant-time, and the first ask for an unknown name costs a little more than
  a real start. The conformance password is as
  sensitive as a test account's; turn the account off when the run is over.
- **Residuals, stated.**
  - **Lockout by many sources.** The refusal counts are per container. About twenty sources at the issuer (ten at the
    console), each spending its share on wrong proofs that ran the check, can hold that container's ceiling for a
    minute and refuse the operator for that minute. This is how recovery was throttled before; section 4 removed only
    the cheap way to do it (handles that do no work).
  - **The decoy cache fills.** It holds 1024 names and evicts none, so after that many distinct names the first ask for
    a new one costs a modular exponentiation again and the timing difference of section 4 returns. Low value: the
    accounts' names are fixed and public.
  - **Raising the rounds.** A decoy announces `Params.Iterations()`, the current default, and a stored credential
    announces the rounds it was made with. After the default is raised, an account made at the old number differs
    from a decoy in that field. Re-derive stored credentials (a new apply, a new `sluisctl recovery verifier`) when
    raising it.
  - **Session-key rotation on layouts v4 and v4+v5.** The hub derives its salt from the session key (section 5). A
    container that has the new key and one that has the old serve different salts for up to `lazy.TTL` (30 s), so a
    sign-in whose two requests straddle the change, or reach the two containers, fails once. Retry.
  - **A weak password is found at start.** The hub that derives its verifier checks the password then: an ERROR in the
    log and a readiness that fails, and every sign-in is refused until the password is replaced.

## Alternatives considered

- **`github.com/1Password/srp`.** Maintained, but its conventions are not RFC 5054's (section 1), so the owner's
  instruction could not be checked against the RFC's vectors.
- **Keep the form, hash in the page.** A hashed password is the password: whoever sees the request replays it.
- **OPAQUE (RFC 9807).** Stronger against a stolen verifier, and not what the owner chose; there is no maintained
  browser implementation to self-host without a bundler.
- **A conformance run as the recovery account.** What the harness did on a cluster. It makes the test subject an
  operator, and on Lambda the issuer had no password door to sign it in.
- **A conformance account outside the switch.** Rejected in section 2.
- **A new audit action for conformance.** A sign-in that opens no privilege is what `roster.person.signed_in` records;
  a second action would bump the catalogue to say what `how` already says.
- **Denying the password in v4 too.** The v4 service derives the verifier from it; the deny comes with layout v5.
