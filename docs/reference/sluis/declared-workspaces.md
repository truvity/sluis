# Declared workspaces

`directory.workspaces[]` of the service document declares the corporate directories a deployment adopts at start. A workspace connected through the console is merged with them. Source: `schemas/config/sluis.schema.json`.

```yaml
directory:
  workspaces:
    - id: C0example                  # optional: the backend's tenant id (Google: the customer id); given, adoption checks it
      backend: google
      admin: admin@example.com       # the account the key impersonates
      keySecret: directory/C0example/key   # the NAME of the key's secret
      serve:                         # optional; omitted serves every domain it owns
        - example.com
      syncGroups:                    # optional; omitted keeps every group in them
        - platform@example.com
```

| Key | Required | Meaning |
|---|---|---|
| `backend` | yes | The implementation that reads the directory: `google` |
| `admin` | yes | The account the credential impersonates |
| `keySecret` | yes | The NAME of the service-account key, `directory/<id>/key` ([Secrets](secrets.md#the-names)). On Kubernetes the chart projects it from the Secret a `secrets` entry names: `{name: directory/C0example/key, secretName: example-sa-key, key: key.json}`. Read on every use |
| `id` | no | The backend's tenant id; checked at adoption when given |
| `serve` | no | The tenant domains this installation serves. Empty serves every domain, including ones added later |
| `syncGroups` | no | The groups kept. Empty keeps every group in the served domains |

A workspace that cannot be adopted stops the service. Steps: [Connect a corporate directory](../../guides/sluis/connect/google-workspace.md).

## Behaviour

| Topic | Behaviour |
|---|---|
| Declared and connected | Declared workspaces are read-only in the console and cannot be disconnected there; remove them from the document. They win when a domain is claimed twice. Domains are discovered from the backend |
| `serve` | A subset leaves other domains discovered and shown, but nothing routes to them and their accounts are never cached. A named domain the tenant does not own routes nothing and is reported as no longer owned |
| `syncGroups` | Narrows what is kept, not what is read: the service still lists the tenant's groups for the chooser. Only a group the last read held may be named |
| Connected in the console | The same choice is made before the first snapshot. The consenting administrator's domain is pre-selected, other domains are listed and off, and *all, including ones added later* is an option. Change it later on the directory's page |
