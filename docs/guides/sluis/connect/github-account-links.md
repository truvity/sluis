# Link a GitHub account to a person

Link each GitHub account to a work address so the controller can place it. The controller's use of links is in [How a GitHub pass decides](../../../concepts/sluis/github-pass.md).

## Before you start

- Only the installation-wide operator creates or disconnects the link App.
- The link App must be public. A private App is authorised only by members of its owner organisation.
- A personal address links nothing, an unverified one proves nothing, and a suspended account's address is refused with the reason on the page.

## 1. Create the link App once

On the GitHub page, open the *Apps* tab, open the *Link App* and press **Create** under an organisation you own.

| | The link App | An organisation's App |
|---|---|---|
| Used by | each person, as themselves | the controller |
| Permission | read the person's own email addresses | `members: write`, `organization_administration: read` |
| Installed | nowhere | on its organisation |
| Visibility | public | private |
| Kept | client id and secret | private key |

## 2. Each person links once

Send people to `https://<issuer>/connect/github/link`. They press *Continue to GitHub* and authorise. No console role is needed.

The service links the account to every verified address the directory has live. Linking a second account with the same address moves the address to it, and the first account is lost.

## Verify

Every pass the controller re-reads the account's verified addresses:

| GitHub says | The link | The account |
|---|---|---|
| Addresses still verified | linked | stays |
| Some linked addresses gone or unverified | narrowed | stays, by what remains |
| Every linked address gone or unverified | lost | leaves the organisation at once |
| Authorization revoked, or account gone | lost | leaves at once |
| Outage, timeout or rate limit | unchanged | nothing happens |
| Token pair lost in an interrupted renewal | unverifiable | nothing happens; the person links again |

GitHub kills the old token pair on every renewal. The controller records a renewal as in progress before making it, so an interrupted one reads as unverifiable, never as revoked. A link is checked only with the credentials of the App that issued it. A lost owner link is reported.

## Link sources

| Source | Proof | Checked each pass | Removed when the address leaves GitHub |
|---|---|---|---|
| Linked by them | they authorised the link App | yes | yes |
| Public profile | the account publishes a verified work address; members only | no | no |
| Imported | `ImportGitHubLinks` once: approved, address live in the directory, account already a member | no | no |

All three count as the person. A profile match or an import never displaces a link the person made, and the person linking replaces it.

## Roll back

Disconnecting the link App makes every self-link unverifiable. Nobody is added or removed until each person links again. Profile matches and imports hold no token and stand.
