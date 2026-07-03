import { Navigate, Route, Routes } from 'react-router-dom'
import { App as AntdApp, ConfigProvider, Spin, theme } from 'antd'
import { PrefsProvider, usePrefs } from './prefs'
import { AuthProvider, useAuth } from './auth'
import AppLayout from './components/AppLayout'
import LoginPage from './pages/LoginPage'
import HomePage from './pages/HomePage'
import StockPage from './pages/StockPage'
import RunPage from './pages/RunPage'
import ResearchPage from './pages/ResearchPage'
import ManageLayout from './pages/manage/ManageLayout'
import LinksPage from './pages/manage/LinksPage'
import TypesPage from './pages/manage/TypesPage'
import UsersPage from './pages/manage/UsersPage'
import SettingsPage from './pages/manage/SettingsPage'
import BatchAdminPage from './pages/manage/BatchAdminPage'
import WebhooksPage from './pages/manage/WebhooksPage'
import AppsHub from './pages/AppsHub'
import BatchConsole from './pages/BatchConsole'

function FullSpin() {
  return (
    <div style={{ height: '100vh', display: 'grid', placeItems: 'center' }}>
      <Spin size="large" />
    </div>
  )
}

function Protected({ children }: { children: React.ReactNode }) {
  const { user, loading } = useAuth()
  if (loading) return <FullSpin />
  if (!user) return <Navigate to="/login" replace />
  return <>{children}</>
}

function AdminOnly({ children }: { children: React.ReactNode }) {
  const { admin } = useAuth()
  if (!admin) return <Navigate to="/" replace />
  return <>{children}</>
}

function RequirePerm({ perm, children }: { perm: string; children: React.ReactNode }) {
  const { can } = useAuth()
  if (!can(perm)) return <Navigate to="/" replace />
  return <>{children}</>
}

function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          <Protected>
            <AppLayout />
          </Protected>
        }
      >
        <Route path="/" element={<HomePage />} />
        <Route path="/research" element={<ResearchPage />} />
        <Route path="/stock/:symbol" element={<StockPage />} />
        <Route path="/run/:key" element={<RunPage />} />
        <Route
          path="/apps"
          element={
            <RequirePerm perm="run_batch">
              <AppsHub />
            </RequirePerm>
          }
        />
        <Route
          path="/apps/batch"
          element={
            <RequirePerm perm="run_batch">
              <BatchConsole />
            </RequirePerm>
          }
        />
        <Route
          path="/manage"
          element={
            <AdminOnly>
              <ManageLayout />
            </AdminOnly>
          }
        >
          <Route index element={<Navigate to="links" replace />} />
          <Route path="links" element={<LinksPage />} />
          <Route path="types" element={<TypesPage />} />
          <Route path="users" element={<UsersPage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="batch" element={<BatchAdminPage />} />
          <Route path="webhooks" element={<WebhooksPage />} />
        </Route>
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

function Themed() {
  const { dark, antd } = usePrefs()

  return (
    <ConfigProvider
      locale={antd}
      theme={{
        algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
        token: { colorPrimary: '#1677ff', borderRadius: 8 },
        cssVar: true,
      }}
    >
      <AntdApp style={{ height: '100%' }}>
        <AuthProvider>
          <AppRoutes />
        </AuthProvider>
      </AntdApp>
    </ConfigProvider>
  )
}

export default function App() {
  return (
    <PrefsProvider>
      <Themed />
    </PrefsProvider>
  )
}
