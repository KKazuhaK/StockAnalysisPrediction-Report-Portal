import { useMemo, useState } from 'react'
import { Button, Input, Radio, Segmented, Space, Tag, Tooltip, Tree, Typography, theme } from 'antd'
import { LeftOutlined, RightOutlined, SearchOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { UserGroupRow } from '../../api/types'

// The organizational-unit picker: a tree on the left that scopes the account list.
//
// OUs have been a tree since ADR 0022 — parent_id, restriction sticky down a subtree, quota and
// weight inherited root-to-leaf — but every screen rendered them as a flat list, so the one thing
// that explains the inheritance was the one thing an admin could not see. Parenthood existed only
// as a dropdown inside an edit dialog.
//
// Selecting an OU includes its DESCENDANTS. That is not a convenience: in this schema a setting on
// a parent already governs the whole subtree, so "the people this OU's settings apply to" is the
// question an admin is actually asking, and it is the only reading that matches what the OU does.

/** subtreeOf expands a selection to every OU beneath it, so a parent means "and everything under". */
export function subtreeOf(groups: UserGroupRow[], selected: number[]): Set<number> {
  const children = new Map<number, number[]>()
  for (const g of groups) {
    const p = g.parent_id || 0
    children.set(p, [...(children.get(p) ?? []), g.id])
  }
  const out = new Set<number>()
  const walk = (id: number) => {
    if (out.has(id)) return // a cycle would otherwise hang the page; the server refuses them, but
    out.add(id) // this runs on data the server sent and must not depend on that holding
    for (const c of children.get(id) ?? []) walk(c)
  }
  selected.forEach(walk)
  return out
}

interface TreeNode {
  key: number
  title: React.ReactNode
  children?: TreeNode[]
}

export default function OrgUnitPicker({
  groups,
  unassigned,
  scoped,
  onScopedChange,
  selected,
  onSelect,
  onManage,
  mode = 'filter',
  onAdd,
}: {
  groups: UserGroupRow[]
  /** Accounts with no primary group; they belong to the Default OU by inheritance. */
  unassigned: number
  scoped: boolean
  onScopedChange: (v: boolean) => void
  selected: number[]
  onSelect: (ids: number[]) => void
  onManage: () => void
  /**
   * 'filter' scopes the account list — all-vs-selected, single or multi. 'manage' picks the one OU
   * being edited beside it, so the scope radio and multi-select have nothing to mean there.
   */
  mode?: 'filter' | 'manage'
  onAdd?: () => void
}) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const [collapsed, setCollapsed] = useState(false)
  const [multi, setMulti] = useState(false)
  const [q, setQ] = useState('')
  // Controlled, so a search does not destroy the admin's own expand/collapse work. Keying the Tree
  // on the query instead remounted it on every keystroke, re-ran defaultExpandAll, and left the
  // scroll container parked on an unrelated node — with no way back to the view you had.
  //
  // null means "untouched": the tree starts fully expanded, which is what an OU hierarchy is for.
  // The moment the admin expands or collapses anything, their choice is the state and it survives
  // searching, clearing the search, and selecting.
  const [expanded, setExpanded] = useState<React.Key[] | null>(null)

  const query = q.trim().toLowerCase()

  // A search match keeps its ancestors, or a matching leaf would be unreachable in a tree view.
  const visible = useMemo(() => {
    if (!query) return null
    const byId = new Map(groups.map((g) => [g.id, g]))
    const keep = new Set<number>()
    for (const g of groups) {
      if (!g.name.toLowerCase().includes(query)) continue
      let cur: UserGroupRow | undefined = g
      while (cur && !keep.has(cur.id)) {
        keep.add(cur.id)
        cur = cur.parent_id ? byId.get(cur.parent_id) : undefined
      }
    }
    return keep
  }, [groups, query])

  // The number on a node is the number of accounts selecting it produces — its own members plus
  // every descendant's, and for the Default OU the accounts that inherit it by having no primary
  // group. Showing only the direct count contradicted the click: a parent with three child OUs of
  // ten read "0" and then listed thirty. In a console whose job is to make inheritance visible,
  // that is the one number that must not understate the population an OU governs.
  const reach = useMemo(() => {
    const direct = new Map(groups.map((g) => [g.id, g.members || 0]))
    const out = new Map<number, number>()
    for (const g of groups) {
      let n = 0
      for (const id of subtreeOf(groups, [g.id])) n += direct.get(id) ?? 0
      if (g.is_default) n += unassigned
      out.set(g.id, n)
    }
    return out
  }, [groups, unassigned])

  const nodes = useMemo(() => {
    const build = (parent: number): TreeNode[] =>
      groups
        .filter((g) => (g.parent_id || 0) === parent && (!visible || visible.has(g.id)))
        .map((g) => {
          const kids = build(g.id)
          return {
            key: g.id,
            title: (
              <Space size={4}>
                <span>{g.name}</span>
                {g.is_default && <Tag color="green">{t('users.defaultGroupTag')}</Tag>}
                {g.restricted_effective && <Tag color="volcano">{t('users.restrictedTag')}</Tag>}
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {reach.get(g.id) ?? g.members}
                </Typography.Text>
              </Space>
            ),
            ...(kids.length ? { children: kids } : {}),
          }
        })
    return build(0)
  }, [groups, visible, reach, t])

  if (collapsed) {
    return (
      <Tooltip title={t('ou.expand')}>
        <Button
          icon={<RightOutlined />}
          aria-label={t('ou.expand')}
          onClick={() => setCollapsed(false)}
          style={{ marginRight: 8 }}
        />
      </Tooltip>
    )
  }

  return (
    <div
      style={{
        width: 268,
        flex: '0 0 268px',
        border: `1px solid ${token.colorBorderSecondary}`,
        borderRadius: token.borderRadiusLG,
        display: 'flex',
        flexDirection: 'column',
        alignSelf: 'flex-start',
      }}
    >
      <Space
        style={{
          width: '100%',
          justifyContent: 'space-between',
          padding: '10px 12px',
          borderBottom: `1px solid ${token.colorBorderSecondary}`,
        }}
      >
        <Typography.Text strong>{t('ou.title')}</Typography.Text>
        <Tooltip title={t('ou.collapse')}>
          <Button
            type="text"
            size="small"
            icon={<LeftOutlined />}
            aria-label={t('ou.collapse')}
            onClick={() => setCollapsed(true)}
          />
        </Tooltip>
      </Space>

      <Space direction="vertical" size={10} style={{ padding: 12, width: '100%' }}>
        {mode === 'filter' && (
        <Radio.Group
          value={scoped ? 'scoped' : 'all'}
          onChange={(e) => onScopedChange(e.target.value === 'scoped')}
        >
          <Space direction="vertical" size={4}>
            <Radio value="all">{t('ou.scopeAll')}</Radio>
            <Radio value="scoped">{t('ou.scopeSelected')}</Radio>
          </Space>
        </Radio.Group>
        )}

        <Input
          allowClear
          size="small"
          prefix={<SearchOutlined />}
          placeholder={t('ou.search')}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />

        {mode === 'filter' && (
        <Segmented
          block
          size="small"
          value={multi ? 'multi' : 'single'}
          onChange={(v) => {
            const isMulti = v === 'multi'
            setMulti(isMulti)
            // Narrowing to single select must not silently keep several OUs in the filter.
            if (!isMulti && selected.length > 1) onSelect(selected.slice(0, 1))
          }}
          options={[
            { value: 'single', label: t('ou.single') },
            { value: 'multi', label: t('ou.multi') },
          ]}
        />
        )}
      </Space>

      <div style={{ padding: '0 8px 8px', maxHeight: 420, overflow: 'auto' }}>
        {nodes.length === 0 ? (
          <Typography.Text type="secondary" style={{ fontSize: 12, padding: 8, display: 'block' }}>
            {query ? t('ou.noMatch') : t('users.noGroups')}
          </Typography.Text>
        ) : (
          <Tree
            blockNode
            multiple={multi}
            // Under a filter every kept ancestor is expanded — that is exactly what `visible`
            // computes — and with no filter the admin's own expansion stands.
            expandedKeys={query && visible ? [...visible] : (expanded ?? groups.map((g) => g.id))}
            onExpand={setExpanded}
            treeData={nodes}
            selectedKeys={selected}
            onSelect={(keys) => {
              const ids = keys.map(Number)
              // In manage mode a click must never clear the selection, or the detail pane would
              // vanish when you click the OU you are already editing.
              if (mode === 'manage' && ids.length === 0) return
              onSelect(ids)
              // Picking an OU is the act of scoping — making the admin also flip the radio would
              // be a second step with no decision in it.
              if (mode === 'filter' && ids.length > 0) onScopedChange(true)
            }}
          />
        )}
      </div>

      <div style={{ borderTop: `1px solid ${token.colorBorderSecondary}`, padding: 8 }}>
        {mode === 'manage' ? (
          <Button type="link" size="small" block onClick={onAdd}>
            {t('ou.add')}
          </Button>
        ) : (
          <Button type="link" size="small" block onClick={onManage}>
            {t('ou.manage')}
          </Button>
        )}
      </div>
    </div>
  )
}
