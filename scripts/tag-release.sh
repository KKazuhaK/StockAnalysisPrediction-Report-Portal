#!/bin/sh
# Cut an annotated release tag from its release note.
#
# The convention this encodes (see docs/releases/README.md): a release tag is ANNOTATED and its
# message is the release note verbatim, so `git tag -n99 vX.Y.Z` and the file in docs/releases/ say
# the same thing. Placed on the commit where that release's work is complete — usually the merge
# that brought it into main.
#
# Until now that lived in whoever cut the last one's memory, which is why v0.4.42 and v0.4.43 sat on
# main untagged.
#
#   scripts/tag-release.sh v0.4.42 dacc4328    # tag that commit
#   scripts/tag-release.sh v0.4.42             # tag HEAD
#
# It does not push. Pushing a tag is the one irreversible step here, so it stays a separate,
# deliberate command — which the script prints for you.
set -eu

usage() {
    echo "usage: $0 <version> [commit]" >&2
    echo "  e.g. $0 v0.4.42 dacc4328   (commit defaults to HEAD)" >&2
    exit 2
}

[ $# -ge 1 ] || usage
version=$1
commit=${2:-HEAD}

case "$version" in
    v*.*.*) ;;
    *) echo "error: '$version' is not a vX.Y.Z tag name" >&2; exit 1 ;;
esac

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

git tag -a "$version" "$sha" -F "$note"

echo "created $version -> $(git rev-parse --short "$sha")  $(git log -1 --format=%s "$sha")"
echo
echo "review:  git tag -n99 $version | head"
echo "push:    git push origin $version"
