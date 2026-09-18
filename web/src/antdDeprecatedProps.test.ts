import { describe, expect, it } from 'vitest'

const sources = import.meta.glob('./**/*.tsx', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

const production = Object.entries(sources).filter(([path]) => !/\.test\.tsx$/.test(path))

const deprecated: Array<[string, RegExp]> = [
  ['Space/Steps direction', /<(?:Space|Steps)\b[^>]*\bdirection=/s],
  ['Alert message', /<Alert\b[^>]*\bmessage=/s],
  ['Drawer width', /<Drawer\b[^>]*\bwidth=/s],
  ['InputNumber addon', /<InputNumber\b[^>]*\baddon(?:Before|After)=/s],
  ['DatePicker popupClassName', /<DatePicker\b[^>]*\bpopupClassName=/s],
  ['List component', /<List(?:\.|\s|>)/],
  ['Table rowKey index callback', /\browKey=\{\s*\([^)]*,/],
]

describe('Ant Design 6 deprecated component APIs', () => {
  it('keeps production components on their current replacements', () => {
    const offenders: string[] = []
    for (const [path, source] of production) {
      for (const [name, pattern] of deprecated) {
        if (pattern.test(source)) offenders.push(`${path}: ${name}`)
      }
    }
    expect(offenders).toEqual([])
  })
})
