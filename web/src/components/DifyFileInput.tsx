import { useRef, useState } from 'react'
import { App, Button, Upload } from 'antd'
import type { UploadFile } from 'antd'
import { UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import { MAX_FILE_BYTES, MAX_FILE_MB, fileLimit, type UploadedFile } from '../lib/difyInputs'

// The run form's control for a Dify `file` / `file-list` input. A file cannot ride in the row the
// way text does: it is uploaded the moment it is picked, and what the form then holds is the id
// Dify handed back. So this is a Form.Item child in the ordinary value/onChange sense — its value
// is the list of uploaded files — and the picking, the size/count limits and the upload itself are
// its own business.
export default function DifyFileInput({
  targetId,
  type,
  value = [],
  onChange,
}: {
  targetId: number
  type?: string // the declared input kind: "file" (one) or "file-list" (up to Dify's per-run cap)
  value?: UploadedFile[]
  onChange?: (v: UploadedFile[]) => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [uploading, setUploading] = useState(0)
  const limit = fileLimit(type)

  // The form owns the value, so mirror it on every render: picking a different workflow resets the
  // fields, and a stale ref would then hand the next run files the form no longer has.
  const listRef = useRef<UploadedFile[]>(value)
  listRef.current = value
  // Slots held by uploads still in flight. Selecting five files at once calls beforeUpload five
  // times in one tick, before any of them has answered — without this they would all read the same
  // "0 uploaded" and sail past the cap together.
  const heldRef = useRef(0)
  const seqRef = useRef(0)

  const commit = (next: UploadedFile[]) => {
    listRef.current = next
    onChange?.(next)
  }

  const send = async (file: File) => {
    try {
      const body = new FormData()
      body.append('file', file)
      const r = await api.upload<{ file_id: string; name?: string }>(
        `/api/admin/batch/targets/${targetId}/upload`,
        body,
      )
      seqRef.current += 1
      commit([...listRef.current, { uid: `${r.file_id}-${seqRef.current}`, name: r.name || file.name, id: r.file_id }])
    } catch (e) {
      message.error(errText(e, t, 'run.uploadFailed'))
    } finally {
      heldRef.current -= 1
      setUploading((n) => n - 1)
    }
  }

  const beforeUpload = (file: File) => {
    if (file.size > MAX_FILE_BYTES) {
      message.error(t('run.fileTooLarge', { name: file.name, mb: MAX_FILE_MB }))
      return Upload.LIST_IGNORE
    }
    if (listRef.current.length + heldRef.current >= limit) {
      message.error(t('run.fileTooMany', { n: limit }))
      return Upload.LIST_IGNORE
    }
    heldRef.current += 1
    setUploading((n) => n + 1)
    void send(file)
    // The upload is ours, and so is the list: antd never gets to run its own request or track a
    // file we may still reject.
    return Upload.LIST_IGNORE
  }

  const fileList: UploadFile[] = value.map((f) => ({ uid: f.uid, name: f.name, status: 'done' }))
  const full = value.length + uploading >= limit

  return (
    <Upload
      multiple={limit > 1}
      disabled={full}
      fileList={fileList}
      beforeUpload={beforeUpload}
      onRemove={(f) => {
        commit(listRef.current.filter((u) => u.uid !== f.uid))
        return false
      }}
    >
      <Button icon={<UploadOutlined />} loading={uploading > 0} disabled={full}>
        {t('run.fileUpload')}
      </Button>
    </Upload>
  )
}
