#!/usr/bin/env python3
"""Refuse a repeated H3 (### Added, ### Changed, ...) within one version of CHANGELOG.md.

A release lists each kind of change once; a second `### Added` is the same
heading written by a second pull request and belongs merged into the first.

Versions older than v1.74.0-rc.1 predate the rule and are not checked.

Usage: hack/check-changelog-headings.py [CHANGELOG.md]
"""
import re
import sys

FIRST_CHECKED = "v1.74.0-rc.1"


def main() -> int:
    path = sys.argv[1] if len(sys.argv) > 1 else "CHANGELOG.md"
    version = None
    seen: dict[str, int] = {}
    bad = []
    in_fence = False
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            if line.startswith("```"):
                in_fence = not in_fence
            if in_fence:
                continue
            if line.startswith("## "):
                version = line[3:].split()[0]
                seen = {}
                if version == "v1.73.0" or re.fullmatch(r"v1\.(?:[0-6]\d|7[0-3])\..*", version):
                    break  # the rest predates the rule
                continue
            if version and line.startswith("### "):
                head = line.strip()
                if head in seen:
                    bad.append(f"{path}:{n}: {version}: repeated {head!r} (first at line {seen[head]})")
                else:
                    seen[head] = n
    if bad:
        print("\n".join(bad), file=sys.stderr)
        print("changelog-headings: merge the repeated headings into one per version", file=sys.stderr)
        return 1
    print("changelog-headings: no repeated heading within a version")
    return 0


if __name__ == "__main__":
    sys.exit(main())
