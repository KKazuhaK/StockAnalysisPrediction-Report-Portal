import type { KeyboardEvent, SyntheticEvent } from 'react'

// A card that is a clickable <div> is invisible to anyone not using a mouse: it takes no focus, it
// is not announced as anything actionable, and Enter does nothing. antd's `hoverable` + `onClick`
// Card is exactly that shape, and it is how the report feed and the app hub are opened.
//
// This is the minimum that makes such a card a real control — a role so it is announced as a button,
// a tab stop so it can be reached, a name so the announcement says which one, and Enter/Space so it
// can be activated. Spread it onto the Card:
//
//   <Card hoverable {...clickable(open, displayName)}>   // no text of its own: name it
//   <a {...clickable(toggle)}>Show unchanged</a>                 // its text is the name already
//
// Space is preventDefault'd because its default on a focused element is to scroll the page, which
// would otherwise happen instead of (and then as well as) the activation.
// label is optional and should be OMITTED when the element already shows its own text: an aria-label
// replaces the visible name rather than adding to it, so supplying one there is a way to make a
// control announce something different from what it reads.
export function clickable(onActivate: (e: SyntheticEvent) => void, label?: string) {
  return {
    role: 'button',
    tabIndex: 0,
    ...(label ? { 'aria-label': label } : null),
    onClick: onActivate,
    onKeyDown: (e: KeyboardEvent) => {
      if (e.key !== 'Enter' && e.key !== ' ') return
      // A control INSIDE the card (a button, a link) handles its own keys; activating the card too
      // would open the card behind whatever the inner control just did.
      if (e.target !== e.currentTarget) return
      e.preventDefault()
      onActivate(e)
    },
  }
}
