// Shared spacing scale for the admin console (Manage -> *). Every manage page reads these four
// numbers instead of scattered magic literals, so pages line up with each other. See the console
// consistency plan: GAP_SECTION between top-level sections/Cards, GAP_FIELD for the vertical rhythm
// inside a section, GAP_INLINE for label<->control and button clusters, FORM_MAXW to cap form width.
export const GAP_SECTION = 16
export const GAP_FIELD = 12
export const GAP_INLINE = 8
export const FORM_MAXW = 720
