# Failure semantics

| Situation | What consumers and operators see |
|---|---|
| Probe failed, snapshot still young | answers from the snapshot, `authoritative=false` |
| Snapshot older than the freshness window | same |
| Full refresh failed a page | old snapshot kept; nothing partial is ever served |
| Domain claimed by two workspaces | `authoritative=false` for that domain on both |
| Address in no served domain | `in_domain=false`: no opinion |
| Account missing from the snapshot | one live read first; `found=false` only after the backend said so |
| The state store is unreachable | the replica leaves readiness and says which dependency; sessions and snapshots are unavailable until it returns; liveness is unaffected, so nothing restarts |
| Credential revoked or admin suspended | probe fails, domain is provisional; reconnect is the recovery |
| A request would wait on the directory | it does not: the work runs detached and the answer is *first snapshot pending* |
| A policy the process refuses to load | the new pod does not start and the previous pods keep serving the previous policy |
| A GitHub pass fails | the last report with rows stands; the pass is retried next interval; nothing is removed on a failed read |
| The console answers a controller under another policy | the pass changes nothing and is tried again within seconds, a bounded number of times, before the interval resumes |
| A Slack workspace is not connected or not installed yet | that workspace reports a `waiting` pass with no error; nothing else is affected |
| A Slack pass cannot read the workspace whole, or the directory cannot be read | the report is kept with the failure on it; nothing is decided or changed on a partial read, and nobody is removed |
| A removal set is over half of a channel or of the workspace's managed members | nobody in that set is removed until an operator confirms that exact fingerprint (valid 24 hours; one confirmation covers every gate it fits) |
| A channel is defined in both git and the console | held on both sides, unchanged, until one definition is removed |
| The audit installation cannot be reached | records queue in the process; sign-ins are not refused, except a recovery sign-in, which fails closed |
| The whole installation is down | no new sign-ins; existing sessions and tokens live to expiry; [recovery](recovery.md) is by cluster proof |

The rule under all of them: **access is removed only on an authoritative answer.** Everything that can go wrong
degrades to *provisional*, never to "gone".

What operators do about each of these is in [day two](../how-to/day-two.md); the controllers' refusals are collected
in [safety](safety.md).
