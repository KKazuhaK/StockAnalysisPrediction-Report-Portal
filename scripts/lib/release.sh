#!/bin/sh
# Choosing a release number for a repository.
#
# Sourced, never executed. Kept apart from lib/calver.sh so that file stays a pure shell-plus-awk
# library the release workflows can call without a checkout: everything here needs git.
#
# Sourced by scripts/tag-release.sh and by scripts/tag_release_test.sh.

# in_list NEEDLE HAYSTACK — the haystack is whitespace-separated words.
in_list() {
    for _x in $2; do
        if [ "$_x" = "$1" ]; then return 0; fi
    done
    return 1
}

# in_week TAG WEEK — is TAG's number inside WEEK's series?
#
# A prefix test is not enough and gets the answer wrong for a third of the year. A week has no
# leading zero, so "v2026.3" is a prefix of "v2026.30" and "v2026.31": in weeks 1 through 9 the
# derivation would see every note and tag from weeks 10 to 90 that happens to start with the same
# digit, and could hand back another week's number.
in_week() {
    case "$1" in
        "v$2" | "v$2".*) return 0 ;;
        *) return 1 ;;
    esac
}

# derive_version ROOT WEEK — prints the number a maintainer would pick by hand, or fails with the
# reason on stderr.
#
# WEEK is a parameter rather than something read from the clock here, so the boundaries — week 1,
# week 9, week 53, a year rollover — are tested by naming them instead of by waiting for them.
#
# Only the week is computed; the revision comes from what this checkout already has. The rule is the
# first one a person would apply: cut the note you just wrote, meaning the highest release note for
# this week with no tag on it yet. With every note for the week already tagged, the week is
# continuing rather than starting, so it is the revision after everything this week has.
#
# It reads local tags and the working tree only. A clone that has not fetched cannot see a tag cut
# elsewhere, which is why naming the version explicitly stays the escape hatch.
derive_version() {
    _root=$1
    _week=$2

    _notes=""
    for _f in "$_root"/docs/releases/*.md; do
        [ -f "$_f" ] || continue # an unmatched glob arrives here as its own literal text
        _t=$(basename "$_f" .md)
        if in_week "$_t" "$_week" && calver_valid "$_t"; then
            _notes="$_notes$_t
"
        fi
    done

    _tags=""
    for _t in $(git -C "$_root" tag -l); do
        if in_week "$_t" "$_week" && calver_valid "$_t"; then
            _tags="$_tags$_t
"
        fi
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
        # A week can legitimately start at a revision: nothing stops a maintainer from naming the
        # week's first release v2026.3.1. What cannot happen is tagging v2026.3 AFTER v2026.3.1 was
        # published, because that is a release whose number sits below one already out there — the
        # channel reconciliation would refuse to move, and the number would never be usable.
        _tagged_max=$(printf '%s' "$_tags" | calver_sort | tail -n 1)
        if [ -n "$_tagged_max" ] && [ "$(calver_cmp "$_pick" "$_tagged_max")" = "-1" ]; then
            printf 'error: this week already has %s tagged, so %s would sit below it.\n' \
                "$_tagged_max" "$_pick" >&2
            printf '       Pass %s as the version to cut it anyway.\n' "$_pick" >&2
            return 1
        fi
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
