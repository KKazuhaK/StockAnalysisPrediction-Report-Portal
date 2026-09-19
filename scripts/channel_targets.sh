#!/bin/sh
# Decide what the rolling image channels should point at, given every published release.
#
# Pure decision logic: it reads the GitHub `/releases` JSON array on stdin, calls no CLI and changes
# nothing, and prints `KEY=value` lines on stdout. That shape lets the same function run against a
# captured fixture offline (scripts/channel_targets_test.sh) and against the live API in the
# reconciliation workflow, which then does only the verification and the registry write.
#
# The rules (docs/adr/0034-calver-baseline-and-database-compatibility-reset.md, section 7.2):
#
#   :latest  the highest eligible FULL CalVer release. An older series' maintenance patch cannot
#            displace a newer recommended version, which is why this is a maximum over the CalVer
#            tuple and not "whatever was published most recently".
#   :beta    the existing compatibility alias — the highest eligible published release, pre-release
#            or full. It is not a product-version suffix; renaming it is separate scope.
#
# A release is eligible only when it is published (a draft is not a release yet), its tag is a
# well-formed CalVer tag, and its assets are complete. An incomplete release is reported, never
# treated as stable: six archives plus SHA256SUMS.txt is the same bar the release job enforces.
#
# Inputs (env):
#   CURRENT_LATEST, CURRENT_BETA   what each channel is applied as right now (durable repo
#                                  variables). Empty means "unknown / never applied".
#   LATEST_OVERRIDE, BETA_OVERRIDE operator pins. When set, the channel must equal the pin and the
#                                  computed candidate is ignored — reconciliation must not undo a
#                                  deliberate withdrawal or rollback.
#   FORCE                          1 to permit a backward move on purpose.
# Outputs (stdout), for `>> "$GITHUB_OUTPUT"`:
#   LATEST_TARGET / BETA_TARGET  what each channel should point at, whether or not it is already
#                                there. Alignment work that is not a channel write — GitHub's own
#                                Latest field — keys off these, so a transient failure is repaired by
#                                the next reconciliation instead of being skipped because the channel
#                                was already correct.
#   LATEST, LATEST_ACTION (set|keep|none|refuse), LATEST_REASON   what to change, now
#   BETA,   BETA_ACTION,   BETA_REASON
#   SKIPPED_INCOMPLETE
set -eu

here=$(dirname -- "$0")
# shellcheck source=lib/calver.sh
. "$here/lib/calver.sh"

tab=$(printf '\t')

# assets_complete TAG ASSET_CSV — the release carries six platform archives named for its own tag,
# plus the checksum file. An archive named for a different tag is a different artifact set.
assets_complete() {
    _tag=$1
    _assets=$2
    for _name in "SHA256SUMS.txt" "release-metadata.json" \
        "report-portal_${_tag}_linux_amd64.tar.gz" \
        "report-portal_${_tag}_linux_arm64.tar.gz" \
        "report-portal_${_tag}_darwin_amd64.tar.gz" \
        "report-portal_${_tag}_darwin_arm64.tar.gz" \
        "report-portal_${_tag}_windows_amd64.zip" \
        "report-portal_${_tag}_windows_arm64.zip"; do
        case ",${_assets}," in
            *",${_name},"*) ;;
            *) return 1 ;;
        esac
    done

}

