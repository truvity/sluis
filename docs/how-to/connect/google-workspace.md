# Connect a Google Workspace

## Purpose

Let sluis read a Google Workspace tenant's users, groups and domains: once per installation (the OAuth client), then once
per workspace (consent and the domains to serve).

## Preconditions

- Rights to create a Google Cloud project and an OAuth client for the installation, about fifteen minutes.
- The installation's public host `<host>`, reachable from the administrators' browsers.
- An **operator** of sluis, and a **Super Admin** role account in each tenant to connect.
- Permission to create a Secret in the service's namespace.

## Before you start

- **Enable the Admin SDK API on the project.** Missing it fails late: the consent screen appears, the administrator
  grants it, and the *first read* is refused. The console says so on the consent page, quoting Google's message, which
  names the project and links the page that enables the API.
- **Publish the consent screen (In production).** In Testing, only the listed test users can consent: the first tenant
  connects and the second is refused with `Error 403: access_denied` ("can only be accessed by developer-approved
  testers") before the request reaches sluis. In Testing, refresh tokens also expire after seven days, so a workspace
  that connected fine goes stale a week later without a word. Publishing is not verification: the four scopes are
  "sensitive", not "restricted", so Google needs no review.
- **Register both redirect URIs.** A client missing the second works until somebody tries to sign in.
- **Consent as a role account, a Super Admin.** The refresh token acts as whoever consents and dies with that account.
  Reading a customer's domain list is a super-admin act in Google; a narrower role fails as a 403 on the first read, not
  at consent. The token stays bounded by the four read-only scopes: users, groups, memberships and domains, nothing else.
- **Never change the client from the console.** It shows the client read-only; a credential a console can change is one
  somebody can change from a browser.

## Steps

### 1. Create the Google Cloud project and enable the Admin SDK API

**Run**: a project owned by the installation, any name; it holds exactly one OAuth client. Enable **Admin SDK API**.

**Expect**: the API shows as enabled.

**Verify**: the Admin SDK page of the project says *API enabled*.

**Rollback**: delete the project.

### 2. Configure the consent screen

**Run**: Google Auth Platform, Branding and Audience. Audience **External** (Internal accepts only the tenant that owns
the project; an installation serves several). Publishing status **In production**, and press *Publish app*. App name,
support email and developer contact identify the installation to the admins who consent. Under Data access add the
four scopes, all read-only:

- `https://www.googleapis.com/auth/admin.directory.user.readonly`
- `https://www.googleapis.com/auth/admin.directory.group.readonly`
- `https://www.googleapis.com/auth/admin.directory.group.member.readonly`
- `https://www.googleapis.com/auth/admin.directory.domain.readonly`

The domain scope is what makes domain discovery possible.

**Expect**: status *In production*; unverified, the consent screen shows "Google hasn't verified this app" and the admin
clicks *Advanced, Go to ... (unsafe)*.

**Verify**: the publishing status reads *In production*, not *Testing*.

**Rollback**: set the status back to Testing (existing tokens then expire in seven days).

Verification by Google is optional and only removes the interstitial (it needs a public homepage, a privacy policy and a
scope justification, and takes days). A tenant admin can instead mark the client id as trusted in the Workspace admin
console (Security, API controls, Manage third-party app access), which is required where the tenant blocks
unconfigured apps.

### 3. Create the OAuth client

**Run**: Clients, Create, Web application. Authorised redirect URIs, both:

- `https://<host>/connect/google/callback`: an administrator granting sluis read access to a company.
- `https://<host>/login/google/callback`: a person signing in.

They are separate because the endpoints have opposite authorisation: the first adopts a workspace and demands an
operator, the second is how a person becomes anyone at all. On a workstation the same pair on
`http://localhost:<port>` may sit on the client; remove them once it runs behind its real host.

**Expect**: Google shows a client id and secret.

**Verify**: both URIs are listed on the client.

**Rollback**: delete the client.

### 4. Hand the client to sluis

**Run**: create a Secret with keys `client-id` and `client-secret` in the service's namespace and set
`config.oauthClient.secretName` (with `idKey` and `secretKey` if the keys are called something else, and `idFile` and
`secretFile` where the Secret is mounted with `secretMounts`). The example is under
[Chart values](corporate-directory.md#chart-values).

**Expect**: the console shows the client read-only.

**Verify**: sign in on the console as a person of the installation.

**Rollback**: remove the `oauthClient` keys.

### 5. Connect a workspace

**Run**: an operator presses **Connect Google Workspace** on the console (nothing to fill in), lands on Google's
consent screen, signs in as the tenant's Super Admin role account, clicks through the interstitial and consents.

**Expect**: the browser returns to sluis, which records the consenting account, reads the tenant id and the domain
list and stores the workspace. A failure is shown on a page, quoting the directory's own message; a consent Google
granted and then refused on the first read is almost always the Admin SDK API not being enabled (step 1).

**Verify**: the workspace appears on the console with its tenant id and discovered domains.

**Rollback**: **Disconnect** on the workspace (revokes the token at Google and deletes the credential and the record).

### 6. Choose the domains

**Run**: the consenting administrator's own domain is pre-selected; the tenant's other domains are listed and off;
*all, including ones added later* is an explicit option. Pick what sluis should answer for. The rest stays discovered
and visible, but nothing routes to it and its accounts are never read.

**Expect**: the first snapshot runs in the background. The tenant's page shows *first snapshot pending* until it lands
(seconds for a small tenant, a minute or two for a large one), then the served domains turn authoritative.

**Verify**: the served domains read *authoritative*.

**Rollback**: change the domain selection; nothing is deleted by narrowing it.

### Alternative: a service-account key

For installations that prefer a robot identity or cannot publish an external consent screen. In the Google Cloud
project: a service account, a JSON key, and **domain-wide delegation** granted in the Workspace admin console to the
service account's client id for the same four scopes. Then on the console: **Upload key**, the JSON file and the admin
address to impersonate. Same record, different credential type. A declared (overlay) workspace is this path expressed in
chart values ([Chart values](corporate-directory.md#chart-values)).

## Afterwards

- **Reconnect** is the same button on an existing workspace. Use it when the probe fails: a revoked token, a suspended
  or deleted admin account, a changed tenant policy. The consenting tenant must be the same workspace; a different one
  is refused.
- **Disconnect** revokes the token at Google and deletes the credential and the record. The domains stop being served:
  consumers get `in_domain=false`, no opinion, for those addresses.
- **A domain moves** to another tenant: connect the new workspace and set it to serve **all of them, including ones
  added later**; on its next probe the old one stops listing the domain and the new one lists it, with no edit at the
  moment of the hand-over. A workspace narrowed to named domains will not pick the moved one up on its own, so add it
  there instead. During the overlap the domain is authoritative for neither; the console shows it contested.
- Consenting leaves a record in a ConfigMap the console shows, and the credential in
  `Secret <release>-workspace-credentials`, one key per workspace with a copy of the record beside it. That Secret is
  part of the backup set ([day two](../back-up-and-restore.md)): a deployment that
  copies it can put a lost namespace back without another visit to the cloud console. The connect and every disconnect
  are recorded in the audit trail.

Connecting a Slack workspace is a different flow: [Slack](slack-workspace.md). So is a GitHub organisation:
[GitHub](github-organisation.md).
