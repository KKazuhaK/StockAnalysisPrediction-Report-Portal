#!/bin/sh
# Boundary tests for derive_version (scripts/lib/release.sh). Run: sh scripts/release_derive_test.sh
#
# The week is a parameter, which is the point: week 1, week 9, week 53 and a year rollover cannot be
# tested by waiting for them. Each case gets its own throwaway repository so a fixture in one cannot
# quietly satisfy another.
set -u

here=$(dirname -- "$0")
# shellcheck source=lib/calver.sh
. "$here/lib/calver.sh"
# shellcheck source=lib/release.sh
. "$here/lib/release.sh"

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() {
    fail=$((fail + 1))
    printf 'FAIL: %s\n' "$1"
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

repo=""
fresh_repo() {
    repo=$(mktemp -d "$tmp/repo.XXXXXX")
    mkdir -p "$repo/docs/releases"
    (
        cd "$repo" || exit 1
        git init -q .
        git config user.email "test@example.invalid"
        git config user.name "derive test"
        git config commit.gpgsign false
        git config tag.gpgsign false
        git commit -q --allow-empty -m base
    )
}

note() { printf '# %s — fixture\n\n## Changed\n\n- fixture\n' "$1" > "$repo/docs/releases/$1.md"; }
tagged() { (cd "$repo" && git tag -a --cleanup=verbatim "$1" -m "$1"); }

expect_derive() { # week expected description
    got=$(derive_version "$repo" "$1" 2>"$tmp/err.txt")
    status=$?
    if [ "$got" = "$2" ] && [ "$status" = "0" ]; then
        ok
    else
        bad "$3: derived [$got] (exit $status), want [$2] -- $(cat "$tmp/err.txt")"
    fi
}

expect_refusal() { # week needle description
    got=$(derive_version "$repo" "$1" 2>"$tmp/err.txt")
    status=$?
    if [ "$status" = "0" ]; then
        bad "$3: derived [$got] instead of refusing"
    elif ! grep -q "$2" "$tmp/err.txt"; then
        bad "$3: refused without mentioning [$2] -- $(cat "$tmp/err.txt")"
    else
        ok
    fi
}

# ---------- an untouched week ----------
fresh_repo
expect_derive 2026.1 v2026.1 "a week with nothing in it yet needs no revision"

# ---------- the week boundary: a week number is not a prefix ----------
# Week 2026.3 must not see 2026.30 or 2026.31, which is what a `v2026.3*` glob would hand it — the
# over-match that made weeks 1-9 read notes and tags from every week starting with the same digit.
fresh_repo
tagged v2026.30.2
tagged v2026.31.1
note v2026.30.2
expect_derive 2026.3 v2026.3 "week 2026.3 does not see weeks 2026.30 / 2026.31"

# The same fixture one digit up must see them, or the test above proves nothing.
fresh_repo
tagged v2026.30.2
tagged v2026.31.1
expect_derive 2026.30 v2026.30.3 "week 2026.30 does see its own tags"
expect_derive 2026.31 v2026.31.2 "and week 2026.31 sees its own"

# ---------- the note you just wrote is the release you get ----------
fresh_repo
note v2026.38
expect_derive 2026.38 v2026.38 "an untagged note is the next release"

fresh_repo
note v2026.38
note v2026.38.1
expect_derive 2026.38 v2026.38.1 "with two untagged notes, the higher one is cut"

fresh_repo
note v2026.38
tagged v2026.38
expect_derive 2026.38 v2026.38.1 "a tagged note is done; the week continues"

fresh_repo
note v2026.38
tagged v2026.38.1
# v2026.38 is untagged but sits below the already-published v2026.38.1.
expect_refusal 2026.38 "v2026.38.1" "a number below an already-tagged one is refused"

# ---------- crossing ten ----------
fresh_repo
tagged v2026.4.9
expect_derive 2026.4 v2026.4.10 "9 + 1 is ten, not nine-plus-a-character"

fresh_repo
tagged v2026.4.10
expect_derive 2026.4 v2026.4.11 "and ten + 1 is eleven"

fresh_repo
tagged v2026.4.9
tagged v2026.4.10
expect_derive 2026.4 v2026.4.11 "the highest of .9 and .10 is .10, so the next is .11"

fresh_repo
note v2026.4.9
note v2026.4.10
expect_derive 2026.4 v2026.4.10 "pending notes order numerically: .10 beats .9"

fresh_repo
tagged v2026.4.99
expect_derive 2026.4 v2026.4.100 "and a three-digit revision keeps going"

# ---------- week 53, and the year rollover ----------
fresh_repo
expect_derive 2026.53 v2026.53 "2026 has an ISO week 53, so it is derivable"
if calver_valid "$(derive_version "$repo" 2026.53)"; then ok; else bad "the derived week-53 tag is not valid"; fi

fresh_repo
# A week that does not exist is not the derivation's business to police — the clock never names one —
# but the validation that runs immediately after it must refuse, or an impossible number could be
# tagged. This is the composition, not the function in isolation.
if calver_valid "$(derive_version "$repo" 2021.53)"; then
    bad "v2021.53 does not exist in 2021; the validation after the derivation must refuse it"
else
    ok
fi

fresh_repo
tagged v2026.53.9
expect_derive 2027.1 v2027.1 "a new year does not inherit the old year's revisions"

# ---------- what is not a release note ----------
fresh_repo
printf '# Release notes\n\nnot a release\n' > "$repo/docs/releases/README.md"
note v0.4.70
note v2026.38.1
expect_derive 2026.1 v2026.1 "README.md and the legacy v0.x notes are not this week's releases"

# ---------- the escape hatch still works ----------
# Nothing here: tag-release.sh takes an explicit version without calling the derivation at all, which
# is what makes every refusal above recoverable.

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
