#!/bin/sh
# Tests for scripts/channel_targets.sh — the rolling-channel decision logic.
#
# Fixtures are built here rather than checked in: they are small, and a checked-in JSON blob drifts
# from the GitHub shape without anyone noticing. Run directly: sh scripts/channel_targets_test.sh
set -u

here=$(dirname -- "$0")
targets="$here/channel_targets.sh"

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
    fail=$((fail + 1))
    printf 'FAIL: %s\n' "$1"
}

# mk TAG PRERELEASE DRAFT COMPLETE — one release in GitHub's /releases shape.
mk() {
    jq -n --arg tag "$1" --arg pre "$2" --arg draft "$3" --arg comp "$4" '
      {
        tag_name: $tag,
        draft: ($draft == "yes"),
        prerelease: ($pre == "yes"),
        assets: (
          if $comp == "yes" then
            [ "report-portal_\($tag)_linux_amd64.tar.gz",
              "report-portal_\($tag)_linux_arm64.tar.gz",
              "report-portal_\($tag)_darwin_amd64.tar.gz",
              "report-portal_\($tag)_darwin_arm64.tar.gz",
              "report-portal_\($tag)_windows_amd64.zip",
              "report-portal_\($tag)_windows_arm64.zip",
              "SHA256SUMS.txt" ]
            | map({ name: . })
          else
            [ { name: "SHA256SUMS.txt" } ]
          end
        )
      }'
}

