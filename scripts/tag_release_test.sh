#!/bin/sh
# Tests for scripts/tag-release.sh. Run directly: sh scripts/tag_release_test.sh
#
# The tag helper had no test, which is how it kept losing every Markdown heading out of the tag
# annotation for 64 releases without anyone noticing: `git tag -a -F` defaults to `--cleanup=strip`,
# which drops any line starting with `#`. The convention is that `git tag -n99 <tag>` and the file in
# docs/releases say the same thing, and nothing was checking it.
#
# Everything happens in a throwaway git repository, so the real one is untouched.
set -u

scriptdir=$(dirname -- "$0")
script=$(cd "$scriptdir" && pwd)/tag-release.sh

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() {
    fail=$((fail + 1))
    printf 'FAIL: %s\n' "$1"
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
repo="$tmp/repo"
mkdir -p "$repo/docs/releases"
cd "$repo" || exit 1
git init -q .
# Pinned so the test never depends on, or prompts for, the machine's signing setup.
git config user.email "test@example.invalid"
git config user.name "tag helper test"
git config commit.gpgsign false
git config tag.gpgsign false

week=$(date -u +%G.%V | awk -F. '{ printf "%s.%d", $1, $2 + 0 }')
tag="v${week}"

# A note shaped like the real ones: a heading, then h2 sections, then a bullet that itself starts
# with a '-'. Only the '#' lines are at risk, and every real note has several.
cat > "docs/releases/${tag}.md" <<EOF
# ${tag} — a test release

## Upgrade notes

Pull and restart.

## Changed

- the first thing

\`\`\`sh
# a shell comment inside a fence is still a line starting with a hash
echo hi
\`\`\`
EOF
git add -A
git commit -qm "docs: note for ${tag}"

run() { # run the helper, capturing output and status
    LAST=$(sh "$script" "$@" 2>&1)
    STATUS=$?
}

expect_status() { # expected description
    if [ "$STATUS" = "$1" ]; then ok; else bad "$2: exit $STATUS, want $1 -- output: $LAST"; fi
}

contains() { # needle description
    case "$LAST" in
        *"$1"*) ok ;;
        *) bad "$2: output did not mention [$1] -- got: $LAST" ;;
    esac
}

# ---------- --next is read-only ----------
run --next
expect_status 0 "--next succeeds"
if [ "$LAST" = "$tag" ]; then ok; else bad "--next printed [$LAST], want [$tag]"; fi
if [ "$(git tag -l | wc -l | tr -d ' ')" = "0" ]; then ok; else bad "--next created a tag"; fi

# ---------- the derived number cuts a tag ----------
run
expect_status 0 "a bare run succeeds"
contains "push:" "the helper prints the push command it will not run itself"
if [ "$(git for-each-ref refs/remotes | wc -l | tr -d ' ')" = "0" ]; then ok; else bad "the helper pushed"; fi

annotated=$(git cat-file -t "$(git rev-parse "refs/tags/$tag")" 2>/dev/null)
if [ "$annotated" = "tag" ]; then ok; else bad "the tag is $annotated, want an annotated tag"; fi

# The assertion this file exists for: the annotation is the note, headings and all.
git cat-file tag "$tag" | sed '1,/^$/d' > "$tmp/annotation.txt"
if diff -u "docs/releases/${tag}.md" "$tmp/annotation.txt" > "$tmp/diff.txt" 2>&1; then
    ok
else
    bad "the annotation is not the note file:"
    sed -n '1,20p' "$tmp/diff.txt" >&2
fi

# ---------- a version whose note is absent ----------
run "v${week}.7"
expect_status 1 "a number with no note is refused"
contains "no release note" "the refusal names the missing file"

# ---------- a duplicate ----------
run "$tag"
expect_status 1 "an existing tag is refused"
contains "already exists" "the refusal says the tag exists"

# ---------- a note that is not in the tagged commit ----------
cat > "docs/releases/v${week}.9.md" <<EOF
# v${week}.9 — not committed

## Changed

- nothing, this file is deliberately untracked
EOF
run "v${week}.9"
expect_status 1 "a note outside the commit is refused"
contains "does not contain" "the refusal explains the note is not in the commit"

# ---------- the SemVer nudge still fires ----------
run v0.4.73
expect_status 1 "a SemVer tag is refused"
contains "SemVer" "the refusal names the retired numbering"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
