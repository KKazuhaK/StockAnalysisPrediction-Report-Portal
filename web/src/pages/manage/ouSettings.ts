import type { UserGroupRow } from '../../api/types'

// Resolving what an organizational unit's settings actually ARE.
//
// Two inheritance models coexist here and they are not the same, which is most of why this screen
// was hard to read:
//
//   - run governance (urgent policy, ticket weight, max queued, run window, priority) is null =
//     inherit the DEFAULT group — group model B, a flat fallback, nothing to do with the OU tree;
//   - tenancy (restricted, daily quota) inherits down the OU TREE from the parent (ADR 0022), and
//     the server already resolves `restricted_effective` for us.
//
// Every caller used to re-derive these inline, which is how the urgent tags came to contradict each
// other: a ticket count and "unlimited" were rendered by two independent conditions, so an OU
// inheriting weight 2 from Default while setting unlimited itself displayed both. The urgent policy
// is a THREE-way choice, so it has to be resolved to one answer before anything renders it.

export type UrgentPolicy = 'off' | 'ticket' | 'unlimited'

export interface Resolved<T> {
  value: T
  /** True when this OU does not set the value itself. */
  inherited: boolean
}

export interface OUSettings {
  urgent: Resolved<UrgentPolicy>
  /** Tickets per period. Only meaningful when urgent.value === 'ticket'. */
  weight: Resolved<number>
  maxQueued: Resolved<number>
  runWindow: Resolved<string>
  priority: Resolved<number | null>
  dailyQuota: Resolved<number>
  /** The window dailyQuota covers. Always resolved from the same OU as the number. */
  quotaPeriod: Resolved<string>
}

/**
 * resolveOU answers, for one OU, what each setting is and whether it was inherited.
 *
 * `groups` is the whole tree, needed only by the settings that inherit down it (the quota). Without
 * it those fall back to the permissive baseline, which is what every caller got before.
 */
export function resolveOU(g: UserGroupRow, def?: UserGroupRow, groups?: UserGroupRow[]): OUSettings {
  const isDefault = !!g.is_default
  // The Default group inherits from nobody: it IS the fallback, so a null there is simply unset.
  const own = <T>(v: T | null | undefined, fallback: T): Resolved<T> =>
    v == null && !isDefault ? { value: fallback, inherited: true } : { value: (v ?? fallback) as T, inherited: false }

  const allow = own(g.allow_urgent, def?.allow_urgent !== false)
  const unlimited = own(g.urgent_unlimited, !!def?.urgent_unlimited)
  const weight = own(g.weight, def?.weight ?? 0)

  // One answer, in precedence order. "Disabled" wins over everything — a ticket count on a lane
  // nobody may use is noise — and "unlimited" makes the count meaningless, so it wins over it too.
  let policy: UrgentPolicy = 'ticket'
  let policyInherited = allow.inherited
  if (!allow.value) {
    policy = 'off'
  } else if (unlimited.value) {
    policy = 'unlimited'
    policyInherited = unlimited.inherited
  } else {
    policyInherited = allow.inherited && unlimited.inherited && weight.inherited
  }

  return {
    urgent: { value: policy, inherited: policyInherited },
    weight,
    maxQueued: own(g.max_queued, def?.max_queued ?? 0),
    runWindow: own(g.run_window, def?.run_window ?? ''),
    // priority is a string on the wire ('' = inherit the SYSTEM default, not the Default group).
    priority: g.priority ? { value: Number(g.priority), inherited: false } : { value: null, inherited: true },
    ...quotaOf(g, groups),
  }
}

/**
 * quotaOf walks from this OU up to the root and takes the nearest ancestor that sets a cap — the
 * same deepest-wins rule EffectiveGroupSettings applies server-side. The number and its period come
 * from ONE OU: inheriting "20" from a parent and "month" from a grandparent would show a limit
 * neither of them configured.
 */
function quotaOf(g: UserGroupRow, groups?: UserGroupRow[]): Pick<OUSettings, 'dailyQuota' | 'quotaPeriod'> {
  const period = (v?: string) => v || 'day' // a row written before the column existed meant per-day
  if (g.daily_run_quota != null) {
    return {
      dailyQuota: { value: g.daily_run_quota, inherited: false },
      quotaPeriod: { value: period(g.run_quota_period), inherited: false },
    }
  }
  const byId = new Map((groups ?? []).map((x) => [x.id, x]))
  const seen = new Set<number>([g.id]) // the server refuses cycles; this runs on whatever it sent
  let cur = g.parent_id ? byId.get(g.parent_id) : undefined
  while (cur && !seen.has(cur.id)) {
    seen.add(cur.id)
    if (cur.daily_run_quota != null) {
      return {
        dailyQuota: { value: cur.daily_run_quota, inherited: true },
        quotaPeriod: { value: period(cur.run_quota_period), inherited: true },
      }
    }
    cur = cur.parent_id ? byId.get(cur.parent_id) : undefined
  }
  // Nobody up the chain caps anything: unlimited. The Default group is the root of that chain, so
  // it needs no special case here — an unset cap on it means the same thing.
  return { dailyQuota: { value: 0, inherited: !g.is_default }, quotaPeriod: { value: 'day', inherited: !g.is_default } }
}

/** ouPath is the chain of names from the root down to this OU, for showing where it sits. */
export function ouPath(groups: UserGroupRow[], id: number): string[] {
  const byId = new Map(groups.map((g) => [g.id, g]))
  const out: string[] = []
  const seen = new Set<number>()
  let cur = byId.get(id)
  while (cur && !seen.has(cur.id)) {
    seen.add(cur.id) // the server refuses cycles; this runs on whatever it sent
    out.unshift(cur.name)
    cur = cur.parent_id ? byId.get(cur.parent_id) : undefined
  }
  return out
}
