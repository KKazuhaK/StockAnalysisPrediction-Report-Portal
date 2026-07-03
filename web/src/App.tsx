import { lazy } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { App as AntdApp, ConfigProvider, Spin, theme } from 'antd'
import { PrefsProvider, usePrefs } from './prefs'
import { AuthProvider, useAuth } from './auth'
import { SiteProvider } from './site'
import AppLayout from './components/AppLayout'
import LoginPage from './pages/LoginPage'

// Route pages are lazy-loaded (Suspense boundary lives in AppLayout), so the first
// paint only ships the shell + landing page; admin / batch / webhook code and the
// markdown renderer load on demand.
const HomePage = lazy(() => import('./pages/HomePage'))
const StockPage = lazy(() => import('./pages/StockPage'))
const RunPage = lazy(() => import('./pages/RunPage'))
const ResearchPage = lazy(() => import('./pages/ResearchPage'))
const ManageLayout = lazy(() => import('./pages/manage/ManageLayout'))
const LinksPage = lazy(() => import('./pages/manage/LinksPage'))
const TypesPage = lazy(() => import('./pages/manage/TypesPage'))
const UsersPage = lazy(() => import('./pages/manage/UsersPage'))
const SettingsPage = lazy(() => import('./pages/manage/SettingsPage'))
const BatchAdminPage = lazy(() => import('./pages/manage/BatchAdminPage'))
const WebhooksPage = lazy(() => import('./pages/manage/WebhooksPage'))
const AppsHub = lazy(() => import('./pages/AppsHub'))
const BatchConsole = lazy(() => import('./pages/BatchConsole'))

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
      <SiteProvider>
        <Themed />
      </SiteProvider>
    </PrefsProvider>
  )
}
