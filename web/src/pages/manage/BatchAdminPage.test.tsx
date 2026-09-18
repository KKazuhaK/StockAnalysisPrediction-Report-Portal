import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { App } from 'antd'
import BatchAdminPage, { TargetInputsPreview } from './BatchAdminPage'

const putSpy = vi.fn((_url: string, _body: unknown) => Promise.resolve({ ok: true }))

// Targets and advanced plugins used to stack as two cards; they are now sub-tabs.
// get() is routed by URL so the edit flow can load a target's config.
vi.mock('../../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url === '/api/admin/batch/targets')
        return Promise.resolve({
          targets: [{
            id: 7,
            plugin_slug: 'dify',
            name: 'My workflow',
            created_at: '2026-07-06 09:00:00',
            inputs: [
              { key: 'symbol' },
              { key: 'report_date' },
              { key: 'information_cutoff' },
              { key: 'policy_config_json' },
              { key: 'portfolio_context_json' },
            ],
          }],
        })
      if (url === '/api/admin/batch/dify/targets/7')
        return Promise.resolve({
          id: 7,
          name: 'My workflow',
          base_url: 'https://dify.example/v1',
          inputs: [{ variable: 'symbol', label: 'Stock code (exchange format)', display_label: 'Stock code', description: 'Current help', required: true }],
          has_key: true,
          collapse_optional_inputs: true,
        })
      return Promise.resolve({ plugins: [] })
    },
    post: () => Promise.resolve({}),
    put: (url: string, body: unknown) => putSpy(url, body),
    del: () => Promise.resolve({}),
  },
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

describe('BatchAdminPage — targets / plugins sub-tabs', () => {
  beforeEach(() => putSpy.mockClear())

  it('renders both sub-tabs', async () => {
    const { container } = render(
      <App>
        <BatchAdminPage />
      </App>,
    )
    expect(await screen.findByText('batch.admin.targets')).toBeTruthy()
    expect(await screen.findByText('batch.admin.advancedPlugins')).toBeTruthy()

    // Input chips must share the available inputs column instead of contributing their
    // combined max-content width to the table. A bounded fixed-layout table lets the compact
    // preview stay inside that column while retaining table-local scrolling on narrow screens.
    const table = container.querySelector<HTMLTableElement>('.rp-batch-targets-table table')
    expect(table?.style.tableLayout).toBe('fixed')
    expect(table?.style.width).not.toBe('max-content')
  })

  it('opens the complete input list from the final overflow chip', async () => {
    render(
      <App>
        <TargetInputsPreview
          inputs={[
            { key: 'symbol' },
            { key: 'report_date' },
            { key: 'information_cutoff' },
            { key: 'policy_config_json' },
            { key: 'portfolio_context_json' },
          ]}
        />
      </App>,
    )

    // The row stays compact: only the preview is mounted until the explicit last chip opens it.
    expect(screen.queryByText('policy_config_json')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'batch.admin.inputsMore:{"n":2}' }))
    expect(await screen.findByText('policy_config_json')).toBeTruthy()
    expect(screen.getByText('portfolio_context_json')).toBeTruthy()
  })

  it('edits a Dify target through the modal and saves via PUT', async () => {
    render(
      <App>
        <BatchAdminPage />
      </App>,
    )
    // The target row renders with its Edit action.
    await screen.findByText('My workflow')
    fireEvent.click(screen.getByTitle('common.edit'))

    // The edit modal opens prefilled with the target's name + base URL (never the key).
    await screen.findByText('batch.dify.editTarget')
    expect(await screen.findByDisplayValue('My workflow')).toBeTruthy()
    expect(screen.getByDisplayValue('https://dify.example/v1')).toBeTruthy()
    const displayLabel = screen.getByLabelText(/batch\.dify\.inputDisplayLabelFor/) as HTMLInputElement
    expect(displayLabel.value).toBe('Stock code')
    fireEvent.change(displayLabel, { target: { value: 'Ticker' } })
    const description = screen.getByLabelText(/batch\.dify\.inputDescriptionFor/) as HTMLInputElement
    expect(description.value).toBe('Current help')
    fireEvent.change(description, { target: { value: 'Six-digit exchange code' } })

    // Typing a new key rotates it: the PUT body must carry the freshly-entered key,
    // not the blank "keep existing" sentinel.
    fireEvent.change(screen.getByPlaceholderText('batch.dify.apiKeyKeepPlaceholder'), { target: { value: 'app-newkey' } })

    // Saving issues a PUT to the target's Dify endpoint with the edited config.
    fireEvent.click(screen.getByText('common.save'))
    await vi.waitFor(() => expect(putSpy).toHaveBeenCalled())
    const [url, body] = putSpy.mock.calls[0] as unknown as [string, Record<string, unknown>]
    expect(url).toBe('/api/admin/batch/dify/targets/7')
    expect(body.name).toBe('My workflow')
    expect(body.base_url).toBe('https://dify.example/v1')
    expect(body.api_key).toBe('app-newkey')
    expect(Array.isArray(body.inputs)).toBe(true)
    expect((body.inputs as Array<Record<string, unknown>>)[0].display_label).toBe('Ticker')
    expect((body.inputs as Array<Record<string, unknown>>)[0].description).toBe('Six-digit exchange code')
    expect(body.collapse_optional_inputs).toBe(true)
  })
})
