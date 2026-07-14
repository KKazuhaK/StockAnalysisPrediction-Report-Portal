import { Navigate, Route, Routes } from 'react-router-dom'
import { App as AntdApp, ConfigProvider, Spin, theme } from 'antd'
import { PrefsProvider, usePrefs } from './prefs'
import { AuthProvider, useAuth } from './auth'
import { SiteProvider } from './site'
import { lazyRetry } from './lib/lazyRetry'
import AppLayout from './components/AppLayout'
import LoginPage from './pages/LoginPage'
import ResetPasswordPage from './pages/ResetPasswordPage'

// Route pages are lazy-loaded (Suspense boundary lives in AppLayout), so the first
// paint only ships the shell + landing page; admin / batch / webhook code and the
// markdown renderer load on demand. lazyRetry (not React.lazy directly) recovers
// from a stale chunk — the dist is wiped clean on every build, so a tab left open
// across a deploy can 404 on a route it hasn't loaded yet; see lib/lazyRetry.ts.
const HomePage = lazyRetry(() => import('./pages/HomePage'))
const StockPage = lazyRetry(() => import('./pages/StockPage'))
const RunPage = lazyRetry(() => import('./pages/RunPage'))
const ManageLayout = lazyRetry(() => import('./pages/manage/ManageLayout'))
const LinksPage = lazyRetry(() => import('./pages/manage/LinksPage'))
const TypesPage = lazyRetry(() => import('./pages/manage/TypesPage'))
const UsersPage = lazyRetry(() => import('./pages/manage/UsersPage'))
const SiteSettingsPage = lazyRetry(() => import('./pages/manage/SiteSettingsPage'))
const AnnouncementPage = lazyRetry(() => import('./pages/manage/AnnouncementPage'))
const EmailPage = lazyRetry(() => import('./pages/manage/EmailPage'))
const TokensPage = lazyRetry(() => import('./pages/manage/TokensPage'))
const ApiDocPage = lazyRetry(() => import('./pages/manage/ApiDocPage'))
const BatchAdminPage = lazyRetry(() => import('./pages/manage/BatchAdminPage'))
const RunQueueSettingsPage = lazyRetry(() => import('./pages/manage/RunQueueSettingsPage'))
const ChatAdminPage = lazyRetry(() => import('./pages/manage/ChatAdminPage'))
const WebhooksPage = lazyRetry(() => import('./pages/manage/WebhooksPage'))
const StoragePage = lazyRetry(() => import('./pages/manage/StoragePage'))
const AppsHub = lazyRetry(() => import('./pages/AppsHub'))
const AppView = lazyRetry(() => import('./pages/AppView'))
const AppsAdminPage = lazyRetry(() => import('./pages/manage/AppsAdminPage'))
const BatchConsole = lazyRetry(() => import('./pages/BatchConsole'))
const RecurringConsole = lazyRetry(() => import('./pages/RecurringConsole'))
const QueuePage = lazyRetry(() => import('./pages/QueuePage'))
const ChatPage = lazyRetry(() => import('./pages/ChatPage'))

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
      <Route path="/reset" element={<ResetPasswordPage />} />
      <Route
        element={
          <Protected>
            <AppLayout />
          </Protected>
        }
      >
        <Route path="/" element={<HomePage />} />
        <Route path="/stock/:symbol" element={<StockPage />} />
        <Route path="/run/:key" element={<RunPage />} />
        {/* The apps hub and installed iframe apps are open to any logged-in user;
            the built-in batch console stays permission-gated. */}
        <Route path="/apps" element={<AppsHub />} />
        <Route path="/apps/x/:id" element={<AppView />} />
        <Route
          path="/apps/batch"
          element={
            <RequirePerm perm="run_batch">
              <BatchConsole />
            </RequirePerm>
          }
        />
        <Route
          path="/apps/recurring"
          element={
            <RequirePerm perm="run_batch">
              <RecurringConsole />
            </RequirePerm>
          }
        />
        <Route
          path="/queue"
          element={
            <RequirePerm perm="run_batch">
              <QueuePage />
            </RequirePerm>
          }
        />
        <Route
          path="/chat"
          element={
            <RequirePerm perm="run_batch">
              <ChatPage />
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
          <Route index element={<Navigate to="site" replace />} />
          <Route path="site" element={<SiteSettingsPage />} />
          <Route path="announcement" element={<AnnouncementPage />} />
          <Route path="email" element={<EmailPage />} />
          <Route path="links" element={<LinksPage />} />
          <Route path="types" element={<TypesPage />} />
          <Route path="users" element={<UsersPage />} />
          <Route path="tokens" element={<TokensPage />} />
          <Route path="batch" element={<BatchAdminPage />} />
          <Route path="runqueue" element={<RunQueueSettingsPage />} />
          <Route path="assistant" element={<ChatAdminPage />} />
          <Route path="apps" element={<AppsAdminPage />} />
          <Route path="webhooks" element={<WebhooksPage />} />
          <Route path="storage" element={<StoragePage />} />
          <Route path="apidoc" element={<ApiDocPage />} />
          {/* Back-compat: the old catch-all Settings tab split into these pages. */}
          <Route path="settings" element={<Navigate to="/manage/site" replace />} />
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
      // One loading spinner everywhere: the same ring as the boot splash (index.html),
      // instead of antd's default 4-dot spinner. Sizing tracks Spin's size via CSS.
      spin={{ indicator: <span className="rp-spin-ring" /> }}
      theme={{
        algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
        token: { colorPrimary: '#1677ff', borderRadius: 8 },
        // antd 6 gives a left/right-titled Divider a leading rail (orientationMargin token,
        // default 5%) so the text floats inward. Our section headers want the title flush to
        // the edge, so zero the token globally — the native way, no per-Divider prop or CSS.
        components: { Divider: { orientationMargin: 0 } },
        // A cssVar key PER THEME, not the single useId `cssVar: true` gives. antd caches the
        // generated CSS variables under this key; with one shared key, switching the algorithm at
        // runtime does not regenerate them, so neutral tokens (default button/select/tag fills)
        // stay stuck on the previous palette while colored tokens (colorPrimary is the same hex in
        // both) still look right. Distinct keys give each palette its own scope, so a dark→light
        // switch cleanly applies the light variables. See antd cssVar "unique key per theme".
        cssVar: { key: dark ? 'rp-dark' : 'rp-light' },
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
