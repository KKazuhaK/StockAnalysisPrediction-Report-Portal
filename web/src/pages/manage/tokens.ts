// Shared spacing scale for the admin console (Manage -> *): GAP_SECTION between top-level
// sections/Cards, GAP_FIELD for the vertical rhythm inside a section, GAP_INLINE for
// label<->control and button clusters, FORM_MAXW to cap form width.
//
// Only GAP_FIELD is adopted so far, by RunQueueSettingsPage. The scale is kept whole rather than
// trimmed to what is used: three of four constants reading as "dead" is a page-migration that never
// happened, not four numbers that want deleting one at a time.
export const GAP_SECTION = 16
export const GAP_FIELD = 12
export const GAP_INLINE = 8
export const FORM_MAXW = 720
