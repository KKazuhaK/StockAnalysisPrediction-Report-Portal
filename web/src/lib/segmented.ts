// Segmented strips whose label needs no native tooltip.
//
// rc-segmented defaults an item's `title` to its own label text, so hovering a button makes the
// browser draw a native tooltip that repeats it — drawn flush under the button, which reads as one
// control stuck to another rather than as a hint. The category and report-type strips scroll
// instead of truncating, so their labels are always fully visible and the tooltip could only ever
// restate what is already on screen.
//
// An EMPTY title is what suppresses it: `undefined` makes rc-segmented fall back to the label
// again, and an empty attribute also stops a title being inherited from an ancestor. Keep the
// default where a label CAN be clipped — there the tooltip is the only way to read it.
export const NO_ITEM_TOOLTIP = ''
