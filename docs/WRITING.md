# Writing the documentation

Every page is one of four types. Pick the type first; it decides what the page may hold.

| Type | Answers | Directory |
|---|---|---|
| Tutorial | take me from nothing to a working install | `*/install/` |
| How-to | how do I do one task | `*/how-to/`, `*/connect/`, `*/operate/` |
| Reference | what are the keys, flags and fields | `*/reference/`, `docs/sdk/` |
| Explanation | how does it work | `*/explanation/` |

ADRs under `docs/decisions/` hold the why. Every other page links to them and does not re-argue them.

## Skeletons

**Tutorial.** Outcome in one sentence. What you need, as a table. Numbered steps, each a code block and its expected result. Check it works. Next.

**How-to.** Goal in one sentence. Before you start: at most three traps, one line each. Steps: code first, one sentence of prose each. Verify. Roll back. Decided in: ADR links.

**Reference.** One sentence of scope, then the generated region or the tables. Nothing else.

**Explanation.** The question is the title. Three to six sections, one idea each. Decided in: ADR links.

## Limits

`hack/check-docs-budget.py` enforces these in `just docs-check`.

| | Tutorial | How-to | Reference | Explanation | Index |
|---|---|---|---|---|---|
| Prose words, excluding code, tables and generated regions | 900 | 400 | 300 | 800 | 250 |

- A sentence has at most 25 words. None has more than 40.
- A paragraph has one idea and at most four sentences.
- A how-to has at most six steps. A step is a heading only when it has a code block.
- Tables hold rows with the same columns. Prose inside `|` borders is prose.
- No parenthetical aside longer than three words. Make it a sentence or delete it.
- Link text names the target. Never "here" or "this page".

## Rules

1. Say each fact once. The glossary row for a term names the page that owns it. Everywhere else, link.
2. No justification in a how-to or tutorial. The why is an ADR link under "Decided in".
3. Code or config first, prose second. Show the step, then say one sentence about it.
4. One name per thing. The glossary owns the names: `docs/sluis/explanation/glossary.md`, `docs/audit/explanation/glossary.md`. Do not coin a term it lacks.
5. Present tense, second person, active voice. "The chart refuses the value", not "the value is refused".
6. Condition before instruction. "To run two replicas, set `adapters.state: dynamodb`."
7. History goes to the CHANGELOG or an ADR. A page describes what ships now.
8. A legacy identifier is named once, with the words "legacy identifier, renamed in v1.75–v1.76". The nineteen that still exist are listed in `hack/docs-hygiene-allow.tsv`.
9. The old product names appear nowhere else: `access-roster`, `access-issuer`, `github-roster`, `slack-roster`, `githubroster`, `slackroster`, `directoryroster`, `accessctl`, `access-proxy`. Exceptions: CHANGELOG, ADRs, `internal/audit/catalogue/testdata/released/`.

## Banned

Phrases: "note that", "it is worth", "in other words", "that is,", "in practice", "deliberately", "by design", "on purpose", "exactly" as emphasis, "the whole of", "first-class", "which is why", "the rule under", "is the contract", "is the keystone".

Patterns: a paragraph that opens "The X is the Y"; a sentence that states a rule and defends it; a bold trap followed by a paragraph; an em-dash aside; a list of principles inside a how-to; a page that restates another page's table.

## Generated regions

Where a reference page can be produced from code, a schema or a release file, wrap it in `<!-- generated: name -->` and `<!-- /generated -->`.
`just docs-generate` rewrites the region and `just docs-check` fails when it is stale. Do not edit inside the markers.

## Before you push

- [ ] The page type is in the PR description and the page sits in that type's directory.
- [ ] `just docs-check` passes: budget, banned phrases, retired names, links, generated regions.
- [ ] Every fact is owned here, or replaced by a link to its owner.
- [ ] Every "because" and "so that" is gone, or moved to an ADR link under "Decided in".
- [ ] Read aloud. Every sentence over 25 words is split.
- [ ] Every code block renders: `sluisctl render`, `helm template`, or the values schema.
- [ ] A moved or merged page has a row in `docs/_redirects/<area>.yaml`.

## Sources

Fetched 2026-10-09.

- Diátaxis, [How-to guides](https://diataxis.fr/how-to-guides/) and [Reference](https://diataxis.fr/reference/): a how-to is "action and only action", with "no digression, explanation, teaching"; reference is "austere", should "describe and only describe", and is generated "where possible".
- Google developer documentation style guide, [Highlights](https://developers.google.com/style/highlights) and [Sentence structure](https://developers.google.com/style/sentence-structure): second person, active voice, present tense, "mention the circumstance, conditions, or goal before you provide the instruction", descriptive link text.
- Microsoft Writing Style Guide, [Top 10 tips](https://learn.microsoft.com/en-us/style-guide/top-10-tips-style-voice): "bigger ideas, fewer words", "get to the point fast", "start each statement with a verb", avoid "there is" and "there are".
- GOV.UK content guidance, [Use clear language](https://guidance.publishing.service.gov.uk/writing-to-gov-uk-standards/writing-guidelines/clear-language/) and [Create a clear structure](https://guidance.publishing.service.gov.uk/writing-to-gov-uk-standards/writing-guidelines/clear-structure/): "split up sentences that are over 25 words long"; users "only read 20 to 28% of text on a webpage"; "put the most important information first".
