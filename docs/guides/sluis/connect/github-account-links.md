# Link a GitHub account to a person

GitHub tells an organisation outside its Enterprise Cloud plan nothing about which work address a member has. This page is how a person links their account, how the link App is set up once, and how links are checked. The controller's use of links is in [How a GitHub pass decides](../../../concepts/sluis/github-pass.md).

Not the App and not an owner can say it, so each person shows it themselves, once.

**Set up once.** On the GitHub page's *Apps* tab, open the *Link App* and press
**Create** on its page, under an organisation you own. The link App is
installation-wide: only the installation-wide operator creates or disconnects
it. It is a separate App from the
organisations', on purpose:

| | The link App | An organisation's App |
|---|---|---|
| used by | each person, authorizing it as themselves | the controller |
| permission | read the person's own email addresses | `members: write`, `organization_administration: read` |
| installed | nowhere | on its organisation |
| visibility | **public**: a private App can only be authorized by members of its owner organisation, which a new hire and a partner's engineer are not | private |
| kept | client id and secret | private key |

A person's token carries its App's permissions, so a token for the link
App reads one person's addresses and nothing else.

**What a person does.** They open the link page the GitHub page shows —
`https://<issuer>/connect/github/link` — press *Continue to GitHub*,
and authorize. No console role is needed: the proof is GitHub's. The
service reads the account and its **verified** addresses and links the
account to each address the directory has, live and vouched for. A
personal address is ignored; an unverified one proves nothing; a
suspended account's address is refused with the reason on the page.
Linking a second account with the same address moves the address to it,
and the first account, proving nothing, is lost.

**Checked every pass.** The link keeps the person's token pair. Every pass
the controller reads the account's verified addresses again:

| GitHub says | The link | The account |
|---|---|---|
| the addresses are still verified | linked | stays |
| a linked address is gone, or unverified, and others remain | narrowed | stays, by what remains |
| every linked address is gone or unverified | lost | **leaves the organisation at once** |
| the authorization was revoked, or the account is gone | lost | **leaves at once** |
| nothing — an outage, a timeout, a rate limit | unchanged | nothing happens |
| the token pair was lost in an interrupted renewal | unverifiable | nothing happens; the person links again |

GitHub rotates the pair on every renewal and kills the old one, so a
renewal is written as *in progress* before it is made and as done right
after; one found in progress on a later pass is never read as a
revocation. A link is only ever checked with the credentials of the App
that issued it.

An owner's link going lost is reported, like anything about an owner.

## Where a link comes from

| Source | Proof | Checked on GitHub every pass | Removed when the address leaves GitHub |
|---|---|---|---|
| **linked by them** | they authorized the link App; GitHub verified the address | yes | yes |
| **public profile** | the account publishes the work address; GitHub lets an account publish only a verified one. Matched automatically, members only | no | no — hiding an address is not removing it |
| **imported** | an approved pairing from elsewhere, handed to the operator RPC `ImportGitHubLinks` once — three checks each: approved, an address the directory has live, the account a member of a connected organisation | no | no |

All three count as the person: they are invited, moved between teams and
removed when the directory suspends them. A profile match or an import
happens only when the address is a live account the directory vouches
for and the account is a member already, and never displaces a link the
person made. The person linking themselves replaces it.

**Disconnecting the link App** makes every self-link unverifiable — nothing
can check their tokens any more — which adds and removes nobody until
each person links again. A profile match or an import holds no token of
the App's, and stands.
