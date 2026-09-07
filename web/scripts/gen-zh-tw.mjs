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
const force = process.argv.includes('--force')

// The interpolation placeholders a string uses, as a stable set. Traditional conversion never
// touches them — they are ASCII — so zh-TW must always carry exactly zh-CN's.
const slots = (s) => [...String(s).matchAll(/\{\{\s*([\w.]+)/g)].map((m) => m[1]).sort().join(',')

// Substitutions applied AFTER conversion, for the handful of words where s2twp picks the Taiwanese
// term for the wrong sense. These are corrections to the converter, not translations, so they belong
// here rather than as hand-edits in the bundle: a hand-edit survives the default run but is silently
// undone by --all, which is how a wrong word comes back months later with nobody watching.
//
// 代码 is the standing case. In Taiwanese computing 代码 is usually 程式碼 -- source code -- and s2twp
// converts it that way, which is right for an API and wrong for a stock code. A 股票代码 is 股票代碼.
const AFTER_CONVERSION = [[/程式碼/g, '代碼']]
const fix = (s) => AFTER_CONVERSION.reduce((acc, [re, to]) => acc.replace(re, to), s)

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
  const conv = fix(convert(v))
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

// --all reconverts every key, which means it DISCARDS every hand correction in the bundle. That is
// not hypothetical: this file has ~180 keys where somebody replaced s2twp's output with the term
// Taiwanese usage actually wants -- 權限 not 許可權, 優先級 not 優先順序, 存取 not 訪問, 唯讀 not
// 只讀 -- and a bare --all silently puts every one of them back. The flag stays, because
// reconverting is occasionally the right thing after a converter upgrade; it now has to be asked
// for twice, and it says what it is about to throw away first.
const reworded = Object.keys(out).filter((k) => k in tw && out[k] !== tw[k])
if (all && !force && reworded.length) {
  console.error(`--all would reword ${reworded.length} existing key(s), discarding any hand correction in them. For example:`)
  for (const k of reworded.slice(0, 5)) console.error(`  ${k}\n    have: ${tw[k]}\n    want: ${out[k]}`)
  console.error('Nothing written. Re-run with --all --force if that is genuinely what you want.')
  process.exit(1)
}

writeFileSync(twPath, JSON.stringify(out, null, 2) + '\n')
console.log(`zh-TW: ${changed} key(s) ${all ? 'reconverted' : 'filled'} from zh-CN via OpenCC s2twp`)
if (!all && restated) console.log(`zh-TW: ${restated} existing key(s) reconverted — their zh-CN placeholders had changed`)
if (dropped.length) console.log(`zh-TW: dropped ${dropped.length} key(s) no longer in zh-CN: ${dropped.join(', ')}`)
