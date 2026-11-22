# 0055 — A grant name is three segments or five, and its thing is one word or three

**Status:** Accepted
**Date:** 2026-11-22

Proposal: [issue #1956](https://github.com/truvity/sluis/issues/1956).

## Context

[0020](/decisions/0020-grant-vocabulary-and-token-claims/#grant-names-and-the-vocabulary) makes a grant name `<scope>:<thing>:<role>`, three segments, and every v2 reader refuses any other count: the vocabulary, the loader of a client's `requires` and `groups`, the `groups` claim of `session` and `exchange`, and `signer`.

v1 accepts three segments or five (v1 ADR 0081, `policy.SplitGroup` on `master`, released in v1.86.0). In the long form a project is named by its vendor, its own name and its customer, `<env>:<vendor>:<project>:<customer>:<kind>`, so that two vendors can each have a project of the same name. Relying parties already read groups of that form, such as `devel:shared:keycloak:example-a:act-as`, and some of them split a group on `:`. v2 replaces v1 by a parallel switch ([0001](/decisions/0001-v1-and-v2-lines/)), so a v2 installation has to declare, issue and accept the same names, or every one of those relying parties would change at the switch.

## Decision

- **A grant name is three segments or five.** `<scope>:<thing>:<role>`, or `<scope>:<vendor>:<project>:<customer>:<role>`. Each segment is 1 to 64 ASCII letters, digits, `-` and `_`, as in 0020. Two segments, four, six or more, an empty segment and a wildcard are not a grant name.
- **The scope is the first segment and the role the last; the thing is everything between them,** joined by `:`. A thing is therefore one word, or exactly three: `<vendor>:<project>:<customer>`. In `devel:shared:keycloak:example-a:act-as` the scope is `devel`, the thing `shared:keycloak:example-a` and the role `act-as`. This is v1's rule, so a name splits the same way in both lines.
- **A thing of three words is a thing like any other.** The vocabulary declares it as a key of `things` (`shared:keycloak:example-a:`), with its roles and ladder. The `Grant` entity has its three words in `thing`, so a policy binds it in full (`Grant::"devel:shared:keycloak:example-a:act-as"`) or by a pattern on `resource.thing`. A ladder is expanded on the same scope and the same whole thing. A client lists it in its `groups` override. Narrowing compares the `<scope>:<thing>` pair, and an override thing is matched whole, never by one of its words.
- **A five-segment name and a three-segment name live side by side.** Nothing about a three-segment name changes. A thing of one word and a thing of three are different things, and neither implies the other.
- **The rule is written once and held to shared vectors.** `kits/conformance/token/grant-name-vectors.json` fixes which names are grant names and how each splits. The vocabulary kit (`GrantName::split`, `valid_thing`), the loader, `signer` and `kits/go/groupsclaim` (`Split`) are held to it.
- **The schema of `InstallationClient` grows an alternative.** The pattern of `requires` is the three-segment pattern, then `|` and the five-segment one, and the pattern of `groups` accepts a thing of one word or three. Each new pattern accepts whatever the old one accepted, so the schema keeps its `$id`, and the compatibility check of the schemas accepts a pattern that only gains an alternative after the old one.
- **A token may carry a longer group.** `signer` takes a group of five names of 64 bytes and four delimiters of 8 before it knows the delimiter, and holds it to the grant name of the audience's delimiter after.

### What this supersedes

- **[0020](/decisions/0020-grant-vocabulary-and-token-claims/), in part.** In "Grant names and the vocabulary", the first bullet's "A grant name is `<scope>:<thing>:<role>`" with three segments, and "a thing is a system, project or application" as one segment: a grant name is three segments or five, and a thing is one word or three, as above. Everything else in 0020 stands: the vocabulary, the ladders, sensitive scopes, the wildcard rules, the claim set, narrowing, the override and the delimiter. 0020's status line holds one record number, and it now names this one; [0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/) still supersedes the part of 0020 that it names.

## Consequences

- A v2 installation can declare the names v1 issues today, and a relying party that reads a five-segment group reads the same group from v2.
- A relying party that splits a group on `:` and reads the last part still finds the role; one that reads the second part finds the vendor of a five-segment name, not a thing, as it does with v1.
- A typo that drops or adds one segment is a four- or six-segment name and is refused at load, not read as a grant nobody holds.
- A thing name can hold `:`. The vocabulary's key for such a thing is written as a plain YAML key with colons inside, and the delimiter of a client still replaces every `:`, so the vendor, the project and the customer are each a part for a verifier that splits.
- The repository check of the example installations (`just check-installations`) accepts a thing of three words.
- The requirement row REQ-POLICY-025 states the rule.

## Roads not taken

- **Any count of segments from three up.** A mistyped four-segment name would read as a thing with one colon and load as a grant nobody holds. Refusing four keeps the typo visible, as v1 does.
- **Keep three segments, and map the long names at the switch.** Every relying party that reads a five-segment group would change at the switch, which is the flag day the parallel switch exists to avoid.
- **A second reader for the long form.** One reader per language, held to one set of vectors, cannot drift; two readers agree until the day they do not.
- **A new schema version of `InstallationClient` for the wider pattern.** The new pattern accepts everything the old one did, so the old `$id` stays true; a new version would make every configuration change its `$schema` for no gain.

## Follow-ups

None.
