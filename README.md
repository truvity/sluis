# sluis

sluis is an open-source identity and access broker for people, robots and external systems. It sits between the identity providers that already know who someone is and the systems that need a credential before they let that someone in.

**The name.** *sluis* is Dutch for a sluice or canal lock (plural *sluizen*). Pronounced /slœy̯s/, roughly "slouse", with the Dutch *ui* sound of *huis*. [Hear it](https://commons.wikimedia.org/wiki/File:Nl-sluis.ogg) · [Wiktionary](https://en.wiktionary.org/wiki/sluis)

## Where it stands

v2 is the line on the `main` branch. Its core is being built in the open: `v2.0.0-alpha.1` was published on 2026-11-08. Customer identity and quota, parity with v1, the switch of the maintainer's platform, and the PKI, SSH and database add-on come after it, in that order. v1 runs in production and lives on the `master` branch.

## Read more

- Documentation: https://sluis.id (start at the [overview](https://sluis.id/overview/)).
- The order of the work: the [roadmap](https://sluis.id/roadmap/).
- Working on sluis: [CONTRIBUTING.md](CONTRIBUTING.md), [GOVERNANCE.md](GOVERNANCE.md) and [SECURITY.md](SECURITY.md). Report a vulnerability as SECURITY.md says, never in an issue.
- Root custody lives in the sister project [ritueel](https://ritueel.id).

sluis is released under the licence in [LICENSE](LICENSE).
