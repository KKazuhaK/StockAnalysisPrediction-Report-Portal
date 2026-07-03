import { useRef, useState } from 'react'
import { App, Button, Modal, Space, Spin, Typography } from 'antd'
import { FilePdfOutlined, FileZipOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { exportReportPdf, exportDayZip, DayExportEmptyError, isAbortError, type ReportForPrint } from '../lib/exportPdf'

// Server-side PDF rendering (wkhtmltopdf) streams back a single finished blob with no
// real progress %, so exports show an honest indeterminate spinner. It lives in a small
// modal with a Cancel button rather than just a button spinner, so a mis-click costs
// nothing: Cancel aborts the in-flight request (AbortController) and closes the dialog.

// useCancelableExport runs one async task at a time, exposing a busy flag (drives the
// modal + button spinner) and a cancel() that aborts the task's fetch. The task owns its
// own success/error toasts; this hook only manages the busy/abort lifecycle.
function useCancelableExport() {
  const [busy, setBusy] = useState(false)
  const ctrlRef = useRef<AbortController | null>(null)

  const run = async (task: (signal: AbortSignal) => Promise<void>) => {
    if (busy) return // guard against a second launch while one is in flight
    const ctrl = new AbortController()
    ctrlRef.current = ctrl
    setBusy(true)
    try {
      await task(ctrl.signal)
    } finally {
      setBusy(false)
      ctrlRef.current = null
    }
  }

  const cancel = () => ctrlRef.current?.abort()
  return { busy, run, cancel }
}

// ExportProgressModal is the shared cancelable "working…" dialog. Cancel (button or ESC)
// calls onCancel; mask-click is disabled so the dialog isn't dismissed by accident.
function ExportProgressModal({ open, text, onCancel }: { open: boolean; text: string; onCancel: () => void }) {
  const { t } = useTranslation()
  return (
    <Modal
      open={open}
      centered
      width={320}
      closable={false}
      maskClosable={false}
      onCancel={onCancel}
      title={null}
      footer={[
        <Button key="cancel" onClick={onCancel}>
          {t('common.cancel')}
        </Button>,
      ]}
    >
      <Space align="center" size={12} style={{ padding: '8px 0' }}>
        <Spin />
        <Typography.Text>{text}</Typography.Text>
      </Space>
    </Modal>
  )
}

// ExportPdfButton exports one report to PDF with a cancelable progress dialog. Shared by
// the stock and run pages, which previously each inlined an identical fire-and-forget button.
export function ExportPdfButton({ rid, report }: { rid: string; report: ReportForPrint }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { busy, run, cancel } = useCancelableExport()

  const onClick = () =>
    run(async (signal) => {
      try {
        const result = await exportReportPdf(rid, report, signal)
        message[result === 'printed' ? 'info' : 'success'](
          t(result === 'printed' ? 'export.pdfPrinted' : 'export.pdfReady'),
        )
      } catch (e) {
        message[isAbortError(e) ? 'info' : 'error'](t(isAbortError(e) ? 'export.cancelled' : 'export.pdfFailed'))
      }
    })

  return (
    <>
      <Button icon={<FilePdfOutlined />} loading={busy} onClick={onClick}>
        {t('stock.exportPdf')}
      </Button>
      <ExportProgressModal open={busy} text={t('export.pdfGenerating')} onCancel={cancel} />
    </>
  )
}

// ExportDayButton downloads every report a stock has on the selected date as one ZIP
// (each report as .md + .pdf). Same cancelable dialog; the wait can be longer since the
// server renders every report's PDF before responding.
export function ExportDayButton({ symbol, date, name }: { symbol: string; date: string; name?: string }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { busy, run, cancel } = useCancelableExport()

  const onClick = () =>
    run(async (signal) => {
      try {
        await exportDayZip(symbol, date, name, signal)
        message.success(t('export.dayReady'))
      } catch (e) {
        if (isAbortError(e)) message.info(t('export.cancelled'))
        else if (e instanceof DayExportEmptyError) message.error(t('export.dayEmpty'))
        else message.error(t('export.dayFailed'))
      }
    })

  return (
    <>
      <Button icon={<FileZipOutlined />} loading={busy} onClick={onClick}>
        {t('stock.exportDay')}
      </Button>
      <ExportProgressModal open={busy} text={t('export.dayExporting')} onCancel={cancel} />
    </>
  )
}