# resolve NAME CANDIDATE CURRENT OVERRIDE FORCE — prints "action|tag|reason". `tag` is what to
# apply and is empty for every action other than `set`.
resolve() {
    _name=$1
    _cand=$2
    _cur=$3
    _ovr=$4
    _force=$5

    if [ -n "$_ovr" ]; then
        if ! calver_valid "$_ovr"; then
            printf 'refuse||%s_OVERRIDE is %s, which is not a CalVer tag\n' "$_name" "$_ovr"
            return 0
        fi
        if [ "$_ovr" = "$_cur" ]; then
            printf 'keep||operator override %s is already applied\n' "$_ovr"
        else
            printf 'set|%s|operator override (candidate was %s)\n' "$_ovr" "${_cand:-none}"
        fi
        return 0
    fi

    # No full CalVer release yet: leave the legacy stable target alone rather than guess at it.
    if [ -z "$_cand" ]; then
        printf 'none||no eligible published release yet\n'
        return 0
    fi
    if [ "$_cand" = "$_cur" ]; then
        printf 'keep||%s is already applied\n' "$_cand"
        return 0
    fi
    # A stale or duplicated event can arrive after a newer release was already applied. Serializing
    # the jobs does not fix that on its own — the ordering has to be rechecked here.
    if [ -n "$_cur" ] && calver_valid "$_cur" && [ "$(calver_cmp "$_cand" "$_cur")" = "-1" ]; then
        if [ "$_force" = "1" ]; then
            printf 'set|%s|older than the applied %s, forced\n' "$_cand" "$_cur"
        else
            printf 'refuse||%s is older than the applied %s; set FORCE to roll back deliberately\n' \
                "$_cand" "$_cur"
        fi
        return 0
    fi
    printf 'set|%s|\n' "$_cand"
}

releases=$(jq -r '
    .[]
    | select(.draft == false)
    | select(.tag_name != null)
    | [ .tag_name,
        (if .prerelease then "1" else "0" end),
        ([ .assets[].name ] | join(","))
      ] | @tsv
')

full_candidates=""
all_candidates=""
skipped=""

# A `while read` fed by a pipe would run in a subshell and lose the accumulators, so feed it from a
# here-document instead.
while IFS="$tab" read -r tag pre assets; do
    [ -n "$tag" ] || continue
    if ! calver_valid "$tag"; then
        continue # legacy v0.x history and dev/ci diagnostics: readable, never a channel target
    fi
    if ! assets_complete "$tag" "$assets"; then
        if [ -n "$skipped" ]; then skipped="${skipped},${tag}"; else skipped=$tag; fi
        continue
    fi
    all_candidates="${all_candidates}${tag}
"
    if [ "$pre" = "0" ]; then
        full_candidates="${full_candidates}${tag}
"
    fi
done <<EOF
$releases
EOF

cand_latest=$(printf '%s' "$full_candidates" | calver_sort | tail -n 1)
cand_beta=$(printf '%s' "$all_candidates" | calver_sort | tail -n 1)

latest=$(resolve LATEST "$cand_latest" "${CURRENT_LATEST:-}" "${LATEST_OVERRIDE:-}" "${FORCE:-0}")
beta=$(resolve BETA "$cand_beta" "${CURRENT_BETA:-}" "${BETA_OVERRIDE:-}" "${FORCE:-0}")

# What each channel should point at afterwards, which is not the same question as what to change: a
# `keep` has a target and nothing to apply, and the work that depends on the target has to happen
# either way.
target_of() { # action override candidate
    case "$1" in
        # `set` and `keep` differ in what they APPLY, not in where the channel ends up: both end at
        # the operator's pin when there is one and at the computed candidate otherwise. Reporting an
        # empty target for a plain `set` is how the workflow skipped its own verification and write
        # steps while still reporting success.
        set | keep)
            if [ -n "$2" ]; then printf '%s\n' "$2"; else printf '%s\n' "$3"; fi
            ;;
        *) printf '\n' ;;
    esac
}

printf 'LATEST=%s\n' "$(printf '%s' "${latest#*|}" | cut -d'|' -f1)"
printf 'LATEST_TARGET=%s\n' "$(target_of "${latest%%|*}" "${LATEST_OVERRIDE:-}" "$cand_latest")"
printf 'LATEST_ACTION=%s\n' "${latest%%|*}"
printf 'LATEST_REASON=%s\n' "$(printf '%s' "${latest##*|}" | tr -d '\n')"
printf 'BETA=%s\n' "$(printf '%s' "${beta#*|}" | cut -d'|' -f1)"
printf 'BETA_TARGET=%s\n' "$(target_of "${beta%%|*}" "${BETA_OVERRIDE:-}" "$cand_beta")"
printf 'BETA_ACTION=%s\n' "${beta%%|*}"
printf 'BETA_REASON=%s\n' "$(printf '%s' "${beta##*|}" | tr -d '\n')"
printf 'SKIPPED_INCOMPLETE=%s\n' "$skipped"
