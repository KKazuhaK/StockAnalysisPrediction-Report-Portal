#!/bin/sh
# CalVer (vYYYY.W.R) parsing, validation and ordering for the release scripts.
#
# Sourced, never executed. POSIX shell plus awk only, so scripts/tag-release.sh keeps working
# with nothing but git in the PATH, and the release workflows call these same functions rather
# than carrying a second copy of the rules in YAML.
#
# The contract (docs/adr/0034-calver-baseline-and-database-compatibility-reset.md):
#   - YYYY is the ISO week-numbering year; W is the UTC ISO week the series starts in.
#   - R starts at 1 and rises for every changed published artifact set.
#   - No leading zeroes, no maturity suffix, no extra components.
#   - A series keeps its year/week across delayed publication and maintenance, so revision
#     gaps are legal and published numbers are never recycled.
#   - Comparison is numeric on (YYYY, W, R), never lexical. A larger number does not imply a
#     more stable release: maturity lives in GitHub Release metadata, not in the tag.

# The awk program is one string so every entry point below shares a single parser. It is
# single-quoted with no apostrophes, quotes, dollars or backslashes inside, so it can be
# pasted into a double-quoted context safely.
CALVER_AWK='
function is_leap(y) { return (y % 4 == 0 && y % 100 != 0) || y % 400 == 0 }
function dow(y, m, d,   t) {
    if (m == 1) t = 0; else if (m == 2) t = 3; else if (m == 3) t = 2
    else if (m == 4) t = 5; else if (m == 5) t = 0; else if (m == 6) t = 3
    else if (m == 7) t = 5; else if (m == 8) t = 1; else if (m == 9) t = 4
    else if (m == 10) t = 6; else if (m == 11) t = 2; else t = 4
    if (m < 3) y--
    return (y + int(y / 4) - int(y / 100) + int(y / 400) + t + d) % 7
}
# ISO 8601: a year carries a week 53 only if it starts on a Thursday, or starts on a
# Wednesday in a leap year. dow() returns 0=Sunday..6=Saturday, so Thursday is 4 and
# Wednesday is 3. Computed arithmetically: neither GNU date -d nor a calendar table is
# available on every machine the release scripts run on.
function week53(y,   w) {
    w = dow(y, 1, 1)
    return (w == 4) || (is_leap(y) && w == 3)
}
# calver_parse(tag) fills _y/_w/_r and returns 1 for a well-formed tag, 0 otherwise.
function calver_parse(tag) {
    if (tag !~ /^v[0-9][0-9][0-9][0-9]\.[0-9][0-9]?\.[0-9]+$/) return 0
    split(substr(tag, 2), _f, ".")
    if (_f[1] ~ /^0/) return 0
    if (_f[2] ~ /^0/) return 0
    if (_f[3] ~ /^0/) return 0
    _y = _f[1] + 0; _w = _f[2] + 0; _r = _f[3] + 0
    if (_y < 2000) return 0
    if (_w < 1 || _w > 53) return 0
    if (_w == 53 && !week53(_y)) return 0
    if (_r < 1) return 0
    return 1
}
'

# calver_valid TAG — exits 0 when TAG is a well-formed CalVer release tag.
calver_valid() {
    awk -v tag="$1" "$CALVER_AWK"'
BEGIN { exit(calver_parse(tag) ? 0 : 1) }
' /dev/null
}

# calver_week_exists YYYY W — exits 0 when ISO week W exists in year YYYY.
calver_week_exists() {
    awk -v y="$1" -v w="$2" "$CALVER_AWK"'
BEGIN {
    y += 0; w += 0
    if (w < 1 || w > 53) exit 1
    if (w == 53) exit(week53(y) ? 0 : 1)
    exit 0
}
' /dev/null
}

# calver_tuple TAG — prints the canonical "YYYY W R" tuple, for shell comparisons and logs.
calver_tuple() {
    awk -v tag="$1" "$CALVER_AWK"'
BEGIN {
    if (!calver_parse(tag)) exit 1
    printf "%d %d %d\n", _y, _w, _r
}
' /dev/null
}

# calver_cmp A B — prints -1, 0 or 1. Exits 1, printing nothing, if either operand is not a
# CalVer tag, so a caller cannot accidentally order a legacy tag against a new one.
calver_cmp() {
    awk -v a="$1" -v b="$2" "$CALVER_AWK"'
BEGIN {
    if (!calver_parse(a)) exit 1
    ay = _y; aw = _w; ar = _r
    if (!calver_parse(b)) exit 1
    by = _y; bw = _w; br = _r
    if (ay != by) { print (ay < by) ? -1 : 1; exit 0 }
    if (aw != bw) { print (aw < bw) ? -1 : 1; exit 0 }
    if (ar != br) { print (ar < br) ? -1 : 1; exit 0 }
    print 0
}
' /dev/null
}

# calver_sort — reads tags on stdin, prints the well-formed ones in ascending numeric order.
# Anything that is not a CalVer tag (legacy SemVer history, dev/ci diagnostics, blank lines)
# is dropped rather than guessed at.
calver_sort() {
    awk "$CALVER_AWK"'
{ if (calver_parse($0)) printf "%06d %06d %09d %s\n", _y, _w, _r, $0 }
' | sort | cut -d' ' -f4-
}

# calver_current_week — prints the current UTC ISO week as "YYYY.W", with no leading zero
# on the week (%V pads it, CalVer does not).
calver_current_week() {
    date -u +%G.%V | awk -F. '{ printf "%s.%d\n", $1, $2 + 0 }'
}

# calver_legacy — exits 0 for any tag that is not a CalVer release tag. Channel selection
# uses this to skip the whole v0.x.y history and the dev/ci diagnostic tags without
# pretending to order them.
calver_legacy() {
    calver_valid "$1" && return 1
    return 0
}
