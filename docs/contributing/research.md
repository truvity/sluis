---
title: Spikes and research
description: The shape of a sluis spike or research result, verdict first, dated and reproducible, and the rule that a standard beats an invented format.
status: shipped
sidebar:
  order: 5
---

A spike answers one question by trying it. Research answers one question by reading and comparing. Both end in a result the maintainer can decide on, posted on the spike's issue and followed by the records and pages it changes.

## Standards first

A spike that compares formats or protocols starts from the standards that exist, and pins their versions ([Standards first](/contributing/tools/#standards-first)).

## The shape of a result

Write the parts in this order:

1. **Question.** The one question the spike or research answers.
2. **When.** The date and time it was measured, in UTC. A number without a date cannot be compared with the next one.
3. **Verdict.** The answer, first, in one or two sentences.
4. **Method.** What was run or read, with the exact versions, so someone else can repeat it.
5. **What the setup over- and under-counts.** How the test setup differs from a real installation, and in which direction each difference moves the numbers.
6. **Numbers.** In tables, with units.
7. **Roads not taken.** The approaches you did not try or dropped, each with the reason.
8. **Open questions.** Each as options with pros and cons, and a recommendation. The maintainer decides.
9. **Changes made.** Every change to any machine, account or device: what was installed, configured, created or reset, and whether it was undone.
10. **Raw logs.** Where the raw output is kept.

Measure before you conclude, and read what each tool printed, not only its exit code ([Tools, checks and verification](/contributing/tools/)).

## Keep it public

A result posted in the repository or on an issue follows [Public content and secrets](/contributing/public-content/): example names for hosts and accounts, no secrets, no device serials. A result that needs private names to make sense stays internal, and only a generic version is posted publicly, as [GOVERNANCE.md](https://github.com/truvity/sluis/blob/main/GOVERNANCE.md) says in "From internal reasoning to a public ADR".
