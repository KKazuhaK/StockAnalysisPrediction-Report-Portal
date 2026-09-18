import { useEffect, useMemo, useState, type ClipboardEvent } from 'react'
import {
  Alert,
  Button,
  Input,
  InputNumber,
  Popconfirm,
  Popover,
  Segmented,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
  Upload,
  type TableColumnsType,
} from 'antd'
import {
  DeleteOutlined,
  DownloadOutlined,
  PlusOutlined,
  QuestionCircleOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'

import type { PluginInput } from '../api/types'
import {
  activeBatchRows,
  blankBatchRow,
  missingCSVHeaders,
  missingRequiredCells,
  pasteBatchGrid,
  type BatchDraftRow,
} from '../lib/batchRows'
import { csvToRows, downloadCSV, toCSV } from '../lib/csv'
import { inputDisplayMeta } from '../lib/difyInputs'

type EditorMode = 'grid' | 'csv'
type TableRow = { values: BatchDraftRow; rowIndex: number }

export default function BatchRowsEditor({
  inputs,
  rows,
  onChange,
  onValidityChange,
}: {
  inputs: PluginInput[]
  rows: BatchDraftRow[]
  onChange: (rows: BatchDraftRow[]) => void
  onValidityChange?: (valid: boolean) => void
}) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<EditorMode>('grid')
  const [rawCSV, setRawCSV] = useState(() => toCSV(inputs.map((input) => input.key), []))

  const keys = useMemo(() => inputs.map((input) => input.key), [inputs])
  const displayedRows = rows.length > 0 ? rows : [blankBatchRow(inputs)]
  const readyRows = useMemo(() => activeBatchRows(rows), [rows])
  const missingCells = useMemo(() => missingRequiredCells(rows, inputs), [rows, inputs])
  const missingCellSet = useMemo(
    () => new Set(missingCells.map((cell) => `${cell.rowIndex}:${cell.key}`)),
    [missingCells],
  )
  const headerMissing = useMemo(
    () => (mode === 'csv' ? missingCSVHeaders(rawCSV, inputs) : []),
    [mode, rawCSV, inputs],
  )
  const valid = readyRows.length > 0 && missingCells.length === 0 && headerMissing.length === 0

  useEffect(() => {
    onValidityChange?.(valid)
  }, [onValidityChange, valid])

  const setModeAndSync = (next: EditorMode) => {
    if (next === mode) return
    if (next === 'csv') {
      setRawCSV(toCSV(keys, readyRows.map((row) => keys.map((key) => row[key] ?? ''))))
    } else {
      const parsed = csvToRows(rawCSV, keys)
      onChange(parsed.length > 0 ? parsed : [blankBatchRow(inputs)])
    }
    setMode(next)
  }

  const updateCell = (rowIndex: number, key: string, value: string) => {
    const next = displayedRows.map((row) => ({ ...row }))
    next[rowIndex][key] = value
    onChange(next)
  }

  const handlePaste = (event: ClipboardEvent<HTMLElement>, rowIndex: number, columnIndex: number) => {
    const text = event.clipboardData.getData('text')
    if (!text.includes('\n') && !text.includes('\t')) return
    event.preventDefault()
    onChange(pasteBatchGrid(displayedRows, inputs, rowIndex, columnIndex, text))
  }

  const removeRow = (rowIndex: number) => {
    const next = displayedRows.filter((_row, index) => index !== rowIndex)
    onChange(next.length > 0 ? next : [blankBatchRow(inputs)])
  }

  const importCSV = (file: File) => {
    const reader = new FileReader()
    reader.onload = () => {
      const text = String(reader.result || '')
      setRawCSV(text)
      onChange(csvToRows(text, keys))
      setMode('csv')
    }
    reader.readAsText(file)
    return false
  }

  const currentCSV = () =>
    mode === 'csv' ? rawCSV : toCSV(keys, readyRows.map((row) => keys.map((key) => row[key] ?? '')))

  const fieldControl = (input: PluginInput, rowIndex: number, columnIndex: number) => {
    const { title } = inputDisplayMeta(input)
    const value = displayedRows[rowIndex]?.[input.key] ?? ''
    const status = missingCellSet.has(`${rowIndex}:${input.key}`) ? 'error' : undefined
    const label = t('batch.editor.cellLabel', { row: rowIndex + 1, field: title })
    const common = {
      value,
      status: status as 'error' | undefined,
      'aria-label': label,
      onPaste: (event: ClipboardEvent<HTMLElement>) => handlePaste(event, rowIndex, columnIndex),
    }

    if (input.type === 'select' && input.options?.length) {
      return (
        <Select
          {...common}
          allowClear
          showSearch
          options={input.options.map((option) => ({ value: option, label: option }))}
          onChange={(next) => updateCell(rowIndex, input.key, String(next ?? ''))}
        />
      )
    }
    if (input.type === 'number') {
      return (
        <InputNumber
          {...common}
          stringMode
          controls={false}
          onChange={(next) => updateCell(rowIndex, input.key, String(next ?? ''))}
        />
      )
    }
    if (input.type === 'paragraph') {
      return (
        <Input.TextArea
          {...common}
          autoSize={{ minRows: 1, maxRows: 3 }}
          onChange={(event) => updateCell(rowIndex, input.key, event.target.value)}
        />
      )
    }
    return <Input {...common} onChange={(event) => updateCell(rowIndex, input.key, event.target.value)} />
  }

  const columns: TableColumnsType<TableRow> = [
    {
      title: '#',
      dataIndex: 'rowIndex',
      key: 'rowIndex',
      width: 54,
      fixed: 'left',
      align: 'center',
      render: (rowIndex: number) => <Typography.Text type="secondary">{rowIndex + 1}</Typography.Text>,
    },
    ...inputs.map((input, columnIndex) => {
      const { title, detail } = inputDisplayMeta(input)
      return {
        title: (
          <div className="rp-batch-grid-heading">
            <div className="rp-batch-grid-heading__title">
              <Tooltip title={title}>
                <span className="rp-batch-grid-heading__name">{title}</span>
              </Tooltip>
              {input.required ? <span className="rp-batch-grid-heading__required">*</span> : <span className="rp-batch-grid-heading__optional">{t('run.optionalMark')}</span>}
              {detail && (
                <Popover title={title} content={<div className="rp-run-input-help__content">{detail}</div>} trigger="click">
                  <Button
                    type="text"
                    size="small"
                    shape="circle"
                    className="rp-batch-grid-heading__help"
                    icon={<QuestionCircleOutlined />}
                    aria-label={t('run.inputHelp', { field: title })}
                  />
                </Popover>
              )}
            </div>
            {title !== input.key && <Typography.Text code>{input.key}</Typography.Text>}
          </div>
        ),
        key: input.key,
        width: 220,
        render: (_value: string, record: TableRow) => fieldControl(input, record.rowIndex, columnIndex),
      }
    }),
    {
      key: 'actions',
      width: 62,
      fixed: 'right',
      align: 'center',
      render: (_value, record) => (
        <Tooltip title={t('batch.editor.deleteRow')}>
          <Button
            type="text"
            danger
            icon={<DeleteOutlined />}
            aria-label={t('batch.editor.deleteRowN', { row: record.rowIndex + 1 })}
            onClick={() => removeRow(record.rowIndex)}
          />
        </Tooltip>
      ),
    },
  ]

  const tableRows: TableRow[] = displayedRows.map((values, rowIndex) => ({ values, rowIndex }))
  const missingRowCount = new Set(missingCells.map((cell) => cell.rowIndex)).size

  return (
    <div className="rp-batch-rows-editor">
      <div className="rp-batch-editor-toolbar">
        <Space wrap size={8}>
          <Segmented
            value={mode}
            onChange={(value) => setModeAndSync(value as EditorMode)}
            options={[
              { value: 'grid', label: t('batch.editor.gridMode') },
              { value: 'csv', label: t('batch.editor.csvMode') },
            ]}
          />
          <Tag color={readyRows.length > 0 ? 'blue' : undefined}>{t('batch.parsedRows', { n: readyRows.length })}</Tag>
        </Space>
        <Space wrap size={4}>
          <Upload accept=".csv,.txt" showUploadList={false} beforeUpload={importCSV}>
            <Button size="small" icon={<UploadOutlined />}>{t('batch.uploadCsv')}</Button>
          </Upload>
          <Button size="small" icon={<DownloadOutlined />} onClick={() => downloadCSV('batch-rows.csv', currentCSV())}>
            {t('batch.editor.downloadRows')}
          </Button>
          <Popconfirm
            title={t('batch.editor.clearConfirm')}
            onConfirm={() => {
              setRawCSV(toCSV(keys, []))
              onChange([blankBatchRow(inputs)])
            }}
          >
            <Button size="small" danger>{t('batch.editor.clear')}</Button>
          </Popconfirm>
        </Space>
      </div>

      {mode === 'grid' ? (
        <>
          <Typography.Paragraph type="secondary" className="rp-batch-editor-hint">
            {t('batch.editor.pasteHint')}
          </Typography.Paragraph>
          <div className="rp-batch-grid">
            <Table<TableRow>
              size="small"
              rowKey={(record) => String(record.rowIndex)}
              columns={columns}
              dataSource={tableRows}
              pagination={tableRows.length > 20 ? { defaultPageSize: 20, showSizeChanger: true } : false}
              scroll={{ x: Math.max(720, inputs.length * 220 + 116), y: 420 }}
            />
          </div>
          <Button
            className="rp-batch-editor-add"
            icon={<PlusOutlined />}
            onClick={() => onChange([...displayedRows, blankBatchRow(inputs)])}
          >
            {t('batch.editor.addRow')}
          </Button>
        </>
      ) : (
        <Input.TextArea
          className="rp-batch-csv-editor"
          rows={12}
          value={rawCSV}
          onChange={(event) => {
            const text = event.target.value
            setRawCSV(text)
            onChange(csvToRows(text, keys))
          }}
          placeholder={t('batch.csvPlaceholder', { keys: keys.join(',') })}
        />
      )}

      {headerMissing.length > 0 ? (
        <Alert type="error" showIcon title={t('batch.editor.missingHeaders', { fields: headerMissing.join(', ') })} />
      ) : missingCells.length > 0 ? (
        <Alert
          type="error"
          showIcon
          title={t('batch.editor.missingRequired', { cells: missingCells.length, rows: missingRowCount })}
        />
      ) : readyRows.length > 0 ? (
        <Alert type="success" showIcon title={t('batch.editor.ready', { n: readyRows.length })} />
      ) : (
        <Alert type="info" showIcon title={t('batch.editor.empty')} />
      )}
    </div>
  )
}
