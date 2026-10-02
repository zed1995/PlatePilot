import { useQuery } from '@tanstack/react-query'
import {
  Alert,
  Card,
  Col,
  Descriptions,
  Row,
  Space,
  Statistic,
  Table,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { Link } from 'react-router-dom'

import { adminApi } from '../api/client'
import type {
  BatchListItem,
  Boundary,
  DocumentCount,
  Migration,
  Overview,
} from '../api/types'
import PageHeader from '../components/PageHeader'
import StatusTag from '../components/StatusTag'
import { formatDuration, formatTime, statusColor } from '../format'

const breakdownColumns: ColumnsType<DocumentCount> = [
  { title: 'Scope', dataIndex: 'retrieval_scope', width: 96 },
  { title: 'Doc type', dataIndex: 'doc_type', ellipsis: true },
  { title: 'Count', dataIndex: 'count', width: 72 },
]

const batchColumns: ColumnsType<BatchListItem> = [
  {
    title: 'Batch',
    dataIndex: 'batch_id',
    width: 70,
    render: (id: number) => <Link to={`/ingestion/${id}`}>{id}</Link>,
  },
  { title: 'Stage', dataIndex: 'stage', width: 64 },
  {
    title: 'Status',
    dataIndex: 'status',
    width: 96,
    render: (status: string) => (
      <StatusTag tone={statusColor(status)}>{status}</StatusTag>
    ),
  },
  {
    title: 'Started',
    dataIndex: 'started_at',
    ellipsis: true,
    render: (value: string) => formatTime(value),
  },
  {
    title: 'Duration',
    dataIndex: 'duration_ms',
    width: 80,
    render: (ms: number) => formatDuration(ms),
  },
]

const boundaryColumns: ColumnsType<Boundary> = [
  { title: 'ID', dataIndex: 'boundary_id', width: 56 },
  { title: 'Name', dataIndex: 'name', ellipsis: true },
  { title: 'Kind', dataIndex: 'kind', width: 76 },
  {
    title: 'Loaded',
    dataIndex: 'loaded_at',
    ellipsis: true,
    render: formatTime,
  },
]

const migrationColumns: ColumnsType<Migration> = [
  { title: 'Version', dataIndex: 'version', ellipsis: true },
  {
    title: 'Applied',
    dataIndex: 'applied_at',
    width: 120,
    ellipsis: true,
    render: formatTime,
  },
]

export default function Dashboard() {
  const overviewQuery = useQuery<Overview>({
    queryKey: ['overview'],
    queryFn: adminApi.overview,
  })
  const boundariesQuery = useQuery<Boundary[]>({
    queryKey: ['boundaries'],
    queryFn: adminApi.boundaries,
  })

  if (overviewQuery.isError) {
    return (
      <Alert
        type="error"
        showIcon
        message="Failed to load the overview"
        description={
          overviewQuery.error instanceof Error
            ? overviewQuery.error.message
            : String(overviewQuery.error)
        }
      />
    )
  }

  const overview = overviewQuery.data
  const tables = overview?.tables

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <PageHeader
        title="Overview"
        description="Health of the knowledge base and recent ingestion activity."
      />

      <Row gutter={[16, 16]}>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={overviewQuery.isLoading}>
            <Statistic
              title="Restaurants (active)"
              value={tables?.restaurants_total ?? 0}
              suffix={tables ? `/ ${tables.restaurants_active}` : ''}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={overviewQuery.isLoading}>
            <Statistic
              title={tables?.reviews_estimated ? 'Reviews (estimated)' : 'Reviews'}
              value={tables?.reviews_estimate ?? 0}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={overviewQuery.isLoading}>
            <Statistic title="Active documents" value={tables?.documents_active ?? 0} />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card loading={overviewQuery.isLoading}>
            <Statistic title="Batches" value={tables?.batches_total ?? 0} />
          </Card>
        </Col>
      </Row>

      {overview && (
        overview.active_documents_without_vector === 0 ? (
          <Typography.Text type="secondary">
            Vector health: every active document carries a vector.
          </Typography.Text>
        ) : (
          <Alert
            showIcon
            type="error"
            message={`Vector health violation: ${overview.active_documents_without_vector} active documents lack a vector.`}
          />
        )
      )}

      <Row gutter={[16, 16]}>
        <Col xs={24} lg={8}>
          <Card title="Document distribution (active)">
            <Table<DocumentCount>
              size="middle"
              tableLayout="fixed"
              rowKey={(row) => `${row.retrieval_scope}-${row.doc_type}`}
              columns={breakdownColumns}
              dataSource={overview?.document_breakdown ?? []}
              pagination={false}
              loading={overviewQuery.isLoading}
            />
          </Card>
        </Col>
        <Col xs={24} lg={16}>
          <Card title="Recent batches">
            <Table<BatchListItem>
              size="middle"
              tableLayout="fixed"
              rowKey="batch_id"
              columns={batchColumns}
              dataSource={overview?.recent_batches ?? []}
              pagination={false}
              loading={overviewQuery.isLoading}
            />
          </Card>
        </Col>
      </Row>

      <Row gutter={[16, 16]}>
        <Col xs={24} md={12} xl={8}>
          <Card title="Environment" size="small">
            <Descriptions size="small" column={1}>
              <Descriptions.Item label="Embedding model">
                {overview?.environment.embedding_model || '-'}
              </Descriptions.Item>
              <Descriptions.Item label="Dimensions">
                {overview?.environment.embedding_dimensions || '-'}
              </Descriptions.Item>
            </Descriptions>
          </Card>
        </Col>
        <Col xs={24} md={12} xl={8}>
          <Card title="Migrations" size="small">
            <Table<Migration>
              size="small"
              tableLayout="fixed"
              rowKey="version"
              columns={migrationColumns}
              dataSource={overview?.migrations ?? []}
              pagination={false}
              loading={overviewQuery.isLoading}
            />
          </Card>
        </Col>
        <Col xs={24} md={12} xl={8}>
          <Card title="Boundaries" size="small">
            <Table<Boundary>
              size="small"
              tableLayout="fixed"
              rowKey="boundary_id"
              columns={boundaryColumns}
              dataSource={boundariesQuery.data ?? []}
              pagination={false}
              loading={boundariesQuery.isLoading}
            />
          </Card>
        </Col>
      </Row>
    </Space>
  )
}
