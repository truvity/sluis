# 0080 — The GitHub controller reconciles an organisation's settings from a document it reads at run time

**Status:** Proposed (2026-11-18). Amends [0078](0078-a-company-access-fragment-is-owned-rows-checked-against-a-ceiling-and-by-effect.md)
(a company access fragment is owned rows, checked against a ceiling and by effect) and
[0079](0079-a-company-fragment-carries-its-app-grants-and-its-gates.md) (a company fragment carries its App grants and
its gates).
**Date:** 2026-11-18

## Context

The GitHub controller reconciles teams and members. Each organisation's maintainers also keep three kinds of
organisation setting by hand, with recipes they run themselves:

- **Code security configurations**, created and attached to repositories.
- **Actions variables** at the organisation level.
- **Actions secrets** at the organisation level.

Nobody can say who last wrote a setting, a recipe run twice by two people disagrees with itself, and the platform
cannot say which names are its own. Some variables are the platform's: the runner labels, the remote builders, the
caches, the OpenBao settings and the issuer address. An organisation that sets one of them changes how every build in
the organisation runs.

ADR 0078 and ADR 0079 hold a company's rows to a ceiling, and merge them when the installation renders. This record
needs a reading at run time: a setting changes when the organisation's document changes, with no render and no
release. ADR 0079 left that open: "Loading a company's fragment at run time, with a last good pin, is not part of this
record."

The maintainer decided on 2026-11-18 that the controller, on the v1 line, becomes the one writer of these settings,
and that GitHub Apps stay as they are.

## Decision

1. **D1. The input is the company fragment, with an `org` section.** `tenant.Fragment.Org` holds variables (name,
   value, visibility `all`, `private` or `selected`, repositories), secrets (name, the name of a SecureString
   parameter, visibility, repositories) and code security configurations (name, description, GitHub's own settings,
   and an attachment: every repository or selected ones). A secret has no key for a value. The decoder refuses an
   unknown key. The installation does not merge the section into the model: `Check` and `CheckOrg` read it and
   `MergeAll` ignores it.
2. **D2. The ceiling lists what the platform owns.** `tenant.Ceiling.Org` holds `platform_variables`, path.Match
   patterns of the variable names the platform owns (`CI_RUNNER_LABEL_*`, `CI_REMOTE_BUILDERS*`, `CI_GOCACHE_*`,
   `CI_BUILDX_CACHE_STABLE`, `CI_SBOM_GENERATOR_STABLE`, `CI_OPENBAO_*`); `platform_valued`, names whose value is the
   platform's and whose visibility and repositories are the organisation's (`ACCESS_ROSTER_ISSUER`); and
   `secret_prefix`, the prefix of every secret's parameter. The lists are the installation's configuration, never
   code. A section that declares a platform-owned name, or gives a value for a platform-valued one, is refused with the
   new code `owner-org-platform`. Any other violation of the section is `owner-org`. GitHub reads the names of
   variables and secrets without regard to case, so every comparison of a name, the platform's patterns included, is
   made on the name folded to upper case, and two names of one document that fold to one are refused. A refusal names
   the path and the rule and repeats nothing the document said.
3. **D3. The installation sets the platform's values.** `controllers.github.orgSettings.<org>.variables` holds each
   platform variable's value, and the controller writes them beside the document's.
4. **D4. The document lives in the organisation's own account, and is read live.** The organisation keeps it in its own
   parameter store, under its own prefix. The controller assumes a read-only role in that account that trusts only the
   controller's role, and reads the document at every pass. The role's ARN, the region and the parameter name are
   installation configuration, `controllers.github.orgSettings.<org>.read`, with an optional `externalId` the
   organisation's trust policy can require. The controller assumes the role with a session policy that limits it to
   the document and the secrets' parameters. One role serves one organisation, and the document is not under the
   secrets' prefix. There is no source-level link between the
   platform's repository and the organisation's.
5. **D5. A document that fails is not applied.** A document that cannot be read, is not valid YAML, is empty, carries a section
   other than `org`, or has any violation is applied in no part. An empty `org` section reads as a parameter that lost
   its content and not as a request to remove everything. The settings GitHub holds stay as the last good
   document made them. The pass reports the failure on the organisation's report and records
   `roster.github_org_settings.refused` once for as long as it stands. It never fails the membership pass.
