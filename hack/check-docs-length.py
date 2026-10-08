#!/usr/bin/env python3
"""Warn about documentation pages over 400 lines. Never fails.

A long page is usually two pages: one task or one subject per page is the
layout the docs follow (docs/README.md). The CHANGELOG and the generated
reference pages (a page with a `<!-- generated: name -->` region, written by
`just docs-generate`) are exempt, because their length is the data's.
"""
import pathlib
import sys

LIMIT = 400
root = pathlib.Path(__file__).resolve().parent.parent
warned = 0
for page in sorted((root / "docs").rglob("*.md")):
    text = page.read_text(encoding="utf-8")
    lines = text.count("\n") + (0 if text.endswith("\n") or not text else 1)
    if lines <= LIMIT or page.name == "CHANGELOG.md" or "<!-- generated: " in text:
        continue
    rel = page.relative_to(root).as_posix()
    print(f"::warning file={rel}::{rel} is {lines} lines (over {LIMIT}): consider splitting it", file=sys.stderr)
    warned += 1
print(f"docs length: {warned} page(s) over {LIMIT} lines (a warning, not a failure)")
