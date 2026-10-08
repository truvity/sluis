#!/usr/bin/env python3
"""Two checks over the documentation that nothing compiles.

1. LINKS. Every relative link in a Markdown file, and every `docs/...md` path
   named in a Chart.yaml, must point at a file that exists; a `#anchor` must
   match a heading of the file it points into (GitHub's slug rules). A link to
   a page that was never written is found by a stranger, and Chart.yaml's
   description named docs/explanation/verify-and-issue.md for months.

2. BANNED NAMES. The product was renamed (docs/decisions/0035-renamed-to-sluis.md):
   `access-roster`, `access-issuer` and the NATS adapter are gone. They stay in
   places that are history (docs/decisions, CHANGELOG.md, skipped here) and
   in identifiers that deliberately kept the old name (group names, URNs,
   label keys, an npm alias), which hack/docs-hygiene-allow.tsv lists with a
   reason each. Everything else must not use them.

The allowlist has three kinds of row, tab separated, `#` for comments:

  allow     <path glob>  <regex>   <reason>  an identifier that keeps its old name:
                                              the regex's matches are removed from a
                                              line before the banned names are looked for
  baseline  <path>       <term>    <count> <reason>
                                              prose still to be rewritten: at most
                                              <count> hits of <term> in <path>. It is a
                                              ratchet: more hits fail, and fewer fail
                                              too, so the row is lowered as pages are fixed
  link      <path>       <target>  <reason>  a link that is deliberately not a file
                                              (a template placeholder)

Stdlib only, so a runner with a bare python3 can run it.
"""

import fnmatch
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ALLOW = ROOT / "hack" / "docs-hygiene-allow.tsv"

SKIP_DIRS = {".git", "node_modules", ".devbox", "dist", ".next", "build", ".venv"}
# History: what an old name is allowed to appear in.
HISTORY = ("docs/decisions/", "CHANGELOG.md")

TERMS = {
    "NATS": re.compile(r"\bNATS\b"),
    "access-issuer": re.compile(r"access-issuer"),
    "access-roster": re.compile(r"access-roster"),
}


def files():
    out = []
    for p in sorted(ROOT.rglob("*")):
        if not p.is_file() or any(part in SKIP_DIRS for part in p.relative_to(ROOT).parts):
            continue
        # audit/ and its chart are a component with its own vocabulary and its
        # own link check (audit/internal/docscheck); the retired-name rules
        # here are sluis's.
        if rel(p).startswith(("audit/", "charts/audit/")):
            continue
        if p.suffix == ".md" or p.name == "Chart.yaml":
            out.append(p)
    return out


def rel(p):
    return p.relative_to(ROOT).as_posix()


