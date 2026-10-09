# What is a workspace?

A **workspace** is one connected corporate directory.

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

The console calls a workspace a **directory** and its groups **directory groups**. The model, the URLs and the API keep the word workspace.

## Domains are discovered

After connecting, sluis reads the tenant's domain list and re-reads it on every probe. A domain that moves between tenants follows automatically, because a backend never lets one domain belong to two tenants at once.

If two connected workspaces claim one domain, neither is authoritative for it until the conflict clears, and the console says so.

## Routing is by email domain

The workspace serving an address's domain answers every request that names the address. An address in no served domain gets `in_domain=false`: no opinion, never "gone".

## Serving is narrower than owning

A workspace answers for every domain its tenant owns unless `serve` narrows it. A domain left out is still discovered and shown, but it routes nothing and its accounts are not kept.

You can name only domains that discovery returned, so every setting subtracts from the directory's own verified list. A tenant that owns a domain another tenant serves is no conflict, so overlapping directories can coexist.

## Authoritative is per domain

A domain is authoritative when its workspace's last probe succeeded within the freshness window and no other workspace serves it. Consumers that remove access act only on authoritative answers.

## A failure is an error, never an empty answer

A failure to reach a directory is an error. An identity keeps its last-known groups for a bounded time only while "I could not ask" differs from "the directory says nothing".

The same window applies to the console. While a directory cannot be reached, a person it last admitted keeps console access for the window, 4 hours by default. The sign-in ends after it. A person the directory reports suspended or not found is refused at once and the sign-in ends ([sessions](sessions.md#what-the-console-asks-of-the-sso-session)).

The service keeps the groups, the flat membership and each account's live flag as one snapshot per workspace. See [freshness](freshness.md).