6. **D6. A mode, `report` or `enforce`, lives in the installation's entry.** `report` is the default: the pass computes
   and reports what differs and changes nothing. `enforce` applies it. The document has no key for the mode, so the
   platform can hold an organisation to `report` whatever the document says, and an organisation cannot raise its own
   mode. `enforce` also needs the organisation in `enabledOrgs`, the controller's one kill switch: an organisation
   that is not in it runs as `report`, and the report says so.
7. **D7. Secrets are sealed and never kept.** The controller reads the value with the same assumed role and seals it
   with the organisation's Actions public key as a libsodium sealed box (`crypto_box_seal`, which
   `golang.org/x/crypto/nacl/box.SealAnonymous` implements, a library the repository already depends on), and writes it.
   The value is never logged, never stored and never in an audit record. The controller learns that a value changed from
   the parameter's version, which it records beside the name, and writes a secret again only then.
8. **D8. The controller touches only what it created.** The report holds, per organisation, the names the controller
   created or changed. A name removed from the document is removed from GitHub if it is in that record. A name GitHub
   holds that is not in the record is never written, adopted or removed: when it differs from the document the pass
   reports a `conflict` row, audits it once, and holds it until an operator removes it on GitHub or it disappears. A
   secret's value cannot be compared, so an existing secret the controller did not write is always a conflict. The one
   exception is the installation's own variables, which the controller always writes from the installation's
   configuration. A recorded name is not trusted: one that is not a name GitHub accepts, or that the platform owns
   and the installation does not set, is not removed. A pass that would remove more than half of the document-owned
   recorded names that GitHub still holds (the installation's variables are not counted), and at least two, removes none: it reports and audits `removal held`. An operator releases it by restoring the
   document or by removing the names on GitHub.
9. **D9. One audit record per change.** `roster.github_org_setting.changed` names the organisation, the kind, the name,
   the action and the mode, and never a value. In `enforce` mode it records each change made or refused. In `report`
   mode it records each difference once, when it first appears.
10. **D10. The controller is the one writer.** It replaces the recipes the organisations ran by hand. GitHub Apps are
    unchanged.

## Consequences

- An organisation changes its variables, secrets and code security configurations with a change to its own parameter,
  reviewed in its own repository, and the platform's names stay the platform's.
- The platform turns an organisation on in `report`, reads what would change, and moves it to `enforce` when the
  difference is the one it expects.
- An organisation that has hand-made settings under the document's names must remove them on GitHub once, so that
  the controller creates its own. That is the price of the rule that the controller touches only what it created.
- The controller's role needs `sts:AssumeRole` on each organisation's read role, and the organisation's read role must
  trust it. Both are infrastructure code outside this repository.
- The organisation's App needs the permissions of the three settings. Which App that is stays the installation's choice,
  through `apps.github.apps` and `controllers.github.appRefs`.
- The controller's report grows a `settings` object. A console that does not know it ignores it.
- The audit catalogue moves to 1.24.0 with two actions.

## Known limits

- A refusal names the document's map keys in its paths (a variable's name), and a held row repeats the text of an
  upstream AWS error, which can name a parameter. Neither repeats a value.
- Somebody with write access on GitHub can delete a managed setting and create one of the same name by hand. The
  controller then sees a name it recorded and overwrites it; it cannot tell the two apart.

## Alternatives considered

**Merge the section at render time, as the rows of ADR 0078 are.** A setting then changes with a release of the
installation, and the organisation cannot change its own variable without the platform. Rejected: this is the case for
reading at run time.

**Keep the document in the platform's repository.** The platform then reviews every variable of every organisation.
Rejected: the organisation owns the rest of its variables.

**Put the mode in the document.** The platform could not hold an organisation to `report`. Rejected.

**Remove anything the document does not name.** One missing line deletes a variable somebody else wrote. Rejected: the
controller removes only what it recorded writing.

**Store a digest of each secret to detect a change.** A digest of a secret is a copy of it. Rejected for the parameter's
version.
