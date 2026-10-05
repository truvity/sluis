# The directory model

A **workspace** is one connected corporate directory:

```
Workspace {
  id           string      # the backend's tenant id (Google: customer id)
  backend      google | entra
  domains      []string    # DISCOVERED from the backend, re-read on every probe
  serve        []string    # OPTIONAL: which of those to answer for; empty = all
  admin        string      # the account the credential acts as
  credential   oauth-refresh-token | service-account-key
  connectedBy  string      # console identity that connected it
  connectedAt  time
  health       { probedAt, ok, error }
}
```

The console calls a workspace a **directory**, and the groups it holds **directory groups** (it said *provider*
before). The rail puts each under its own heading, *Identity* and *Access*, so the two sides read as mirrors; the
model, the URLs and the API keep the older word.

- **Domains are discovered, not typed.** After connecting, the tenant's domain list is read and re-read on every
  probe. A domain that moves between tenants follows automatically: the backend never lets one domain belong to two
  tenants at once, so the move is sequential. Should two connected workspaces ever claim one domain, neither is
  authoritative for it until the conflict clears, and the console says so.
- **Routing is by email domain.** Every request naming an address is answered by the workspace serving that domain.
  An address in no served domain gets `in_domain=false`: no opinion, never "gone".
- **Serving is narrower than owning.** A workspace answers for every domain its tenant owns unless it is narrowed to
  a subset. What is left out is still discovered and still shown, so an operator can tell "not our business" from
  "missing"; it routes nothing and its accounts are not kept.

  Three things fall out of it. A tenant that happens to own a domain another tenant serves is no longer a conflict,
  so two overlapping directories can coexist. The choice cannot grant anything, because the only domains that may be
  named are the ones discovery returned: the ceiling is the directory's own verified list, and every setting is a
  subtraction from it. And because the served list is intersected with discovery rather than trusted over it, a
  domain moving between tenants hands over on its own.
- **Authoritative is per domain.** A domain is authoritative when its workspace's last probe succeeded within the
  freshness window and no other workspace serves it too. Consumers that remove access act only on authoritative
  answers; this flag is the safety-critical part of the contract.

A failure to reach a directory is an error and never an empty answer. The hold window rests on that distinction: an
identity keeps its last-known groups for a bounded time only while "I could not ask" can be told from "the directory
says nothing".

The groups, the flat membership and the live flag of every account are kept as one snapshot per workspace:
[freshness](freshness.md) says how it is read and refreshed.
