#!/usr/bin/env bash
# Refuse an automatic PATCH release whose unreleased CHANGELOG entries say
# **Breaking:**. Policy: a breaking change is never a patch. Minors are tagged
# by hand, after the change's CHANGELOG heading has landed; auto-release only
# ever cuts patches, so it must not ship one over a breaking entry.
#
# Looks at every section ABOVE the newest released version: `## Unreleased`
# and any `## vX.Y.Z` heading newer than the latest tag. The entries are
# written `- **Breaking: what changed.**`, so the match is `**Breaking:`.
#
# Usage: hack/check-no-breaking-patch.sh [CHANGELOG.md] [latest-tag]
# The latest tag defaults to the newest v* tag of this checkout.
set -euo pipefail

changelog="${1:-CHANGELOG.md}"
latest="${2:-}"
[ -f "$changelog" ] || { echo "no-breaking-patch: $changelog not found" >&2; exit 2; }
if [ -z "$latest" ]; then
    latest=$(git tag --list 'v[0-9]*' --sort=-version:refname | head -1)
fi
[ -n "$latest" ] || { echo "no-breaking-patch: no release tag to measure from" >&2; exit 2; }

# newer A B: A sorts above B as a version.
newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" = "$1" ]; }

pending=""
take=0
while IFS= read -r line; do
    if [[ $line == "## "* ]]; then
        heading=${line#\#\# }
        heading=${heading%% *}
        if [ "$heading" = Unreleased ]; then
            take=1
        elif [[ $heading =~ ^v[0-9] ]] && newer "$heading" "$latest"; then
            take=1
        else
            take=0
            [[ $heading =~ ^v[0-9] ]] && break   # the released sections begin
        fi
        continue
    fi
    [ "$take" = 1 ] && pending+="$line"$'\n'
done <"$changelog"

if hits=$(grep -n -F '**Breaking:' <<<"$pending"); then
    echo "no-breaking-patch: the entries after $latest in $changelog include a breaking change:" >&2
    sed 's/^/  /' <<<"$hits" | cut -c1-160 >&2
    echo "A breaking change is never a patch: tag the next MINOR by hand (docs: CONTRIBUTING.md, Releasing). No patch cut." >&2
    exit 1
fi
echo "no-breaking-patch: nothing breaking after $latest in $changelog"
