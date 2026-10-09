# Connect a corporate directory

Let sluis read a Google Workspace tenant's users, groups and domains. Create the OAuth client once, then consent per tenant. Microsoft Entra has no backend ([extending sluis](../extend.md)).

## Before you start

- Enable the Admin SDK API first, or the first read is refused after consent.

- Publish the consent screen (*In production*). In Testing a second tenant gets `Error 403: access_denied` and refresh tokens expire after seven days.

- Consent as a Super Admin. The token dies with that account; a narrower role fails with a 403.

Every credential reads these four read-only scopes. The domain scope also feeds [Slack domain discovery](../../../concepts/sluis/slack-pass.md).

```text
https://www.googleapis.com/auth/admin.directory.user.readonly
https://www.googleapis.com/auth/admin.directory.group.readonly
https://www.googleapis.com/auth/admin.directory.group.member.readonly
https://www.googleapis.com/auth/admin.directory.domain.readonly
```

## 1. Create the project and consent screen

Create a project and enable **Admin SDK API**. In Google Auth Platform, set the audience to **External**, press *Publish app*, and add the four scopes under Data access. Admins click through the unverified-app warning. A tenant that blocks unconfigured apps must trust the client id.

## 2. Create the OAuth client

Create a **Web application** client with both redirect URIs:

```text
https://<host>/connect/google/callback    # an administrator granting read access; demands an operator
https://<host>/login/google/callback      # a person signing in
```

## 3. Hand the client to sluis

Create a Secret with keys `client-id` and `client-secret`; the console shows the client read-only. Use `idKey` and `secretKey` for other names.

```yaml
config:
  oauthClient:
    secretName: google-oauth-client
    idFile: /var/run/sluis/oauth-client/client-id
    secretFile: /var/run/sluis/oauth-client/client-secret
secretMounts:
  - { secretName: google-oauth-client, mountPath: /var/run/sluis/oauth-client }
```

## 4. Connect a tenant

An operator presses **Connect Google Workspace**, and a Super Admin signs in and consents. A refused first read usually means the Admin SDK API is off.

Then choose domains. The admin's domain is preselected, and *all, including ones added later* is an option. Unselected domains are never read. *First snapshot pending* shows for up to two minutes.

## Alternative: a service-account key

Create a service account and JSON key with **domain-wide delegation** for the four scopes. Choose **Upload key** on the console, or declare it in values (read-only there; remove it to disconnect):

```yaml
directory:
  workspaces:
    - id: C0example              # the backend's tenant id (Google: the customer id)
      backend: google
      admin: admin@example.com   # the account the key impersonates
      secretName: example-sa-key # a Secret in this namespace, key.json holding the JSON key
      serve: [example.com]       # optional; omitted serves every domain the tenant owns
```

All values, including `syncGroups`, are in [declared workspaces](../../../reference/sluis/declared-workspaces.md).

## Verify

The workspace appears with its tenant id, and the served domains read *authoritative*.

## Roll back

- **Disconnect** revokes the token and deletes the credential and record; consumers get `in_domain=false`. Delete a service-account key yourself.

- **Reconnect** repairs a failed probe, for the same tenant only.

- To move a domain, connect the new tenant serving all domains, including later ones. The console shows the domain contested during the overlap.

The credential is in `Secret <release>-workspace-credentials`, part of the [backup set](../operate/back-up-and-restore.md); `directory.push` renders a recovery copy.
