import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { useState } from 'react'
import DifyFileInput from './DifyFileInput'
import { MAX_FILES, MAX_FILE_BYTES, type UploadedFile } from '../lib/difyInputs'

const uploads = vi.hoisted(() => ({ calls: [] as Array<[string, FormData]>, fail: false, seq: 0 }))
vi.mock('../api/client', () => ({
  api: {
    upload: (url: string, body: FormData) => {
      uploads.calls.push([url, body])
      if (uploads.fail) return Promise.reject(new Error('boom'))
      uploads.seq += 1
      const f = body.get('file') as File
      return Promise.resolve({ ok: true, file_id: `id${uploads.seq}`, name: f.name, size: f.size })
    },
  },
  errText: (_e: unknown, _t: unknown, fallback: string) => fallback,
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

// A file of a given size without allocating it: jsdom keeps the parts, and only `size` is read.
function bigFile(name: string, size: number) {
  const f = new File(['x'], name, { type: 'text/plain' })
  Object.defineProperty(f, 'size', { value: size })
  return f
}

// The control is a Form.Item child in the ordinary value/onChange sense, so a bit of state stands
// in for the form here — that is exactly what the form does with it.
function Harness({ type }: { type: string }) {
  const [value, setValue] = useState<UploadedFile[]>([])
  return (
    <App>
      <DifyFileInput targetId={7} type={type} value={value} onChange={setValue} />
      <div data-testid="ids">{value.map((f) => f.id).join(',')}</div>
    </App>
  )
}

const picker = (c: HTMLElement) => c.querySelector('input[type="file"]') as HTMLInputElement

beforeEach(() => {
  uploads.calls = []
  uploads.fail = false
  uploads.seq = 0
})

describe('DifyFileInput', () => {
  it('uploads to the target it was given and keeps the id the server returned', async () => {
    const user = userEvent.setup()
    const { container } = render(<Harness type="file-list" />)
    await user.upload(picker(container), new File(['hello'], 'a.pdf', { type: 'application/pdf' }))
    await waitFor(() => expect(screen.getByTestId('ids').textContent).toBe('id1'))
    expect(uploads.calls[0][0]).toBe('/api/admin/batch/targets/7/upload')
    // The field name is the contract with Dify's own /files/upload, which the server forwards to.
    expect((uploads.calls[0][1].get('file') as File).name).toBe('a.pdf')
    expect(screen.getByText('a.pdf')).toBeTruthy()
  })

  it('refuses a file over the size ceiling without sending it', async () => {
    const user = userEvent.setup()
    const { container } = render(<Harness type="file-list" />)
    await user.upload(picker(container), bigFile('huge.pdf', MAX_FILE_BYTES + 1))
    expect(await screen.findByText(/run\.fileTooLarge/)).toBeTruthy()
    expect(uploads.calls).toHaveLength(0)
    expect(screen.getByTestId('ids').textContent).toBe('')
  })

  it('refuses the files past the per-run ceiling, having accepted the ones that fit', async () => {
    const user = userEvent.setup()
    const { container } = render(<Harness type="file-list" />)
    // One pick, one over the cap: the later files must not all read the same "nothing uploaded
    // yet" as the first and sail past the ceiling together.
    const files = Array.from({ length: MAX_FILES + 1 }, (_, i) => new File(['x'], `f${i}.pdf`, { type: 'text/plain' }))
    await user.upload(picker(container), files)
    expect(await screen.findByText(/run\.fileTooMany/)).toBeTruthy()
    await waitFor(() => expect(screen.getByTestId('ids').textContent.split(',')).toHaveLength(MAX_FILES))
    expect(uploads.calls).toHaveLength(MAX_FILES)
  })

  it('stops offering the picker once a single-file input is filled', async () => {
    const user = userEvent.setup()
    const { container } = render(<Harness type="file" />)
    await user.upload(picker(container), new File(['a'], 'a.pdf', { type: 'application/pdf' }))
    await waitFor(() => expect(screen.getByTestId('ids').textContent).toBe('id1'))
    expect(picker(container).disabled).toBe(true)
  })

  it('drops a removed file from the value', async () => {
    const user = userEvent.setup()
    const { container } = render(<Harness type="file-list" />)
    await user.upload(picker(container), new File(['hello'], 'a.pdf', { type: 'application/pdf' }))
    await waitFor(() => expect(screen.getByTestId('ids').textContent).toBe('id1'))
    await user.click(container.querySelector('.anticon-delete, .ant-upload-list-item-action') as HTMLElement)
    await waitFor(() => expect(screen.getByTestId('ids').textContent).toBe(''))
  })

  it('reports a failed upload and adds nothing', async () => {
    uploads.fail = true
    const user = userEvent.setup()
    const { container } = render(<Harness type="file-list" />)
    await user.upload(picker(container), new File(['hello'], 'a.pdf', { type: 'application/pdf' }))
    expect(await screen.findByText('run.uploadFailed')).toBeTruthy()
    expect(screen.getByTestId('ids').textContent).toBe('')
  })
})
