---
title: Writing style
description: How sluis pages, records and reports are written for an engineer reader, verdict first, in plain words, with dated claims and tables.
status: shipped
sidebar:
  order: 7
---

Everything in this repository is written for an engineer who has not followed the discussion: pages, decision records, requirements, spike results, pull requests and commit messages.

## Plain and short

- Say the verdict first, then the reasons.
- Use plain words and short sentences. Prefer the active voice: "the writer seals the batch", not "the batch is sealed".
- Use a table to compare options, versions or numbers, not a paragraph.
- One idea per paragraph. Cut a word that adds nothing.

## Dated and checkable

- Date every claim that can rot: a measurement, a version, a price, the state of an upstream project. "Measured 2026-10-31" lets the next reader judge it.
- Name the version of a tool or a standard when the claim depends on it.

## Instructions

- In guidance for people and agents, prefer soft wording ("prefer", "avoid") and say why. Keep "never" and "must" for the rules that have no exception, such as secrets and the public-content rules.
- Write steps as numbered lists in the imperative.

## Files

- Use the `.yaml` extension for YAML files, not `.yml`. The exceptions are the file names a tool requires: `.github/ISSUE_TEMPLATE/config.yml` and `.yarnrc.yml`. `just check-yaml-ext` checks it.
- After an edit, reflow the paragraph you changed so it keeps the wrapping the file already uses.
- Text files use LF line endings; `.gitattributes` enforces it.
