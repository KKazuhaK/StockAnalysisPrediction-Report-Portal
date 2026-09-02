import { Button } from 'antd'
import { EditOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../auth'

// "Edit this report" (ADR 0026), on every page that shows one. Renders nothing without the
// permission, so the two reading pages need no gate of their own.
//
// It always navigates to /report/new?from=<id>, whatever the report is, because only the server can
// say what editing this one means: a hand-written report is edited in place, a machine-generated one
// is the seed for a new hand-written form (its own row, at the manual version — the run's record is
// not modifiable), and one that already HAS a hand-written form opens that. The editor asks and
// redirects. Deciding here would mean the reading page needing to know a report's version and
// whether a sibling exists, which is two more things to fetch and one more place to be wrong.
export default function EditReportButton({ reportId }: { reportId: number }) {
  const { can } = useAuth()
  const navigate = useNavigate()
  const { t } = useTranslation()
  if (!can('report_edit')) return null
  return (
    <Button icon={<EditOutlined />} onClick={() => navigate(`/report/new?from=${reportId}`)}>
      {t('reportEditor.edit')}
    </Button>
  )
}
