#!/bin/sh
# Cut an annotated release tag from its release note.
#
# The convention this encodes (see docs/releases/README.md): a release tag is ANNOTATED and its
# message is the release note verbatim, so `git tag -n99 vYYYY.W[.R]` and the file in docs/releases/
# say the same thing. Placed on the commit where that release's work is complete — usually the merge
# that brought it into main.
#
# Until now that lived in whoever cut the last one's memory, which is why v0.4.42 and v0.4.43 sat on
# main untagged.
#
#   scripts/tag-release.sh v2026.38.1 dacc4328     tags that commit
#   scripts/tag-release.sh v2026.38.1              tags HEAD
#
# The examples carry no trailing '#' comment on purpose: zsh does not enable INTERACTIVE_COMMENTS,
# so a line copied out of documentation with an explanation after it hands the '#' to this script as
# the commit argument. The guard below turns that into a sentence instead of a git error.
#
# It does not push. Pushing a tag is the one irreversible step here, so it stays a separate,
# deliberate command — which the script prints for you.
#
# Maturity is NOT decided here. The tag is only the number; whether the release is a pre-release or
# a full release is GitHub Release metadata, set at publication (docs/adr/0034).
set -eu

# CalVer rules live in one place, shared with the release workflow, so the tag a maintainer cuts
# and the tag CI accepts cannot drift apart.
here=$(dirname -- "$0")
# shellcheck source=lib/calver.sh
. "$here/lib/calver.sh"

usage() {
    echo "usage: $0 <version> [commit]" >&2
    echo "  e.g. $0 v$(calver_current_week).1 dacc4328   (commit defaults to HEAD)" >&2
    exit 2
}

[ $# -ge 1 ] || usage
version=$1
commit=${2:-HEAD}

# See the note above: '#' reaching here means a copied comment, not a revision. Without this the
# failure is `git rev-parse` saying "Needed a single revision", which names neither the argument
# that was wrong nor the reason it arrived.
case "$commit" in
    '#'*)
        echo "error: '$commit' is not a commit — it looks like a trailing # comment that your shell" >&2
        echo "       passed as an argument (zsh does not treat # as a comment interactively)." >&2
        echo "       Re-run without the comment: $0 $version" >&2
        exit 1
        ;;
esac

# The mistake worth naming: reaching for the old numbering out of habit. The v0.x line ended at
# v0.4.72 — the bridge every older database has to pass through — and it is not a CalVer tag.
# The first component is what separates them: a CalVer tag carries a four-digit year, so anything
# shaped v<digits>.<digits>.<digits> whose head is under 1000 is the retired numbering.
semver_like() {
    printf '%s' "$1" | awk '
    {
        if ($0 !~ /^v[0-9]+\.[0-9]+\.[0-9]+$/) exit 1
        head = $0
        sub(/^v/, "", head)
        sub(/\..*$/, "", head)
        exit(head + 0 < 1000 ? 0 : 1)
    }'
}

if semver_like "$version"; then
    echo "error: '$version' is a SemVer tag. The v0.x line ended at v0.4.72, which is the" >&2
    echo "       database bridge and is not a CalVer release. New tags are vYYYY.W[.R]:" >&2
    echo "       this week is $(calver_current_week), so e.g. v$(calver_current_week).1" >&2
    exit 1
fi

if ! calver_valid "$version"; then
    echo "error: '$version' is not a CalVer tag name. Expected vYYYY.W[.R] — the ISO week-numbering" >&2
    echo "       year, the UTC ISO week the series starts in, and an optional revision. A tag with no" >&2
    echo "       revision is the first release of that week; every later artifact set in the same" >&2
    echo "       week needs its own, starting at 1. No leading zeroes, no maturity suffix (-beta/-rc)," >&2
    echo "       no extra components. This week is $(calver_current_week): v$(calver_current_week)" >&2
    echo "       for the first release, v$(calver_current_week).2 for the next one after it." >&2
    exit 1
fi

root=$(git rev-parse --show-toplevel)
note="$root/docs/releases/$version.md"

[ -f "$note" ] || { echo "error: no release note at docs/releases/$version.md" >&2; exit 1; }

if git rev-parse -q --verify "refs/tags/$version" >/dev/null; then
    echo "error: tag $version already exists locally" >&2
    exit 1
fi

sha=$(git rev-parse --verify "$commit^{commit}") || exit 1

# The note has to be reachable from the commit being tagged, or the tag describes a release whose
# own notes are not in it — which is how a tag ends up pointing at the wrong thing.
if ! git cat-file -e "$sha:docs/releases/$version.md" 2>/dev/null; then
    echo "error: $commit does not contain docs/releases/$version.md" >&2
    echo "       the tag would describe a release the commit predates" >&2
    exit 1
fi

# The annotation is taken from the COMMIT, not from the working tree. The tag has to keep saying what
# the tagged commit says even if the note has uncommitted edits, which is exactly the case where
# tagging from the file quietly publishes text no commit contains.
if ! git show "$sha:docs/releases/$version.md" | diff -q - "$note" >/dev/null 2>&1; then
    echo "warning: docs/releases/$version.md differs from the copy in $commit" >&2
    echo "         tagging the committed version; commit the working-tree edits and re-tag to change it" >&2
fi

notes=$(mktemp)
trap 'rm -f "$notes"' EXIT INT TERM
git show "$sha:docs/releases/$version.md" > "$notes"
git tag -a "$version" "$sha" -F "$notes"

echo "created $version -> $(git rev-parse --short "$sha")  $(git log -1 --format=%s "$sha")"
echo
echo "review:  git tag -n99 $version | head"
echo "push:    git push origin $version"
echo "publish: the tag push prepares a DRAFT release; maturity (pre-release / full release) and the"
echo "         :latest and :beta channels are set at publication, never by the tag name."
