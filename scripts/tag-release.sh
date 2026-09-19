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
#   scripts/tag-release.sh                 derives this week's number and tags HEAD
#   scripts/tag-release.sh dacc4328        the same, on that commit
#   scripts/tag-release.sh --next          prints the number it would derive, and changes nothing
#   scripts/tag-release.sh v2026.38.2      that exact number, without deriving anything
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
    echo "usage: $0 [--next] [version] [commit]" >&2
    echo "  version is optional: without it the number is derived from this week and the tags and" >&2
    echo "  release notes this checkout already has. This week is $(calver_current_week)." >&2
    echo "  e.g. $0            # derive, tag HEAD" >&2
    echo "       $0 dacc4328   # derive, tag that commit" >&2
    echo "       $0 --next     # just print the number" >&2
    exit 2
}

next_only=0
explicit=0
version=""
commit="HEAD"

for arg in "$@"; do
    case "$arg" in
        --next) next_only=1 ;;
        -h | --help) usage ;;
        # A version is the only argument that starts with a v; anything else is a revision.
        v*)
            version=$arg
            explicit=1
            ;;
        *) commit=$arg ;;
    esac
done

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

if [ "$explicit" = 1 ] && semver_like "$version"; then
    echo "error: '$version' is a SemVer tag. The v0.x line ended at v0.4.72, which is the" >&2
    echo "       database bridge and is not a CalVer release. New tags are vYYYY.W[.R]:" >&2
    echo "       this week is $(calver_current_week), so e.g. v$(calver_current_week)" >&2
    exit 1
fi

root=$(git rev-parse --show-toplevel)

# in_list NEEDLE HAYSTACK — the haystack is newline-separated words.
in_list() {
    for _x in $2; do
        if [ "$_x" = "$1" ]; then return 0; fi
    done
    return 1
}

# derive_version — the number a maintainer would pick by hand, from the clock and this checkout.
#
# Only the WEEK is computed: the clock knows it and nothing else decides it. The revision comes from
# what is already here — release notes written for this week, and tags already cut for it — and the
# rule is the first one a person would apply: cut the note you just wrote. That is the highest
# release note for this week with no tag on it yet. With every note already tagged, the week is
# continuing rather than starting, so it is the revision after everything this week already has.
#
# It reads local tags and the working tree only. A clone that has not fetched cannot see a tag cut
# elsewhere, which is why naming the version explicitly still works and stays the escape hatch.
derive_version() {
    _week=$(calver_current_week)

    _notes=""
    for _f in "$root"/docs/releases/v"$_week"*.md; do
        [ -f "$_f" ] || continue # an unmatched glob arrives here as its own literal text
        _t=$(basename "$_f" .md)
        if calver_valid "$_t"; then _notes="$_notes$_t
"; fi
    done

    _tags=""
    for _t in $(git tag -l "v${_week}*"); do
        if calver_valid "$_t"; then _tags="$_tags$_t
"; fi
    done

    _pending=""
    for _t in $_notes; do
        if ! in_list "$_t" "$_tags"; then
            _pending="$_pending$_t
"
        fi
    done
    _pick=$(printf '%s' "$_pending" | calver_sort | tail -n 1)
    if [ -n "$_pick" ]; then
        printf '%s\n' "$_pick"
        return 0
    fi

    _max=$(printf '%s%s' "$_notes" "$_tags" | calver_sort | tail -n 1)
    if [ -z "$_max" ]; then
        printf 'v%s\n' "$_week" # nothing this week yet: no revision needed
        return 0
    fi
    printf 'v%s.%d\n' "$_week" "$(( $(calver_tuple "$_max" | awk '{print $3}') + 1 ))"
}

if [ -z "$version" ]; then
    version=$(derive_version)
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

if [ "$next_only" = 1 ]; then
    printf '%s\n' "$version"
    exit 0
fi

note="$root/docs/releases/$version.md"

[ -f "$note" ] || {
    echo "error: no release note at docs/releases/$version.md" >&2
    echo "       write it, commit it, then run this again — the tag's message is that file" >&2
    exit 1
}

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
# --cleanup=verbatim, not the default. `git tag -a -F` strips every line starting with '#' unless
# told otherwise, which silently deleted each Markdown heading from the annotation — and any command
# example with a shell comment in it. The convention is that the annotation IS the note, so the note
# is what gets stored, unedited.
git tag -a --cleanup=verbatim "$version" "$sha" -F "$notes"

echo "created $version -> $(git rev-parse --short "$sha")  $(git log -1 --format=%s "$sha")"
echo
echo "review:  git tag -n99 $version | head"
echo "push:    git push origin $version"
echo "publish: the tag push prepares a DRAFT release; maturity (pre-release / full release) and the"
echo "         :latest and :beta channels are set at publication, never by the tag name."
