# 0042 — sluis syncs company-issued OpenPGP keys to staff GitHub accounts

**Status:** Proposed
**Layer:** add-on
**Date:** 2026-11-21

Proposal: [issue #1488](https://github.com/truvity/sluis/issues/1488).

## Context

Nothing in this record is built or scheduled: the work is in the backlog ([roadmap](/roadmap/)).

A company that issues OpenPGP keys to its staff wants the commits of those staff to show as Verified on GitHub. GitHub marks a commit Verified only when the signing key is on the author's account, and its copy of a key changes only when the key is uploaded again. A key uploaded by hand therefore goes stale at every renewal without anyone noticing, and a leaver's key stays on the account until someone remembers it.

[0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/) gives each person one OpenPGP certificate per company: a primary that sluis holds sealed, and subkeys born on the person's card. It binds the subkeys to the person, renews the certification, and ends a binding when a card is lost or the person leaves. The staff keyring is produced from those bindings. This record decides how that certificate reaches the person's own GitHub account and leaves it again.

GitHub's documentation gives these facts, which the design relies on:

- A GitHub App can act as a user. The user authorises it, and the token it gets "only has permissions that both the user and the app have". The web application flow supports PKCE with `S256` only ([user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)).
- The user token expires after eight hours and the refresh token after six months. A refresh returns a new pair, and the old pair stops working ([refreshing user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens)). A refresh token that has expired means the person goes through the flow again.
- The account permission "GPG keys" covers four calls on the authenticated user: `GET /user/gpg_keys` and `GET /user/gpg_keys/{id}` need read, `POST /user/gpg_keys` and `DELETE /user/gpg_keys/{id}` need write ([permissions](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps)). No call changes a key in place ([GPG keys](https://docs.github.com/en/rest/users/gpg-keys)).
- When a user revokes the authorisation, GitHub sends the `github_app_authorization` event to the App, with no way to unsubscribe. Revoking does not uninstall the App, and a call with the revoked token gets `401` ([webhook events](https://docs.github.com/en/webhooks/webhook-events-and-payloads#github_app_authorization)).
- Vigilant mode is a setting of the person's own account. The documentation mentions no API and no organisation-level setting ([verification statuses](https://docs.github.com/en/authentication/managing-commit-signature-verification/displaying-verification-statuses-for-all-of-your-commits)).
- GitHub checks a signature at push time and keeps the result, so a later removal or revocation of a key does not change an old commit ([persistent verification](https://github.blog/changelog/2024-12-10-persistent-commit-signature-verification-is-generally-available/)). Whether expiry behaves the same is not confirmed there.

## Decision

### The App

- **One App does one job: it syncs company-issued OpenPGP keys.** It is the key App, a second public App beside the link App, and a `GitHubApp` of the operator's own tenant ([GitHub](/integrations/github/)). It is public for the reason the link App is: staff accounts are personal, and a private App can be authorised only by members of its owner. Anyone may authorise it, and it does nothing for a person whom the directory does not know.
- **Its only permission is the account permission "GPG keys: read and write".** It has no repository, organisation or other account permission, it is installed nowhere, and it keeps GitHub's expiring user tokens on. It asks for no SSH-key permission, because staff signing keys are card-born OpenPGP keys and there is no SSH-signing fallback.
- **sluis makes it with the manifest flow** of [Connecting](/integrations/connecting/), as for every App. The manifest names the callback URL of `adapter-github` and an active webhook for one event, `github_app_authorization`, with a webhook secret that stays sealed in the `GitHubApp`.
- **`adapter-github` is the only holder.** It opens the App's client secret from its `GitHubApp` per call, as it does for the other Apps ([0025](/decisions/0025-integrations/)), and for this job it calls `api.github.com` and nothing else.

### Joining

- **Each person authorises the App once, as part of joining.** The console shows the joining steps ([0027](/decisions/0027-the-console/)). After the person has signed in and the directory has them as live, the step "authorise the key App" starts the web application flow: a state bound to the person's session, an S256 PKCE challenge, and a redirect to GitHub. The state is a temp item that lives at most one hour, like the other manifest and OAuth states.
- **The first account is pinned.** `adapter-github` exchanges the code, reads the authenticated user and stores the GitHub user id. A later authorisation from another account is refused until an operator releases the pin, so a person cannot move their company keys to a colleague's account or a stranger's.
- **The person verifies their company address on the account.** GitHub shows a signature as Verified only when the commit address is a user ID of the key and a verified address of the account. The joining page tells the person this; sluis cannot do it for them.

### Storing and refreshing the user token

- **The tokens are one durable item per person,** the kind `GitHubKeyAuthorization`, sealed, personal and written by `adapter-github` ([#1214](https://github.com/truvity/sluis/issues/1214)). It holds a version, the GitHub user id, the user token and its expiry, the refresh token and its expiry, a state (`active`, `refresh-lost`, `revoked`, `lapsed` or `left`) and the ids of the GitHub key records that sluis made. Every write is conditional on the version the writer read. It is sealed under the tenant's `data` key, like every credential an adapter uses, and is erased with the person ([0004](/decisions/0004-the-data-model/)).
- **The adapter opens the item per call and keeps nothing** ([0025](/decisions/0025-integrations/)). A token is never in an audit record, a log line, a span or a console page.
- **A refresh is made under the person's lease and written before it is used.** A refresh token works once, so two processes refreshing together would burn the pair. The adapter takes a lease named for the person ([0026](/decisions/0026-reconcilers/)), refreshes only when the user token has less than 5 minutes left, and writes the returned pair with a conditional write before the first call that uses it. If that write fails, the pair is lost, because GitHub has already made the old pair useless.
- **A lost pair is a state of its own, `refresh-lost`, and a routine one.** A process that crashes or loses its lease between GitHub's answer to a refresh and the write loses the pair. A refresh is due about every 8 hours for each person, so this will happen. It is not a revocation: the person did nothing, and the alert says so. The adapter marks the item `refresh-lost` when a refresh was begun and its answer was never written, or when a refresh is refused after one was begun. It raises an operator task "ask the person to authorise again", and the person's console page shows the same step. Until the person does, nothing is added or removed for them.
- **A leaver in `refresh-lost` has no removal path through sluis,** because the pass has no token. The operator removes the person from the organisation and asks the person to remove the key or to authorise again, so that the next pass can remove it. The company key stays on the account until one of the two happens. Old commits stay Verified in any case.
- **A pass refreshes often enough that the six months never run out.** Every full pass lists the account's keys, so a pass refreshes at least once a day, and each refresh starts a new six-month refresh token. The refresh token lapses only when sluis has made no pass for six months. The state is then `lapsed`, and the person is asked to authorise again.

### The sync loop

- **The source of truth is sluis's own binding records.** A binding ties one person to one certificate and its subkeys; it is `pending`, `active`, `ended` or `revoked`, and only an `active` binding of class `hardware-attested` is in the keyring and the sync ([0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/), [REQ-KEYS-154](/requirements/keys/#REQ-KEYS-154)). `adapter-github` derives the staff keyring from the active bindings of the tenant, read through its data port. No keyring file is read from outside sluis, so there is nothing to sign, pin or fetch, and no format to agree with another product.
- **One certificate per person per company is on the account.** It is the person's primary for that company with all its current subkeys and their binding signatures. A second card adds a subkey, and a lost or retired card ends one, so the certificate changes and is replaced on GitHub. A new primary for the company is a new certificate, and the same pass removes the old one.
- **Customers are not synced.** Only a tenant's staff are. The people of a customer tenant get no key on GitHub, and the adapter makes no call for them.
- **The desired state of a person is the certificate of their active binding.** The binding step proved the link between the person and the certificate, and the adapter relies on that proof and does not repeat it. The adapter asks core nothing and only executes what the binding records say; who may hold a binding is decided where bindings are made ([0025](/decisions/0025-integrations/)).
- **Two controls stand between a binding record and an account.**
  1. **The person-to-certificate link.** The adapter adds a certificate only to the pinned GitHub account of the person whose active binding holds it. A wrong or tampered binding of one person cannot put its certificate on another person's account.
  2. **Active bindings only.** A certificate is added only while its binding is active and `hardware-attested`. A binding that is `pending`, ended, lapsed or revoked adds nothing, and the adapter adds only a primary that carries the company's certification ([0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/)).

  A compromised or wrong binding record can still name a certificate for its own person. The limit is stated, and the later CI check, which reads the same bindings, does not catch it. The limits below bound the damage: one certificate per person per company, a bounded size, a bounded number of changes per pass, and a breaker.
- **Three actions, for company fingerprints only.**

  | Action | When | How |
  |---|---|---|
  | Add | The desired certificate is not on the account | `POST /user/gpg_keys` with the armoured certificate and a name that holds the fingerprint |
  | Update | The account holds the primary, and the binding has a subkey, user ID, binding signature or expiry that GitHub's copy lacks | GitHub cannot change a key in place, so add the new copy, then delete the old record, only if its id differs from the id the add returned |
  | Remove | The binding has ended, or the person has left | `DELETE /user/gpg_keys/{id}` |

  If GitHub answers a second upload of a held primary with the existing record, deleting the old record would remove the only copy. So the pass deletes the old record only when its id differs from the id that the add returned, then lists the account again and checks that the fingerprint is still there. If GitHub refuses a second upload, an update is delete then add in one pass, and a commit pushed in the gap stays Unverified for ever, because GitHub never checks again. Which of the two GitHub does is measured before the build (see Follow-ups).
- **A key is identified by its full fingerprint, computed from the key itself.** GitHub's key object has no fingerprint field. The adapter takes the armoured public key that GitHub returns (`raw_key`), parses it, and computes the full version 4 fingerprint of the primary. It never matches on `key_id`, on the name, or on an email of the key object, because a short key id can collide and a name is chosen by whoever uploaded the key. A key that cannot be parsed is personal and is left alone.
- **sluis touches only company keys.** A company key is a primary whose GitHub record sluis made (the ids in the item), or a primary whose binding sluis held and has ended. Removal covers only these, so a binding record that names a fingerprint sluis never added cannot make the adapter delete it. Every other key on the account is personal. It is read only to list it, and never changed or removed, even when its user ID carries a company address.
- **The pass is a reconciler's pass without being a reconciler.** [0026](/decisions/0026-reconcilers/) limits a reconciler to membership, and this job manages keys, so it is a job of `adapter-github` of its own. It keeps the rules of 0026 that apply: one pass per person under a lease, level-triggered and idempotent, a dry run until the operator enables it, and a breaker.
- **The breaker is for mass removals and never holds a leaver.** It holds a pass that would remove more than half of the company keys of all people, and a pass that would remove more than half of one person's keys when the cause is not explicit. A removal for a person the directory marks `left`, and a removal of a fingerprint that the binding record withdrew, are exempt: a leaver loses their one certificate at once.
- **Limits bound one pass.** A person holds at most one company certificate per company on GitHub, a certificate is at most 16 KiB, and a pass makes at most 8 changes to one account. These figures are starting values, not measurements, and are tunables of the target.
- **The pass runs** when a binding changes, when the directory changes the person, when the webhook arrives, and in a full pass at least once a day.
- **An unreadable binding store never removes anything.** A binding record that cannot be read, or whose read is older than its time to live, is unreadable, not empty. The pass adds nothing from it, removes nothing because of it, and reports it ([0026](/decisions/0026-reconcilers/)).

### Audit

`adapter-github` emits its audit records through [audit's emitter](https://github.com/truvity/audit/blob/master/docs/decisions/0002-pieces-and-holders.md), as every adapter does ([0025](/decisions/0025-integrations/)). The records of this job are:

- **A pass:** the person's pseudonym, the digest of the bindings it read, counts, and the mode (dry run or apply).
- **Each add, update and remove:** the fingerprint, the GitHub key id, and the outcome (`done`, `held`, `refused` or `failed`) with a reason from the closed list of [0025](/decisions/0025-integrations/). A fingerprint is public and may be written.
- **The life of the authorisation:** granted, pinned, refreshed, lost, lapsed, revoked, left, deleted, an event that was dropped and why, and a binding that was refused and why.
- **Never recorded:** a token, a client secret, or the armoured key text.

### Revoked authorisation and leavers

- **An event is a hint, and a call confirms it.** The `github_app_authorization` event reaches `entry`, which routes it to `adapter-github` ([0025](/decisions/0025-integrations/)). The adapter checks the `X-Hub-Signature-256` header against the App's webhook secret and drops a request that fails. It maps the event's `sender` to the pinned GitHub user id and drops an event for any other account. GitHub's page lists no `action` field for this event, so the adapter does not read one.
- **`revoked` is set only after a confirmation, by a conditional write.** On the event, or on a `401` or a refused refresh in a pass, the adapter takes the person's lease, reads the item, probes GitHub with the current token (after a refresh if one is due), and only if the probe fails marks the item `revoked`. The write is conditional on the version that was read. On a conflict the adapter reads again and decides again, because another process may have written a newer pair. A process that holds an older pair never deletes the newer one. A `revoked` item has both tokens deleted, an audit record, a notice on the person's console page ("authorise the key App again") and an alert to the operator.
- **A revoked authorisation leaves the keys where they are.** sluis can no longer reach the account, so it removes and adds nothing. The operator is told which fingerprints are on an account that sluis cannot manage.
- **A leaver's keys are removed while the authorisation lasts.** When the person's binding has ended or the directory marks the person `left`, the next pass removes the person's company keys, audits the outcome, revokes the grant at GitHub (`DELETE /applications/{client_id}/grant`) and deletes the tokens. The item moves to `left`. The event that follows the adapter's own revocation does not make the item `revoked` and raises no alert, because the item is already `left`. The item is not erased with the person until the outcome is audited or erasure reaches it.
- **A leaver whose authorisation is gone is covered by the rest of the leaver rule.** The operator removes the person from the organisation, the binding ends, sluis stops certifying, and the certification lapses ([0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/)). The later CI check below is what then refuses a commit signed with the ended key. A leaver whose pair was lost is the case under "Storing and refreshing the user token".
- **No key is revoked at GitHub for a leaver, and old commits stay Verified.** The adapter revokes the App's grant, not a key. A key is revoked only on a compromise, as the binding record decides ([0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/)); its binding then ends and this job removes it.

### Why Verified is not company enforcement

GitHub's Verified badge says that a signature verifies against some key on the author's account and that the address is verified. It says nothing about who issued the key, whether it lives on a card, or whether the person still works for the company. Anyone can add any key to their own account, and this job leaves personal keys in place on purpose, so a personal key still produces Verified commits. GitHub also keeps the result for ever. Syncing company keys makes the company's keys reliable; it does not stop other keys.

Enforcement is a later check. A required status check reads the signing key of each commit in a pull request and compares its fingerprint with the staff keyring that sluis produces from its bindings; a commit signed by a key outside the keyring fails the check. It reads the same bindings that this job reads, and it has to allow the keys GitHub itself uses to sign merges. It is not built or specified here. Until it exists, "cards only" is written policy, and no ruleset requires signed commits for this reason.

### Vigilant mode

Vigilant mode is a per-user setting that marks unsigned commits Unverified. Only the person can switch it on, and sluis can neither set it nor read it. It is a step of joining that the person confirms, with the settings path in the instructions. Nothing in the sync depends on it.

## Consequences

- A staff member's signing key arrives on their account without a manual upload, follows each renewal, and leaves with them. The most common failure, a key that went stale unnoticed, becomes an alert.
- sluis holds, for each person, a credential that reaches a personal account. It is sealed, opened per call and limited to GPG keys, but the write permission is the sharp part: whoever held a person's tokens could add a key of their own and sign commits as that person. The adapter therefore uploads only the certificate of an active binding of that person, through a code path that takes no other input, calls only `api.github.com`, and audits every add. The person-to-certificate link and the active-binding rule bound a wrong or compromised binding record; the later CI check reads the same bindings and does not catch it.
- The App is public, so anyone can authorise it. It holds nothing for a person the directory does not know, and an authorisation from an unknown account is deleted.
- A person who revokes the authorisation, whose pair was lost, or whose account sluis cannot reach, is not managed. The cost is an alert and a manual step, not a hidden gap. A lost pair is routine, and its alert says so.
- The sync depends on sluis's binding records ([0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/)). It needs from them a person, a certificate, an active or ended state and the time of each change, and nothing else.
- The requirements are [REQ-OPS-099](/requirements/ops/#REQ-OPS-099) to [REQ-OPS-129](/requirements/ops/#REQ-OPS-129). The [GitHub](/integrations/github/) page describes the job.

## Roads not taken

- **The account holds exactly the company keys.** It would delete a person's personal signing keys from their personal account, which is not the company's to do, and would break their personal projects.
- **No App; people upload by hand.** It is the state today: keys go stale at every renewal and a leaver's key stays until someone remembers it.
- **Enterprise Managed Users now.** It changes the ownership of every staff account and its reach on other repositories, which is a larger move than this sync, and it can come later without changing this design.
- **ritueel owns the App.** ritueel is a command-line tool that runs when a person is present and holds nothing online, while the sync needs a service that refreshes tokens and reacts to events.
- **A personal access token that the person gives sluis.** It is long-lived, is not bound to the one permission of this App, and expires when the person forgets it; the App's token is narrow and expires in eight hours.
- **Ask the person to authorise again every eight hours.** It removes the stored refresh token and with it the automation.
- **A GitHub organisation call that adds a key to a member's account.** The key calls act on the authenticated user only, so no organisation-level path exists to use.
- **Sync from a keyring file that another product builds and signs.** It adds a format, a signature, a publisher pin and a transport between two products, and a source that sluis does not hold; sluis produces the keyring from its own bindings, so there is nothing external to compromise.
- **Make the sync a reconciler of 0026.** A reconciler manages membership only, and widening it for keys would blur the one rule that keeps its safety rules simple.

## Follow-ups

- Build the key App, the kind `GitHubKeyAuthorization`, the sync and the audit records: [REQ-OPS-099](/requirements/ops/#REQ-OPS-099) to [REQ-OPS-129](/requirements/ops/#REQ-OPS-129) ([#1214](https://github.com/truvity/sluis/issues/1214)), after the measurements in the next item.
- Measure against GitHub before the build, and date the result: whether a second upload of a held primary is accepted and what it answers; the fields of the key object (`raw_key`, and that it has no fingerprint); what a key with an address the account has not verified shows; the answer to a revoked token, to a refresh with a revoked grant, and to the refresh token in its sixth month; the fields of the `github_app_authorization` event; whether expiry changes a stored verification; the name of the permission in the manifest; and the rate limits for a full pass of the staff ([#1718](https://github.com/truvity/sluis/issues/1718)).
- Open: the CI check that compares each commit's signing key with the staff keyring. It is designed in its own record when it is built ([#1718](https://github.com/truvity/sluis/issues/1718)).
- Open: an App for Enterprise Managed Users, designed in its own record if the company moves its accounts ([#1718](https://github.com/truvity/sluis/issues/1718)).
