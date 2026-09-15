// Segmented strips whose label needs no native tooltip.
//
// rc-segmented defaults an item's `title` to its own label text, so hovering a button makes the
// browser draw a native tooltip that repeats it — drawn flush under the button, which reads as one
// control stuck to another rather than as a hint. These strips scroll instead of truncating, so a
// label is always fully visible and a tooltip repeating it could only ever restate the screen.
//
// Pass this ONLY where the tooltip would restate the label. The report-type strips deliberately do
// not: a tab there names the report TYPE, so the one thing it cannot say is WHICH report it opens,
// and they pass the report's own title instead — new information, not a repetition. They fall back
// to this constant for the tab whose report is named exactly what the tab is called.
//
// An EMPTY title is what suppresses it: `undefined` makes rc-segmented fall back to the label
// again, and an empty attribute also stops a title being inherited from an ancestor. Keep the
// default where a label CAN be clipped — there the tooltip is the only way to read it.
export const NO_ITEM_TOOLTIP = ''
