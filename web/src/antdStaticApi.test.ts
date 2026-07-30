import { describe, expect, it } from 'vitest'

// antd's `message` / `notification` / `Modal.method()` come in two flavours, and only one of them
// is themed.
//
// The static exports (`import { message } from 'antd'`) render into their own detached React root,
// outside the app's ConfigProvider. They never see `darkAlgorithm`, so a toast fired from a
// dark-themed page comes out white — which is what happened to the "saved" toast on the report
// versions page.
//
// The hook forms from `App.useApp()` render inside the provider tree and inherit the theme, the
// locale and the design tokens. This test is the only thing keeping the two apart: the broken
// version looks perfectly correct in review and only shows itself in dark mode at runtime.

// Vite's raw glob rather than node:fs — the web tsconfig carries no node types, and this is how the
// bundler already reads files.
const sources = import.meta.glob('./**/*.{ts,tsx}', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

const productionSources = Object.entries(sources).filter(([p]) => !/\.test\.tsx?$/.test(p))

describe('antd static APIs are not used', () => {
  it('has source files to check', () => {
    // A glob that silently matched nothing would make every assertion below vacuously true.
    expect(productionSources.length).toBeGreaterThan(50)
  })

  it('nothing imports message or notification directly from antd', () => {
    const offenders: string[] = []
    for (const [file, src] of productionSources) {
      // Quotes are matched either way: nothing should depend on this file's own formatting.
      for (const m of src.matchAll(/import\s*\{([^}]*)\}\s*from\s*['"]antd['"]/g)) {
        const named = m[1].split(',').map((s) => s.trim().split(/\s+as\s+/)[0].trim())
        for (const bad of ['message', 'notification']) {
          if (named.includes(bad)) offenders.push(`${file} imports \`${bad}\``)
        }
      }
    }
    expect(
      offenders,
      'use `const { message } = App.useApp()` instead — the static export renders outside ' +
        'ConfigProvider and ignores the dark theme',
    ).toEqual([])
  })

  it('nothing calls the static Modal.confirm/info/error family', () => {
    const offenders = productionSources
      .filter(([, src]) => /\bModal\.(confirm|info|success|error|warning)\s*\(/.test(src))
      .map(([file]) => file)
    expect(offenders, 'use `const { modal } = App.useApp()` — same theming problem').toEqual([])
  })
})
