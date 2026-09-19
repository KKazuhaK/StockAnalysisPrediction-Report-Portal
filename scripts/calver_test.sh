#!/bin/sh
# Tests for scripts/lib/calver.sh. Run directly: sh scripts/calver_test.sh
#
# No test framework on purpose: the validator is POSIX shell plus awk so that
# scripts/tag-release.sh keeps working with nothing but git in the PATH, and a
# framework would be a bigger dependency than the code it tests.
set -u

here=$(dirname "$0")
# shellcheck source=lib/calver.sh
. "$here/lib/calver.sh"

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
    fail=$((fail + 1))
    printf 'FAIL: %s\n' "$1"
}

assert_valid() {
    if calver_valid "$1" 2>/dev/null; then ok; else bad "expected a valid CalVer tag: [$1]"; fi
}

assert_invalid() {
    if calver_valid "$1" 2>/dev/null; then bad "expected a rejected tag: [$1]"; else ok; fi
}

assert_eq() { # description expected actual
    if [ "$2" = "$3" ]; then ok; else bad "$1: expected [$2] got [$3]"; fi
}

assert_week53() { # year expected(yes|no)
    if calver_week_exists "$1" 53; then
        if [ "$2" = yes ]; then ok; else bad "$1 should not have an ISO week 53"; fi
    else
        if [ "$2" = no ]; then ok; else bad "$1 should have an ISO week 53"; fi
    fi
}

# ---------- well-formed tags ----------
assert_valid v2026.38.1
assert_valid v2026.1.1
assert_valid v2026.9.2          # single-digit week, no leading zero
assert_valid v2026.38.10        # revision past 9
assert_valid v2026.52.99
assert_valid v2000.1.1

# ---------- week 53 ----------
assert_valid v2020.53.1
assert_valid v2026.53.1
assert_valid v2015.53.1
assert_valid v2032.53.1
assert_invalid v2021.53.1
assert_invalid v2024.53.1
assert_invalid v2025.53.1
assert_invalid v2027.53.1
assert_invalid v2028.53.1

assert_week53 2020 yes
assert_week53 2026 yes
assert_week53 2015 yes
assert_week53 2032 yes
assert_week53 2021 no
assert_week53 2024 no
assert_week53 2025 no
assert_week53 2027 no
assert_week53 2028 no

# ---------- maturity suffixes are never part of the number ----------
assert_invalid v2026.38.1-beta
assert_invalid v2026.38.1-rc.1
assert_invalid v2026.38.1-alpha
assert_invalid v2026.38.1+build
assert_invalid v2026.38.1.2   # extra component
assert_invalid v2026.38.1.0

# ---------- zero, leading zeroes, shape ----------
assert_invalid v2026.38.0
assert_invalid v2026.0.1
assert_invalid v2026.00.1
assert_invalid v2026.54.1
assert_invalid v2026.08.1
assert_invalid v2026.38.01
assert_invalid v2026.38
assert_invalid v2026
assert_invalid v026.38.1
assert_invalid v26.38.1
assert_invalid v2026-38-1
assert_invalid 2026.38.1
assert_invalid v1999.1.1
assert_invalid v0000.1.1
assert_invalid vabc.def.ghi
assert_invalid v0.4.72
assert_invalid ""
assert_invalid v
assert_invalid v2026.38.1-

# ---------- numeric, not lexical ----------
assert_eq "tuple" "2026 38 1" "$(calver_tuple v2026.38.1)"
assert_eq "tuple reformats" "2026 9 2" "$(calver_tuple v2026.9.2)"

assert_eq "9 < 10 (numeric)" "-1" "$(calver_cmp v2026.9.2 v2026.10.1)"
assert_eq "r1 < r2" "-1" "$(calver_cmp v2026.38.1 v2026.38.2)"
assert_eq "r2 < r10" "-1" "$(calver_cmp v2026.38.2 v2026.38.10)"
assert_eq "r9 < next week" "-1" "$(calver_cmp v2026.38.9 v2026.39.1)"
assert_eq "old-series patch trails the newer series" "-1" "$(calver_cmp v2026.38.3 v2026.39.1)"
assert_eq "equal" "0" "$(calver_cmp v2026.38.1 v2026.38.1)"
assert_eq "newer year wins" "1" "$(calver_cmp v2027.1.1 v2026.53.9)"
assert_eq "reverse of the numeric case" "1" "$(calver_cmp v2026.10.1 v2026.9.2)"

if calver_cmp v0.4.72 v2026.38.1 >/dev/null 2>&1; then
    bad "calver_cmp should refuse a non-CalVer operand"
else
    ok
fi

# ---------- sorting merges series correctly ----------
assert_eq "sort" "v2026.9.2
v2026.10.1
v2026.38.1
v2026.38.2
v2026.38.10
v2026.39.1" "$(printf '%s\n' v2026.38.10 v2026.39.1 v2026.9.2 v2026.38.2 v2026.38.1 v2026.10.1 | calver_sort)"

assert_eq "sort drops non-CalVer input" "v2026.38.1
v2026.38.2" "$(printf '%s\n' v2026.38.2 v0.4.72 dev v2026.38.1 ci '' | calver_sort)"

# ---------- legacy / diagnostic tags stay readable ----------
if calver_legacy v0.4.72; then ok; else bad "v0.4.72 should be treated as a legacy tag"; fi
if calver_legacy v0.2.26; then ok; else bad "v0.2.26 should be treated as a legacy tag"; fi
if calver_legacy dev; then ok; else bad "dev should be treated as a non-release tag"; fi
if calver_legacy ci; then ok; else bad "ci should be treated as a non-release tag"; fi
if calver_legacy v2026.38.1; then bad "a CalVer tag is not a legacy tag"; else ok; fi

# ---------- the current week is a CalVer-shaped, self-consistent value ----------
cur=$(calver_current_week)
assert_eq "current week matches date -u +%G.%V" "$(date -u +%G.%V)" "$cur"
assert_valid "v${cur}.1"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