def load_allow():
    allow, baseline, links = [], {}, set()
    for n, raw in enumerate(ALLOW.read_text(encoding="utf-8").splitlines(), 1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        cols = raw.split("\t")
        kind = cols[0]
        try:
            if kind == "allow":
                _, glob, regex, reason = cols
                allow.append((glob, re.compile(regex), reason))
            elif kind == "baseline":
                _, path, term, count, reason = cols
                if term not in TERMS:
                    raise ValueError(f"unknown term {term}")
                baseline[(path, term)] = int(count)
            elif kind == "link":
                _, path, target, reason = cols
                links.add((path, target))
            else:
                raise ValueError(f"unknown row kind {kind}")
            if not cols[-1].strip():
                raise ValueError("a row needs a reason")
        except ValueError as e:
            sys.exit(f"{ALLOW.name}:{n}: {e}")
    return allow, baseline, links


def slug(heading):
    """GitHub's anchor for a heading: lowercase, punctuation dropped (not
    collapsed), spaces to hyphens."""
    h = re.sub(r"[`*_~]", lambda m: "_" if m.group(0) == "_" else "", heading.strip().lower())
    h = re.sub(r"<[^>]+>", "", h)
    h = re.sub(r"[^\w\- ]", "", h)
    return h.replace(" ", "-")


def anchors(text):
    seen, out = {}, set()
    fenced = False
    for line in text.splitlines():
        if line.lstrip().startswith(("```", "~~~")):
            fenced = not fenced
        if fenced:
            continue
        m = re.match(r"#{1,6}\s+(.*?)\s*#*\s*$", line)
        if m:
            s = slug(m.group(1))
            k = seen.get(s, 0)
            seen[s] = k + 1
            out.add(s if k == 0 else f"{s}-{k}")
        out.update(re.findall(r'<a\s+(?:name|id)="([^"]+)"', line))
    return out


LINK = re.compile(r"(?<!\!)\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+\"[^\"]*\")?\s*\)|(?<=\!)\[[^\]]*\]\(\s*<?([^)\s>]+)>?[^)]*\)")


def strip_code(text):
    """Blank fenced blocks and inline code: a link written in a code sample is
    not a link."""
    out, fenced = [], False
    for line in text.splitlines():
        if line.lstrip().startswith(("```", "~~~")):
            fenced = not fenced
            out.append("")
            continue
        out.append("" if fenced else re.sub(r"`[^`]*`", "", line))
    return out


def check_links(allowed_links):
    errors, cache = [], {}

    def heads(path):
        if path not in cache:
            cache[path] = anchors(path.read_text(encoding="utf-8"))
        return cache[path]

    for p in files():
        text = p.read_text(encoding="utf-8")
        if p.name == "Chart.yaml":
            for m in re.finditer(r"\b(docs/[\w./-]+\.md)\b", text):
                if (rel(p), m.group(1)) not in allowed_links and not (ROOT / m.group(1)).exists():
                    errors.append(f"{rel(p)}: names {m.group(1)}, which does not exist")
            continue
        for lineno, line in enumerate(strip_code(text), 1):
            for m in LINK.finditer(line):
                target = m.group(1) or m.group(2)
                if re.match(r"[a-z][a-z0-9+.-]*:", target, re.I) or target.startswith("//"):
                    continue  # http(s), mailto, ...
                if (rel(p), target) in allowed_links:
                    continue
                path, _, anchor = target.partition("#")
                dest = p if path == "" else (p.parent / path).resolve()
                if path and path.startswith("/"):
                    dest = (ROOT / path.lstrip("/")).resolve()
                if not dest.exists():
                    errors.append(f"{rel(p)}:{lineno}: broken link ({target}): no such file")
                elif anchor and dest.suffix == ".md" and anchor.lower() not in heads(dest):
                    errors.append(f"{rel(p)}:{lineno}: broken link ({target}): no heading #{anchor} in {rel(dest)}")
    return errors


def check_names(allow, baseline):
    errors, found = [], {}
    for p in files():
        path = rel(p)
        if path.startswith(HISTORY[0]) or path == HISTORY[1]:
            continue
        rules = [rx for glob, rx, _ in allow if fnmatch.fnmatch(path, glob)]
        for lineno, line in enumerate(p.read_text(encoding="utf-8").splitlines(), 1):
            for rx in rules:
                line = rx.sub(" ", line)
            for term, rx in TERMS.items():
                for _ in rx.finditer(line):
                    found.setdefault((path, term), []).append(lineno)
    for (path, term), lines in sorted(found.items()):
        limit = baseline.get((path, term), 0)
        if len(lines) > limit:
            errors.append(f"{path}: {len(lines)} use(s) of the retired name {term!r} (allowed {limit}), lines {lines[:8]}"
                          " -- use the current name, or add an `allow` row for an identifier that keeps it")
    for (path, term), limit in sorted(baseline.items()):
        n = len(found.get((path, term), []))
        if n < limit:
            errors.append(f"hack/{ALLOW.name}: baseline for {path} / {term} is {limit} but only {n} remain: lower it (or delete the row)")
    return errors


def main():
    allow, baseline, links = load_allow()
    errors = check_links(links) + check_names(allow, baseline)
    if errors:
        print("docs hygiene:", file=sys.stderr)
        for e in errors:
            print("  " + e, file=sys.stderr)
        sys.exit(1)
    print(f"docs hygiene: links resolve, no retired names outside the allowlist "
          f"({len(allow)} identifier rules, {len(baseline)} baselined pages)")


if __name__ == "__main__":
    main()
