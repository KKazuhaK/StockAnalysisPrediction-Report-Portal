import { withChunkReload } from './lazyRetry'

// Keep Mermaid behind the report-body boundary. Most portal routes never render a
// chart, so loading the renderer eagerly would add a large dependency to first paint.
export const loadMermaid = withChunkReload(() => import('mermaid'))
