import type { Principal } from '../api/types'

// An audience is chosen the same way in two places — a site announcement and a hand-written report —
// and both address the same thing: an OU or one account, in the `g:<id>` / `u:<name>` encoding the
// store keeps in a single column. The server validates them through one function (normalizePrincipals
// in announcement_api.go) for the reason that matters: it is the input to a disclosure decision.
// These are the display half of the same idea, so the two pickers cannot drift into listing the same
// principals under different names.

export interface PrincipalLabels {
  groups: string // heading over the OU options
  users: string // heading over the account options
  restricted: string // tag marking an OU whose members' reads are scoped
}

/**
 * principalOptions builds the grouped option list a `Select mode="multiple"` renders. The option
 * VALUE is the principal string itself, which is exactly what the server stores — no id-to-principal
 * mapping to get wrong on either side of the round trip.
 */
export function principalOptions(groups: Principal[], users: Principal[], labels: PrincipalLabels) {
  return [
    {
      label: labels.groups,
      options: groups.map((g) => ({
        value: g.principal,
        label: g.restricted ? `${g.name} · ${labels.restricted}` : g.name,
      })),
    },
    {
      label: labels.users,
      options: users.map((u) => ({
        value: u.principal,
        label: u.display && u.display !== u.name ? `${u.display} (${u.name})` : u.name,
      })),
    },
  ]
}

/**
 * principalName renders one stored principal for reading. Falls back to the raw string, which is
 * what a principal whose group or account has since been deleted looks like — printing `g:7` is
 * ugly and correct, where printing nothing would quietly drop a recipient from a list somebody is
 * reading to check who a thing goes to.
 */
export function principalName(groups: Principal[], users: Principal[], p: string): string {
  return groups.find((g) => g.principal === p)?.name ?? users.find((u) => u.principal === p)?.name ?? p
}
