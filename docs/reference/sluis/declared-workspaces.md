# Declared workspaces

`directory.workspaces[]` of the service document: the corporate directories a deployment declares, which the service
adopts at start. A workspace connected through the console is the other way in; the two are merged. Source:
`schemas/config/sluis.schema.json`.

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
| `backend` | yes | the implementation that reads the directory: `google` |
| `admin` | yes | the account the credential impersonates |
| `keySecret` | yes | the NAME of the service-account key, `directory/<id>/key` ([Secrets](secrets.md#the-names)). On Kubernetes the chart projects it from the Secret a `secrets` entry names (`{name: directory/C0example/key, secretName: example-sa-key, key: key.json}`); read on every use |
| `id` | no | the backend's tenant id; checked at adoption when given |
| `serve` | no | the domains of the tenant this installation serves. Empty serves every domain, including ones added later |
| `syncGroups` | no | the groups kept. Empty keeps every group in the served domains |

One that cannot be adopted stops the service.

## Behaviour

- **Declared and connected workspaces.** Declared ones are read-only in the console, cannot be disconnected there (remove
  them from the document), and win when a domain is claimed twice. Domains are discovered from the backend, as for a
  connected workspace.
- **`serve`.** Naming a subset leaves the other domains discovered and shown, but nothing routes to them and their
  accounts are never cached. A domain named that the tenant does not own routes nothing and is reported as no longer
  owned, so it is safe to declare a domain that is about to move between tenants.
- **`syncGroups`.** It narrows what is kept, not what is read: the service still lists the tenant's groups, because that
  list is what an operator chooses from, so the saving is in storage and attention, not in the directory's quota. Only a
  group the last read held may be named.
- **Connected through the console,** the same choice is made at connect time, before the first snapshot: the consenting
  administrator's own domain is pre-selected, the tenant's other domains are listed and off, and *all, including ones
  added later* is an explicit option. It can be changed afterwards on the directory's page.

To declare a workspace step by step: [Connect a corporate directory](../../guides/sluis/connect/corporate-directory.md).
