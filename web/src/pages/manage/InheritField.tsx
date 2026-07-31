import type { ReactNode } from 'react'
import { Radio, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

// One setting that is either inherited or set here.
//
// This replaces a pattern that went wrong in four places at once: a switch labelled "inherit from
// the default group", sitting ABOVE the field it gated, with the field greyed out when it was on.
// All four switches carried the identical label, so none of them said what they inherited or which
// field they belonged to; and a greyed-out field showed no value, so an admin could not see what
// they were inheriting. In one case it was worse than blank — the run-window input kept its
// placeholder "9-18", which is the EXAMPLE from the hint text, so an inheriting field displayed
// something that reads exactly like a value.
//
// Here it is one line: the inherit option states the resolved value and where it came from, and the
// override input exists only while it is the thing in effect.
export default function InheritField({
  label,
  hint,
  inherited,
  from,
  inheriting,
  onInheritingChange,
  children,
}: {
  label: string
  hint?: string
  /** The resolved value if inherited, formatted for reading ("unlimited", "9-18", "50"). */
  inherited: string
  /** Where that value comes from — an OU name, or the system. */
  from: string
  inheriting: boolean
  onInheritingChange: (v: boolean) => void
  /** The override control, rendered only while the override is in effect. */
  children: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <div style={{ marginBottom: 16 }}>
      <Typography.Text strong style={{ display: 'block', marginBottom: 6 }}>
        {label}
      </Typography.Text>
      <Radio.Group
        value={inheriting ? 'inherit' : 'own'}
        onChange={(e) => onInheritingChange(e.target.value === 'inherit')}
      >
        <Space direction="vertical" size={6}>
          <Radio value="inherit">
            {/* Some sources have no value worth naming — the system default is just "the system
                default" — and "inherit the system default — the system default" says it twice. */}
            {inherited ? t('ou.inheritedAs', { from, value: inherited }) : t('ou.inheritedFrom', { from })}
          </Radio>
          <Radio value="own">
            <Space size={8} align="center">
              {t('ou.override')}
              {/* Only when chosen: a disabled input beside a selected "inherit" is exactly the
                  ambiguity this control exists to remove. */}
              {!inheriting && children}
            </Space>
          </Radio>
        </Space>
      </Radio.Group>
      {hint && (
        <Typography.Text type="secondary" style={{ display: 'block', fontSize: 12, marginTop: 6 }}>
          {hint}
        </Typography.Text>
      )}
    </div>
  )
}
