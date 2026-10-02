import { Layout, Menu, Result } from 'antd'
import {
  AppstoreOutlined,
  CloudUploadOutlined,
  DashboardOutlined,
  ExperimentOutlined,
  FileTextOutlined,
} from '@ant-design/icons'
import { useState } from 'react'
import { Link, Navigate, Route, Routes, useLocation } from 'react-router-dom'

import RetrievalDebug from './pages/RetrievalDebug'
import Dashboard from './pages/Dashboard'
import Restaurants from './pages/Restaurants'
import RestaurantDetailPage from './pages/RestaurantDetail'
import Documents from './pages/Documents'
import DocumentDetailPage from './pages/DocumentDetail'
import Ingestion from './pages/Ingestion'
import IngestionDetailPage from './pages/IngestionDetail'

const { Sider, Content } = Layout

const menuItems = [
  {
    key: '/dashboard',
    icon: <DashboardOutlined />,
    label: <Link to="/dashboard">Dashboard</Link>,
  },
  {
    key: '/restaurants',
    icon: <AppstoreOutlined />,
    label: <Link to="/restaurants">Restaurants</Link>,
  },
  {
    key: '/documents',
    icon: <FileTextOutlined />,
    label: <Link to="/documents">Documents</Link>,
  },
  {
    key: '/ingestion',
    icon: <CloudUploadOutlined />,
    label: <Link to="/ingestion">Ingestion</Link>,
  },
  {
    key: '/retrieval-debug',
    icon: <ExperimentOutlined />,
    label: <Link to="/retrieval-debug">Retrieval Debug</Link>,
  },
]

// selectedKey maps the current path to its top-level menu entry so a detail
// page keeps its section highlighted.
function selectedKey(pathname: string): string {
  const match = menuItems.find((item) => pathname.startsWith(item.key))
  return match?.key ?? pathname
}

function Brand({ collapsed }: { collapsed: boolean }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: collapsed ? 'center' : 'flex-start',
        gap: 10,
        padding: collapsed ? '20px 0 16px' : '20px 16px 16px',
      }}
    >
      <div
        style={{
          width: 28,
          height: 28,
          borderRadius: 7,
          background: '#1d1d1f',
          color: '#fff',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          fontWeight: 600,
          fontSize: 14,
          flexShrink: 0,
        }}
      >
        P
      </div>
      {!collapsed && (
        <div>
          <div style={{ fontWeight: 600, fontSize: 15, lineHeight: '20px', color: '#1d1d1f' }}>
            PlatePilot
          </div>
          <div style={{ fontSize: 12, lineHeight: '16px', color: '#86868b' }}>
            Admin console
          </div>
        </div>
      )}
    </div>
  )
}

export default function App() {
  const location = useLocation()
  // The sidebar collapses itself below the lg breakpoint so a half-screen
  // browser window next to an IDE keeps the content usable.
  const [collapsed, setCollapsed] = useState(false)

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider
        width={232}
        collapsedWidth={64}
        collapsed={collapsed}
        onCollapse={setCollapsed}
        breakpoint="lg"
        style={{ borderRight: '1px solid rgba(0, 0, 0, 0.06)' }}
      >
        <Brand collapsed={collapsed} />
        <Menu
          mode="inline"
          inlineCollapsed={collapsed}
          style={{ borderInlineEnd: 'none', padding: collapsed ? '0 12px' : '0 8px' }}
          selectedKeys={[selectedKey(location.pathname)]}
          items={menuItems}
        />
      </Sider>
      <Layout>
        <Content style={{ padding: '32px 40px 48px' }}>
          <div style={{ maxWidth: 1280, margin: '0 auto' }}>
            <Routes>
              <Route path="/" element={<Navigate to="/dashboard" replace />} />
              <Route path="/dashboard" element={<Dashboard />} />
              <Route path="/restaurants" element={<Restaurants />} />
              <Route path="/restaurants/:id" element={<RestaurantDetailPage />} />
              <Route path="/documents" element={<Documents />} />
              <Route path="/documents/:id" element={<DocumentDetailPage />} />
              <Route path="/ingestion" element={<Ingestion />} />
              <Route path="/ingestion/:id" element={<IngestionDetailPage />} />
              <Route path="/retrieval-debug" element={<RetrievalDebug />} />
              <Route
                path="*"
                element={<Result status="404" title="404" subTitle="page not found" />}
              />
            </Routes>
          </div>
        </Content>
      </Layout>
    </Layout>
  )
}
