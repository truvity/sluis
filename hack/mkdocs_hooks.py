"""mkdocs hook: a relative link to something the site has no page for points at the repository.

The pages link to source files, charts and changelogs with relative paths, which
GitHub resolves. On the site those files do not exist, so such a link is
rewritten to the same path on GitHub (blob for a file, tree for a directory). The same goes for a link to a
directory inside docs/ that has no README.md: GitHub lists it, the site has no
page for it, so it points at the listing.
"""

import posixpath
import re
from pathlib import Path

REPO = "https://github.com/truvity/sluis"
LINK = re.compile(r"(\]\()([^)\s#]+)(#[^)\s]*)?(\))")


def on_page_markdown(markdown, page, config, files):
    docs = Path(config["docs_dir"])
    root = docs.parent
    here = posixpath.dirname(page.file.src_uri)

    def fix(m):
        target = m.group(2)
        if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:", target) or target.startswith("/"):
            return m.group(0)
        inside = posixpath.normpath(posixpath.join(here, target))
        if not inside.startswith(".."):
            if (docs / inside).is_dir() and not (docs / inside / "README.md").exists():
                return f"{m.group(1)}{REPO}/tree/master/docs/{inside}{m.group(3) or ''}{m.group(4)}"
            return m.group(0)
        repo_path = posixpath.normpath(posixpath.join("docs", here, target))
        if repo_path.startswith(".."):
            return m.group(0)
        kind = "tree" if (root / repo_path).is_dir() else "blob"
        return f"{m.group(1)}{REPO}/{kind}/master/{repo_path}{m.group(3) or ''}{m.group(4)}"

    return LINK.sub(fix, markdown)
