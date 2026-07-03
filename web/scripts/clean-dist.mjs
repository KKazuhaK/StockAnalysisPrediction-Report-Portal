// Remove stale build outputs from the go:embed dist dir before a fresh vite build,
// preserving .gitkeep. vite's emptyOutDir cannot do this because outDir lives
// outside the project root (../internal/web/dist), so builds would otherwise pile
// up hashed bundles and bloat the embedded binary.
import { existsSync, readdirSync, rmSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'

const dir = fileURLToPath(new URL('../../internal/web/dist', import.meta.url))
if (existsSync(dir)) {
  for (const entry of readdirSync(dir)) {
    if (entry !== '.gitkeep') rmSync(join(dir, entry), { recursive: true, force: true })
  }
}
