import { Layout, Menu, Result, Typography } from 'antd'
import {
  AppstoreOutlined,
  CloudUploadOutlined,
  DashboardOutlined,
  ExperimentOutlined,
  FileTextOutlined,
} from '@ant-design/icons'
import { Link, Navigate, Route, Routes, useLocation } from 'react-router-dom'

import RetrievalDebug from './pages/RetrievalDebug'
import Dashboard from './pages/Dashboard'
import Restaurants from './pages/Restaurants'
import RestaurantDetailPage from './pages/RestaurantDetail'
import Documents from './pages/Documents'
import DocumentDetailPage from './pages/DocumentDetail'
import Ingestion from './pages/Ingestion'
import IngestionDetailPage from './pages/IngestionDetail'

const { Header, Sider, Content } = Layout
const { Title } = Typography

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

export default function App() {
  const location = useLocation()

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider theme="light" width={220}>
        <Header
          style={{
            background: 'transparent',
            paddingLeft: 24,
            height: 'auto',
            lineHeight: 'normal',
          }}
        >
          <Title level={4} style={{ margin: '16px 0' }}>
            PlatePilot
          </Title>
        </Header>
        <Menu
          mode="inline"
          selectedKeys={[selectedKey(location.pathname)]}
          items={menuItems}
        />
      </Sider>
      <Layout>
        <Content style={{ padding: 24 }}>
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
        </Content>
      </Layout>
    </Layout>
  )
}
