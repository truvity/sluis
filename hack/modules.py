#!/usr/bin/env python3
"""The Go modules of this repository, and the release's pin of their requires.

One release (ADR 0042) tags every module at the same version, and a module
that requires another module of this repository must require THAT version in
the commit its tag points at: a consumer builds without the `replace` that
makes a checkout build against itself, and a library tagged with an older
require compiles for nobody. Which modules exist is read from the go.mod
files, never listed, so a new module is tagged, pinned and built the day it
appears.

    hack/modules.py list                  every module but the root: its directory,
                                          dependencies first (the order to tag in)
    hack/modules.py paths                 every module path, the root included
    hack/modules.py pin <vX.Y.Z> [file]   a go.mod with every require of a module of
                                          this repository set to the version; `-` or no
                                          file reads standard input, writes standard output
    hack/modules.py check <vX.Y.Z>        pins every go.mod in a scratch copy and
                                          reads the result back: the release's gate

Only python3 and the standard library: the release gate runs on a bare runner.
`pin` rewrites the version of the require lines and nothing else, and refuses
a go.mod with two requires of one module, so a reshaped file is a failure here
and not a module nobody can build.
"""

import os
import re
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SKIP = {".git", "node_modules", "testdata", "vendor", "dist", "_site"}
VERSION = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$")
MODULE = re.compile(r"^module\s+(\S+)")
# A require line, inside a block or on its own: `[require] <path> <version> [// comment]`.
REQUIRE = re.compile(r"^(\s*(?:require\s+)?)(\S+)(\s+)(v[0-9]\S*)(\s*(?://.*)?)$")


def die(msg, code=1):
    print(f"modules: {msg}", file=sys.stderr)
    sys.exit(code)


def discover():
    """{module path: directory relative to the root ('.' for the root)}."""
    found = {}
    for base, dirs, files in os.walk(ROOT):
        dirs[:] = sorted(d for d in dirs if d not in SKIP)
        if "go.mod" not in files:
            continue
        for line in (Path(base) / "go.mod").read_text().splitlines():
            m = MODULE.match(line)
            if m:
                found[m.group(1)] = os.path.relpath(base, ROOT)
                break
    return found


def requires(text, paths):
    """The in-repository requires of a go.mod: [(line index, path, version)]."""
    out = []
    for i, line in enumerate(text.splitlines()):
        m = REQUIRE.match(line)
        if m and m.group(2) in paths:
            out.append((i, m.group(2), m.group(4)))
    return out


def order(mods):
    """Directories of every module but the root, each after the ones it requires."""
    deps = {}
    for path, d in mods.items():
        text = (ROOT / d / "go.mod").read_text()
        deps[path] = {p for _, p, _ in requires(text, mods)} - {path}
    done, out = set(), []
    while len(done) < len(mods):
        ready = sorted(p for p in mods if p not in done and deps[p] <= done)
        if not ready:
            die("the modules require each other in a cycle: " + ", ".join(sorted(set(mods) - done)))
        for p in ready:
            done.add(p)
            out.append(p)
    return [mods[p] for p in out if mods[p] != "."]


def pin(version, text, mods):
    if not VERSION.match(version):
        die(f"{version} is not a release version (vX.Y.Z or a semver pre-release such as vX.Y.Z-rc.1)", 2)
    seen = {}
    for _, p, _ in requires(text, mods):
        seen[p] = seen.get(p, 0) + 1
    for p, n in seen.items():
        if n != 1:
            die(f"want at most one require of {p}, found {n}")
    lines = text.splitlines()
    for i, p, _ in requires(text, mods):
        m = REQUIRE.match(lines[i])
        lines[i] = m.group(1) + p + m.group(3) + version + m.group(5)
    return "\n".join(lines) + "\n"


def main(argv):
    if not argv:
        die("usage: modules.py list | paths | pin <vX.Y.Z> [go.mod|-] | check <vX.Y.Z>", 2)
    cmd, args = argv[0], argv[1:]
    mods = discover()
    if cmd == "list":
        print("\n".join(order(mods)))
    elif cmd == "paths":
        print("\n".join(sorted(mods)))
    elif cmd == "pin":
        if not args:
            die("usage: modules.py pin <vX.Y.Z> [go.mod|-]", 2)
        version, f = args[0], (args[1] if len(args) > 1 else "-")
        if f == "-":
            sys.stdout.write(pin(version, sys.stdin.read(), mods))
        else:
            Path(f).write_text(pin(version, Path(f).read_text(), mods))
    elif cmd == "check":
        if len(args) != 1:
            die("usage: modules.py check <vX.Y.Z>", 2)
        version = args[0]
        dirs = order(mods)
        if not dirs:
            die("no module besides the root: a release that tags none is not this repository")
        with tempfile.TemporaryDirectory() as tmp:
            for d in dirs + ["."]:
                text = pin(version, (ROOT / d / "go.mod").read_text(), mods)
                scratch = Path(tmp) / (d.replace("/", "_") + ".mod")
                scratch.write_text(text)
                bad = [p for _, p, v in requires(scratch.read_text(), mods) if v != version]
                if bad:
                    die(f"{d}/go.mod still requires {bad} after the pin")
                print(f"{'' if d == '.' else d + '/'}{version}")
    else:
        die(f"unknown command {cmd}", 2)


if __name__ == "__main__":
    main(sys.argv[1:])
