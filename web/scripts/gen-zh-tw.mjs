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

// The interpolation placeholders a string uses, as a stable set. Traditional conversion never
// touches them — they are ASCII — so zh-TW must always carry exactly zh-CN's.
const slots = (s) => [...String(s).matchAll(/\{\{\s*([\w.]+)/g)].map((m) => m[1]).sort().join(',')

const out = {}
let changed = 0
let restated = 0
for (const [k, v] of Object.entries(cn)) {
  // A key that EXISTS in zh-TW is normally left alone: it may have been hand-corrected, and this
  // script's default is additive on purpose. But an existing key whose zh-CN source has since grown
  // or lost a placeholder is not a translation any more — i18next renders the old string and simply
  // drops the value nobody interpolated, so a count silently disappears from the Traditional UI with
  // every test still green. That happened: storage.cleaned and storage.resultLine gained a
  // {{revisions}} slot and zh-TW kept reporting four categories out of five.
  //
  // Placeholder drift is therefore reconverted even without --all. Wording drift is not: only the
  // machine-checkable half is safe to overwrite behind the operator's back.
  if (!all && k in tw && slots(tw[k]) === slots(v)) {
    out[k] = tw[k]
    continue
  }
  const drift = k in tw
  const conv = convert(v)
  if (conv !== tw[k]) changed += 1
  if (drift) restated += 1
  out[k] = conv
}
// zh-CN is the source of truth in BOTH directions: a key deleted there is deleted here. This file
// is generated in full and holds no hand-written Traditional text, so there is nothing to lose by
// dropping a stale key — whereas keeping one leaves the locale-parity test failing with a message
// that points at zh-TW instead of at the removal that caused it. Not silent, though: they are
// listed, which is what the "never silently drop" rule was actually protecting.
const dropped = Object.keys(tw).filter((k) => !(k in out))

writeFileSync(twPath, JSON.stringify(out, null, 2) + '\n')
console.log(`zh-TW: ${changed} key(s) ${all ? 'reconverted' : 'filled'} from zh-CN via OpenCC s2twp`)
if (!all && restated) console.log(`zh-TW: ${restated} existing key(s) reconverted — their zh-CN placeholders had changed`)
if (dropped.length) console.log(`zh-TW: dropped ${dropped.length} key(s) no longer in zh-CN: ${dropped.join(', ')}`)
