import { Navigate, Route, Routes } from 'react-router-dom'
import { PageShell } from './components/page-shell'
import { Dashboard } from './pages/Dashboard'
import { Restaurants } from './pages/Restaurants'
import { RestaurantDetailPage } from './pages/RestaurantDetail'
import { Documents } from './pages/Documents'
import { DocumentDetailPage } from './pages/DocumentDetail'
import { Ingestion } from './pages/Ingestion'
import { IngestionDetailPage } from './pages/IngestionDetail'
import { RetrievalDebug } from './pages/RetrievalDebug'
import { Inventory } from './pages/Inventory'
import AgentConsole from './pages/AgentConsole'
import { EmptyState } from './components/empty-state'

export default function App() {
  return (
    <PageShell>
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
        {/* The inventory page shares the Agent Console's exception: its
            reset button is the one write the console performs. */}
        <Route path="/inventory" element={<Inventory />} />
        {/* The one page that is not read-only; see web/AGENTS.md. */}
        <Route path="/agent" element={<AgentConsole />} />
        <Route
          path="*"
          element={<EmptyState title="404" description="page not found" />}
        />
      </Routes>
    </PageShell>
  )
}