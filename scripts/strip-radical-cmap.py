#!/usr/bin/env python3
"""Drop the Kangxi/CJK radical blocks from a CJK font's cmap.

WHY THIS EXISTS

Noto Sans CJK SC maps two different codepoint ranges onto the *same* glyphs:
the unified ideographs (月 U+6708) and the radical blocks that look identical
(⽉ KANGXI RADICAL MOON U+2F49). That is correct as font data — the radical
blocks exist so dictionaries can address a radical directly.

It breaks the PDF text layer. wkhtmltopdf's Qt builds the ToUnicode CMap by
reverse-mapping glyph -> codepoint, and where a glyph has several codepoints it
takes the lowest, which is always the radical. The page *looks* right, because
both codepoints draw the same glyph, but the text underneath is wrong: every 月
in an exported report came out as ⽉. Ctrl+F finds nothing, copy-paste yields
characters nobody typed, and anything downstream that reads the text — search,
indexing, a model re-reading its own report, a screen reader — gets corrupted
input. Measured on a real export: zero occurrences of 一月无行, and 96 of their
radical lookalikes.

Removing these entries makes the reverse mapping unambiguous, so Qt has only the
ideograph to choose. Nothing that renders today stops rendering: the glyphs stay,
only the second way of addressing them goes. Real Chinese prose never uses these
codepoints, and scripts/font-coverage-corpus.txt does not list one.

Verified before and after against the real font: 658 mappings removed, file size
16,437,364 -> 16,432,776 bytes, and pdftotext round-trips 一月无行目风长门贝车
exactly instead of returning radicals.
"""

import sys

from fontTools.ttLib import TTFont

# CJK Radicals Supplement, then Kangxi Radicals.
RADICAL_RANGES = ((0x2E80, 0x2EF3), (0x2F00, 0x2FD5))


def in_radical_block(codepoint: int) -> bool:
    return any(lo <= codepoint <= hi for lo, hi in RADICAL_RANGES)


def main(argv: list[str]) -> int:
    if len(argv) != 3:
        print(f"usage: {argv[0]} <in.otf> <out.otf>", file=sys.stderr)
        return 2

    src, dst = argv[1], argv[2]
    font = TTFont(src)

    removed = 0
    for subtable in font["cmap"].tables:
        doomed = [cp for cp in subtable.cmap if in_radical_block(cp)]
        for cp in doomed:
            del subtable.cmap[cp]
        removed += len(doomed)

    if removed == 0:
        # Not fatal on its own, but it means the assumption behind this script no
        # longer holds for this font, and the text layer is probably fine without
        # it. Say so loudly rather than silently doing nothing.
        print(f"{src}: no radical-block mappings found — is this still a Noto CJK build?", file=sys.stderr)
        return 1

    font.save(dst)
    print(f"{src}: removed {removed} radical-block cmap entries -> {dst}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
