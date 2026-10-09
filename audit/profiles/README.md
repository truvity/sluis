# Framework profiles

This directory holds the framework profiles: one YAML file per standard, each saying what that
framework requires of a profile (which record fields must be present or are forbidden, how
identities are treated, how long copies are kept, what integrity applies and how often the trail is
reviewed). Each cites the clauses it reads and carries a disclaimer. A deployment composes them
into its own **profiles**.

The directory and the `profile` package carry the name; the deployment document lists the framework
profiles each of its profiles is composed from under the key `frameworks:`. A *preset* is something else (a named bundle of adapter or deployment
choices, per truvity/policy decision 0012), and the old `presets:` key is refused with a message
that names `frameworks:`. Everything about them, including the table of the seven and the composition
rules, is in [the profiles reference](../../docs/reference/audit/profiles.md); which to compose is
[which profiles to compose](../../docs/concepts/audit/which-profiles-to-compose.md). The format is
[`sdk/schemas/profile.schema.json`](../sdk/schemas/profile.schema.json), and `audit profile explain <name>`
prints what a profile keeps.
