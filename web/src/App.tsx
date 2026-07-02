import { useEffect } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { App as AntdApp, ConfigProvider, Spin, theme } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import enUS from 'antd/locale/en_US'
import dayjs from 'dayjs'
import 'dayjs/locale/zh-cn'
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
        </Route>
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

function Themed() {
  const { dark, locale } = usePrefs()

  useEffect(() => {
    dayjs.locale(locale === 'zh' ? 'zh-cn' : 'en')
  }, [locale])

  return (
    <ConfigProvider
      locale={locale === 'zh' ? zhCN : enUS}
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
