import React, { Suspense, lazy } from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import './styles.css'
import { AppProvider, ToastProvider, useMe, useTheme } from './lib/app'
import { Shell } from './components/Shell'
import { Loading } from './components/ui'
import Login from './pages/Login'
import Overview from './pages/Overview'

const Playground = lazy(() => import('./pages/Playground'))
const Keys = lazy(() => import('./pages/Keys'))
const Agents = lazy(() => import('./pages/Agents'))
const Marketplace = lazy(() => import('./pages/Marketplace'))
const Registry = lazy(() => import('./pages/Registry'))
const Guardrails = lazy(() => import('./pages/Guardrails'))
const Budgets = lazy(() => import('./pages/Budgets'))
const Models = lazy(() => import('./pages/Models'))
const Rollouts = lazy(() => import('./pages/Rollouts'))
const Providers = lazy(() => import('./pages/Providers'))
const Teams = lazy(() => import('./pages/Teams'))
const People = lazy(() => import('./pages/People'))
const SSO = lazy(() => import('./pages/SSO'))
const Requests = lazy(() => import('./pages/Requests'))
const Usage = lazy(() => import('./pages/Usage'))
const Audit = lazy(() => import('./pages/Audit'))
const Settings = lazy(() => import('./pages/Settings'))
const PlatformUsers = lazy(() => import('./pages/PlatformUsers'))
const System = lazy(() => import('./pages/System'))

const qc = new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: false, retry: 1, staleTime: 15_000 } } })

function Authed() {
  const me = useMe()
  const loc = useLocation()
  useTheme()
  if (me.isLoading) return <Loading label="Opening console" />
  if (me.error || !me.data) return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />
  if (!me.data.orgs.length) {
    return (
      <div className="login"><div className="form"><div className="card">
        <h2>You're signed in, but not part of an organization yet</h2>
        <p className="muted" style={{ marginTop: 8 }}>Ask an organization owner to add {me.data.user.email}, then reload this page.</p>
      </div></div></div>
    )
  }
  return (
    <AppProvider me={me.data}>
      <Shell>
        <Suspense fallback={<Loading />}>
          <Routes>
            <Route path="/" element={<Overview />} />
            <Route path="/playground" element={<Playground />} />
            <Route path="/keys" element={<Keys />} />
            <Route path="/agents" element={<Agents />} />
            <Route path="/marketplace" element={<Marketplace />} />
            <Route path="/registry" element={<Registry />} />
            <Route path="/guardrails" element={<Guardrails />} />
            <Route path="/budgets" element={<Budgets />} />
            <Route path="/models" element={<Models />} />
            <Route path="/rollouts" element={<Rollouts />} />
            <Route path="/providers" element={<Providers />} />
            <Route path="/teams" element={<Teams />} />
            <Route path="/people" element={<People />} />
            <Route path="/sso" element={<SSO />} />
            <Route path="/requests" element={<Requests />} />
            <Route path="/usage" element={<Usage />} />
            <Route path="/audit" element={<Audit />} />
            <Route path="/settings" element={<Settings />} />
            <Route path="/platform/users" element={<PlatformUsers />} />
            <Route path="/platform/system" element={<System />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Suspense>
      </Shell>
    </AppProvider>
  )
}

function App() {
  useTheme()
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/*" element={<Authed />} />
    </Routes>
  )
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </ToastProvider>
    </QueryClientProvider>
  </React.StrictMode>,
)
