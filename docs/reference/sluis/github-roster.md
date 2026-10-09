# GitHub roster reference

The objects the GitHub controller and the console keep, and what the policy render refuses. The reasoning is in [How a GitHub pass decides](../../concepts/sluis/github-pass.md); to connect an organisation see [Connect a GitHub organisation](../../guides/sluis/connect/github-organisation.md).

## What connecting leaves behind

| Object | Holds | Read by |
|---|---|---|
| ConfigMap `<release>-github-orgs` | one record per organisation: the App's id and slug, where it is installed, when and by whom it was connected; beside them the operators' removal confirmations (`_confirm.<organisation>.json`) and requests for a pass (`_pass.<organisation>.json`) | the console; the controller, as a read-only mounted volume, for the change check and the pass requests |
| Secret `<release>-github-apps` | one credential per organisation: the App's id, its installation, its private key; and the link App's client id and secret under `_link.json` | the controller, as a mounted volume; this service to uninstall on Disconnect and to redeem a person's authorization |
| Secret `<release>-github-links` | one link per GitHub account (`<id>.json`): its login, the addresses it proves, its state, the person's token pair | this service, which writes a link; the controller, which rewrites it as it checks — the one Secret its Role may update, by name |
| Secret `<release>-github-runner-apps` | every runner App: its three keys once installed, its record beside them | this service, to find the installation and to uninstall; the deployment, copying the keys to its runners |
| ConfigMap `<release>-github-status` | the controller's report, one document per organisation | the console; the controller replaces its data |

All of them exist, empty, from the service's first start, so the
controller's volume always has a Secret behind it. Each credential
carries a copy of its record, so these Secrets are a whole backup:
put them back into an empty namespace and the next start rebuilds the
records ([configuration](../../guides/sluis/operate/back-up-and-restore.md)).
No copy of a key exists anywhere else — not in git, not in a password
manager — so a deployment that wants one copies these Secrets, for
example with an External Secrets `PushSecret` each. A person's link
token may have rotated since the copy; that person links again.

