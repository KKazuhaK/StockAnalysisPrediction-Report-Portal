#!/bin/sh
# Build-time glyph-coverage gate. Runs inside the release image, against the fonts that image
# actually installs, and fails the build when a codepoint in the corpus has no covering font.
#
# It has to run here rather than in CI: a GitHub runner has DejaVu, Liberation and Noto Color Emoji
# installed, so the same check passes there while the image ships broken — the exact false negative
# that makes a gate worse than none.
#
# A missing glyph is not loud. wkhtmltopdf draws nothing at all for an uncovered codepoint, so the
# only symptom is a hole in a customer's PDF. That is what this gate exists to make impossible.
#
# Exit codes match scripts/probe-wkhtmltopdf-svg.sh: 2 = a tool or input is missing, 1 = the check
# failed, 0 = pass.
set -eu

corpus=${1:-/usr/local/share/report-portal/font-coverage-corpus.txt}

for tool in fc-list awk; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Required probe tool is missing: $tool" >&2
    exit 2
  fi
done
if [ ! -r "$corpus" ]; then
  echo "Coverage corpus is missing or unreadable: $corpus" >&2
  exit 2
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM

# Only fonts that can actually paint into a PDF count, and a charset alone does not tell you that.
# A colour emoji font claims every emoji codepoint and then renders a blank page, because its base
# glyphs are empty — the ink lives in colour tables PDF has no concept of. Both shipping builds fail
# this way and they fail differently, which is why the filter needs both terms:
#   CBDT/EBDT bitmap strikes (the build Debian packages) -> outline=false, caught by :outline=true
#   COLRv1 layers (the build google/fonts ships)         -> outline=TRUE, glyf present, glyphs empty
# So :outline=true alone would let the COLRv1 build through. FC_COLOR is true for both and false for
# every monochrome face in the set, so :color=false is what makes this a test of "paints ink" rather
# than a test of "has outlines". Verified against all three builds: monochrome passes, CBDT fails
# with 70 missing, COLRv1 fails with 70 missing. Before adding :color=false, COLRv1 passed 623/623
# while U+2705 U+274C U+1F680 U+2B50 all rendered ink=0.
fc-list --format='%{charset}\n' ':outline=true:color=false' >"$work/charsets"
if [ ! -s "$work/charsets" ]; then
  echo "No outline fonts are installed — refusing to pass a vacuous check." >&2
  exit 1
fi

# fc-list emits one line of space-separated `lo-hi` (or bare `cp`) hex ranges per matching font.
# Their union is the set of codepoints fontconfig can resolve, which is what Qt asks it for.
# Reading fontconfig rather than the font files is deliberate: it reflects the *effective* set,
# after Debian's conf.d rules have rejected the PCF bitmap fonts the wkhtmltox deb drags in.
awk -v corpus_name="$corpus" '
  function hex(s,   i, c, v, d) {
    v = 0; s = tolower(s)
    for (i = 1; i <= length(s); i++) {
      c = substr(s, i, 1); d = index("0123456789abcdef", c) - 1
      if (d < 0) { printf "malformed hex in %s: %s\n", FILENAME, s >"/dev/stderr"; exit 2 }
      v = v * 16 + d
    }
    return v
  }
  # Pass 1: the installed charsets. `parts` is local to this block on purpose — an earlier draft
  # reused the corpus counter as the split() result and reported one codepoint more than it checked.
  NR == FNR {
    for (i = 1; i <= NF; i++) {
      parts = split($i, p, "-")
      nranges++
      lo[nranges] = hex(p[1]); hi[nranges] = hex(parts == 2 ? p[2] : p[1])
    }
    next
  }
  /^[ \t]*#/ || /^[ \t]*$/ { next }
  NF != 3 { printf "malformed corpus line %d: expected 3 tab-separated columns\n", FNR >"/dev/stderr"; bad++; next }
  {
    cp = $1; sub(/^[uU]\+/, "", cp); v = hex(cp); tier = $3
    covered = 0
    for (i = 1; i <= nranges; i++) if (v >= lo[i] && v <= hi[i]) { covered = 1; break }
    checked++
    if (covered) next
    if (tier == "advisory") {
      printf "advisory: no installed font covers U+%s (%s)\n", toupper(cp), $2
      warned++
    } else {
      printf "MISSING GLYPH: U+%s (%s, %s)\n", toupper(cp), $2, tier >"/dev/stderr"
      missing++
    }
  }
  END {
    printf "font coverage: %d codepoints checked against %d installed ranges, %d advisory gaps\n", \
      checked, nranges, warned + 0
    if (missing) {
      printf "%d required codepoint(s) have no covering outline font in this image.\n", missing >"/dev/stderr"
      printf "Install a font that covers them, or move them to the advisory tier in %s.\n", corpus_name >"/dev/stderr"
      exit 1
    }
    if (bad) { printf "%d malformed corpus line(s).\n", bad >"/dev/stderr"; exit 2 }
    if (checked == 0) { print "Corpus contained no codepoints — refusing to pass a vacuous check." >"/dev/stderr"; exit 1 }
  }
' "$work/charsets" "$corpus"

echo "Font coverage probe passed against the outline fonts installed in this image."