fixture() { # groups of four: TAG PRERELEASE DRAFT COMPLETE
    out="["
    while [ $# -ge 4 ]; do
        if [ "$out" != "[" ]; then out="$out,"; fi
        out="$out$(mk "$1" "$2" "$3" "$4")"
        shift 4
    done
    printf '%s]\n' "$out"
}

LAST=""
run() { LAST=$(printf '%s\n' "$1" | "$targets"); }

clear_env() {
    unset CURRENT_LATEST CURRENT_BETA LATEST_OVERRIDE BETA_OVERRIDE FORCE
}
field() { printf '%s\n' "$LAST" | sed -n "s/^$1=//p"; }

expect() { # KEY EXPECTED DESCRIPTION
    got=$(field "$1")
    if [ "$got" = "$2" ]; then ok; else bad "$3: $1 expected [$2] got [$got]"; fi
}

# ---------- nothing published yet ----------
clear_env
run '[]'
expect LATEST_ACTION none "empty repository leaves :latest alone"
expect BETA_ACTION none "empty repository leaves :beta alone"
expect LATEST "" "empty repository applies nothing"

# A draft is not a release: publishing it is what makes it eligible.
run "$(fixture v2026.38.1 no yes yes)"
expect LATEST_ACTION none "a draft full release is not eligible"
expect BETA_ACTION none "a draft is not eligible for :beta either"

# ---------- a pre-release alone feeds :beta, never :latest ----------
run "$(fixture v2026.38.1 yes no yes)"
expect LATEST_ACTION none "a pre-release never becomes :latest"
expect BETA_ACTION set "a pre-release feeds :beta"
expect BETA v2026.38.1 "beta is the pre-release"

# ---------- full release below a later pre-release ----------
run "$(fixture v2026.38.1 no no yes  v2026.39.1 yes no yes)"
expect LATEST v2026.38.1 "the highest FULL release is :latest"
expect LATEST_ACTION set ":latest moves to the full release"
expect BETA v2026.39.1 ":beta takes the newer pre-release"

# ---------- an old-series patch cannot displace a newer series ----------
clear_env
run "$(fixture v2026.38.3 no no yes  v2026.39.1 no no yes)"
expect LATEST v2026.39.1 "a later-published old-series patch does not become :latest"
expect BETA v2026.39.1 "beta follows the highest tuple, not publication order"

# ---------- ordering: a stale event must not move a channel backward ----------
clear_env
export CURRENT_LATEST=v2026.39.1
run "$(fixture v2026.38.2 no no yes)"
expect LATEST_ACTION refuse "an older release does not move :latest backward"
expect LATEST "" "a refused move applies nothing"
expect LATEST_REASON "v2026.38.2 is older than the applied v2026.39.1; set FORCE to roll back deliberately" \
    "the refusal says why"

run_forced() { LAST=$(printf '%s\n' "$1" | FORCE=1 "$targets"); }
run_forced "$(fixture v2026.38.2 no no yes)"
expect LATEST_ACTION set "FORCE allows a deliberate rollback"
expect LATEST v2026.38.2 "the forced rollback applies the older tag"
clear_env

# ---------- the applied tag is a no-op, not a re-push ----------
export CURRENT_LATEST=v2026.38.2
run "$(fixture v2026.38.2 no no yes)"
expect LATEST_ACTION keep "re-applying the current tag is a no-op"
expect LATEST "" "a keep applies nothing"
clear_env

# ---------- the legacy stable target survives until a full CalVer release exists ----------
export CURRENT_LATEST=v0.4.70
run "$(fixture v2026.38.1 yes no yes)"
expect LATEST_ACTION none "only a pre-release: the legacy :latest is left in place"
run "$(fixture v2026.38.1 no no yes)"
expect LATEST_ACTION set "the first full CalVer release takes :latest over"
expect LATEST v2026.38.1 "and it takes the legacy target over explicitly"
clear_env

# ---------- legacy tags are readable but never channel targets ----------
run "$(fixture v0.4.70 no no yes  dev no no yes)"
expect LATEST_ACTION none "legacy tags are never channel targets"
expect BETA_ACTION none "legacy tags are never :beta targets"
expect SKIPPED_INCOMPLETE "" "a legacy tag is skipped, not reported as incomplete"

# ---------- an incomplete release is reported, never treated as stable ----------
run "$(fixture v2026.40.1 no no no  v2026.38.1 no no yes)"
expect LATEST v2026.38.1 "an incomplete release cannot become :latest"
expect SKIPPED_INCOMPLETE v2026.40.1 "the incomplete release is named"

# An archive named for a different tag is not this release's artifact set.
stranger=$(jq -n '{
    tag_name: "v2026.38.1", draft: false, prerelease: false,
    assets: [ { name: "report-portal_v2026.39.9_linux_amd64.tar.gz" },
              { name: "report-portal_v2026.39.9_linux_arm64.tar.gz" },
              { name: "report-portal_v2026.39.9_darwin_amd64.tar.gz" },
              { name: "report-portal_v2026.39.9_darwin_arm64.tar.gz" },
              { name: "report-portal_v2026.39.9_windows_amd64.zip" },
              { name: "report-portal_v2026.39.9_windows_arm64.zip" },
              { name: "SHA256SUMS.txt" } ] }')
run "[$stranger]"
expect LATEST_ACTION none "a release whose archives belong to another tag is skipped"
expect SKIPPED_INCOMPLETE v2026.38.1 "and it is reported"

# ---------- operator overrides are applied and never undone ----------
clear_env
export LATEST_OVERRIDE=v2026.30.1
run "$(fixture v2026.38.1 no no yes)"
expect LATEST_ACTION set "an override is applied even against a newer candidate"
expect LATEST v2026.30.1 "the override is the applied tag"
case "$(field LATEST_REASON)" in
    *override*) ok ;;
    *) bad "the reason names the override: $(field LATEST_REASON)" ;;
esac

export CURRENT_LATEST=v2026.30.1
run "$(fixture v2026.38.1 no no yes)"
expect LATEST_ACTION keep "an applied override is not undone by reconciliation"
expect LATEST "" "a held override applies nothing"

unset LATEST_OVERRIDE
export LATEST_OVERRIDE=not-a-calver-tag
run "$(fixture v2026.38.1 no no yes)"
expect LATEST_ACTION refuse "an unusable override refuses rather than guessing"
expect LATEST "" "a refused override applies nothing"
clear_env

clear_env
export BETA_OVERRIDE=v2026.31.1
run "$(fixture v2026.38.1 no no yes)"
expect BETA_ACTION set "beta honours its own override"
expect BETA v2026.31.1 "beta applies the override"
clear_env

# ---------- both channels move in one reconciliation ----------
run "$(fixture v2026.38.1 no no yes  v2026.38.2 yes no yes)"
expect LATEST v2026.38.1 "latest takes the full release"
expect BETA v2026.38.2 "beta takes the newer pre-release of the same series"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
