// Generate Traditional-Chinese (zh-TW) locale keys from Simplified (zh-CN) via OpenCC
// s2twp (Taiwan idioms — 軟體/設定/使用者, not just char mapping). zh-CN is the source
// of truth; interpolation like {{name}} is ASCII and passes through untouched.
//
//   node scripts/gen-zh-tw.mjs         fill only keys missing from zh-TW (safe default)
//   node scripts/gen-zh-tw.mjs --all   reconvert every key from zh-CN (overwrites)
//
// Run after adding a zh-CN key so you never hand-translate Traditional Chinese.
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const OpenCC = require('opencc-js')
const convert = OpenCC.Converter({ from: 'cn', to: 'twp' })

const dir = join(dirname(fileURLToPath(import.meta.url)), '..', 'src', 'locales')
const cn = JSON.parse(readFileSync(join(dir, 'zh-CN.json'), 'utf8'))
const twPath = join(dir, 'zh-TW.json')
const tw = JSON.parse(readFileSync(twPath, 'utf8'))
const all = process.argv.includes('--all')

const out = {}
let changed = 0
for (const [k, v] of Object.entries(cn)) {
  if (!all && k in tw) {
    out[k] = tw[k]
    continue
  }
  const conv = convert(v)
  if (conv !== tw[k]) changed += 1
  out[k] = conv
}
// Never silently drop zh-TW-only keys (parity should prevent these, but be safe).
for (const k of Object.keys(tw)) if (!(k in out)) out[k] = tw[k]

writeFileSync(twPath, JSON.stringify(out, null, 2) + '\n')
console.log(`zh-TW: ${changed} key(s) ${all ? 'reconverted' : 'filled'} from zh-CN via OpenCC s2twp`)
