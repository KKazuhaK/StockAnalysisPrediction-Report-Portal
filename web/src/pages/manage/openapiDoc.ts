// Flattens an OpenAPI 3.1 spec (served at /api/openapi.json) into the flat endpoint
// shape the 接口说明 tab renders — so the docs are driven by the spec, not a copy.

export interface ApiParam {
  name: string
  in: 'query' | 'body' | 'path' | 'header'
  type: string
  required: boolean
  desc: string
}
export interface ApiError {
  code: number
  when: string
}
export interface ApiEndpoint {
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  path: string
  scope: string
  summary: string
  params: ApiParam[]
  requestExample: string
  responseExample: string
  errors: ApiError[]
  notes: string
}

/* eslint-disable @typescript-eslint/no-explicit-any */
type AnySpec = Record<string, any>

const refName = (ref: string) => ref.split('/').pop() || ''

function resolveSchema(spec: AnySpec, schema: AnySpec | undefined): AnySpec {
  if (schema && schema.$ref) return spec?.components?.schemas?.[refName(schema.$ref)] || {}
  return schema || {}
}

function typeOf(prop: AnySpec): string {
  if (!prop) return ''
  if (prop.type === 'array') {
    const it = prop.items || {}
    return `array<${it.$ref ? refName(it.$ref) : it.type || 'object'}>`
  }
  if (prop.$ref) return refName(prop.$ref)
  return prop.type || 'object'
}

// POST first so /reports shows ingest before query; then the rest.
const METHODS = ['post', 'get', 'put', 'patch', 'delete'] as const

export function specToEndpoints(spec: AnySpec, base: string): { conventions: string; endpoints: ApiEndpoint[] } {
  const conventions: string = spec?.info?.description || ''
  const endpoints: ApiEndpoint[] = []
  const paths: AnySpec = spec?.paths || {}
  for (const path of Object.keys(paths)) {
    const item = paths[path] || {}
    for (const m of METHODS) {
      const op = item[m]
      if (!op) continue
      const params: ApiParam[] = []
      for (const p of op.parameters || []) {
        params.push({
          name: p.name,
          in: p.in,
          type: p.schema?.type || 'string',
          required: !!p.required,
          desc: p.description || '',
        })
      }
      const bodySchema = resolveSchema(spec, op.requestBody?.content?.['application/json']?.schema)
      if (bodySchema.properties) {
        const req: string[] = bodySchema.required || []
        for (const name of Object.keys(bodySchema.properties)) {
          const prop = bodySchema.properties[name]
          params.push({ name, in: 'body', type: typeOf(prop), required: req.includes(name), desc: prop.description || '' })
        }
      }
      const responses: AnySpec = op.responses || {}
      const errors: ApiError[] = []
      for (const code of Object.keys(responses)) {
        const n = Number(code)
        if (n >= 400) errors.push({ code: n, when: responses[code]?.description || '' })
      }
      const okResp = responses['200'] || responses['201'] || {}
      const example = okResp.content?.['application/json']?.example
      const responseExample = example !== undefined ? JSON.stringify(example, null, 2) : ''
      const sample: string = (op['x-codeSamples'] || [])[0]?.source || ''
      endpoints.push({
        method: m.toUpperCase() as ApiEndpoint['method'],
        path,
        scope: op['x-scope'] || 'query',
        summary: op.summary || '',
        params,
        requestExample: sample.split('$BASE').join(base),
        responseExample,
        errors,
        notes: op.description || '',
      })
    }
  }
  return { conventions, endpoints }
}
