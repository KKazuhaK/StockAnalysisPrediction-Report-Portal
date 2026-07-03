import { Navigate, Route, Routes } from 'react-router-dom'
import { App as AntdApp, ConfigProvider, Spin, theme } from 'antd'
import { PrefsProvider, usePrefs } from './prefs'
import { AuthProvider, useAuth } from './auth'
import { SiteProvider } from './site'
import { lazyRetry } from './lib/lazyRetry'
import AppLayout from './components/AppLayout'
import LoginPage from './pages/LoginPage'

// Route pages are lazy-loaded (Suspense boundary lives in AppLayout), so the first
// paint only ships the shell + landing page; admin / batch / webhook code and the
// markdown renderer load on demand. lazyRetry (not React.lazy directly) recovers
// from a stale chunk — the dist is wiped clean on every build, so a tab left open
// across a deploy can 404 on a route it hasn't loaded yet; see lib/lazyRetry.ts.
const HomePage = lazyRetry(() => import('./pages/HomePage'))
const StockPage = lazyRetry(() => import('./pages/StockPage'))
const RunPage = lazyRetry(() => import('./pages/RunPage'))
const ResearchPage = lazyRetry(() => import('./pages/ResearchPage'))
const ManageLayout = lazyRetry(() => import('./pages/manage/ManageLayout'))
const LinksPage = lazyRetry(() => import('./pages/manage/LinksPage'))
const TypesPage = lazyRetry(() => import('./pages/manage/TypesPage'))
const UsersPage = lazyRetry(() => import('./pages/manage/UsersPage'))
const SettingsPage = lazyRetry(() => import('./pages/manage/SettingsPage'))
const BatchAdminPage = lazyRetry(() => import('./pages/manage/BatchAdminPage'))
const WebhooksPage = lazyRetry(() => import('./pages/manage/WebhooksPage'))
const AppsHub = lazyRetry(() => import('./pages/AppsHub'))
const BatchConsole = lazyRetry(() => import('./pages/BatchConsole'))

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
