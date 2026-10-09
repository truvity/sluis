# GitHub organisation reference

The objects the GitHub controller and the console keep for a connected organisation.
Mechanics: [How a GitHub pass decides](../../concepts/sluis/github-pass.md). Setup: [Connect a GitHub organisation](../../guides/sluis/connect/github-organisation.md).

## Objects

All five exist, empty, from the service's first start.

| Object | Holds | Read by |
|---|---|---|
| ConfigMap `<release>-github-orgs` | One record per organisation: the App's id and slug, where it is installed, when and by whom it was connected. Also removal confirmations (`_confirm.<organisation>.json`) and pass requests (`_pass.<organisation>.json`) | The console; the controller, as a read-only volume |
| Secret `<release>-github-apps` | One credential per organisation: App id, installation, private key. The link App's client id and secret under `_link.json` | The controller, as a volume; the service, to uninstall on Disconnect and redeem a person's authorization |
| Secret `<release>-github-links` | One link per GitHub account (`<id>.json`): login, proven addresses, state, the person's token pair | The service writes it; the controller rewrites it as it checks, the one Secret its Role may update, by name |
| Secret `<release>-github-runner-apps` | Every runner App: its three keys once installed, and its record | The service, to find the installation and uninstall; the deployment, to copy the keys to its runners |
| ConfigMap `<release>-github-status` | The controller's report, one document per organisation | The console; the controller replaces its data |

## Backup

Each credential carries a copy of its record, so the Secrets are a full backup. Put them into an empty namespace and the next start rebuilds the records: see [Back up and restore](../../guides/sluis/operate/back-up-and-restore.md).
No other copy of a key exists. To keep one, copy the Secrets, for example with an External Secrets `PushSecret` each.
A person's link token may have rotated since the copy; that person links again.
