import type { ReactNode } from 'react'
import { Button, InputNumber, Space } from 'antd'
import type { InputNumberProps } from 'antd'

type Props = Omit<InputNumberProps<number>, 'addonBefore' | 'addonAfter'> & {
  before?: ReactNode
  after?: ReactNode
}

// Ant Design 6 removed InputNumber's built-in addons. Keep the familiar joined control without
// relying on deprecated props; the affixes are descriptive rather than separate form controls.
export default function CompactNumberInput({ before, after, ...props }: Props) {
  return (
    <Space.Compact>
      {before != null && <Button disabled aria-hidden className="rp-number-input-addon">{before}</Button>}
      <InputNumber {...props} />
      {after != null && <Button disabled aria-hidden className="rp-number-input-addon">{after}</Button>}
    </Space.Compact>
  )
}
