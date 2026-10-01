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
  Tag,
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
import { formatDuration, formatTime, statusColor } from '../format'

const breakdownColumns: ColumnsType<DocumentCount> = [
  { title: 'Scope', dataIndex: 'retrieval_scope' },
  { title: 'Doc type', dataIndex: 'doc_type' },
  { title: 'Count', dataIndex: 'count' },
]

const batchColumns: ColumnsType<BatchListItem> = [
  {
    title: 'Batch',
    dataIndex: 'batch_id',
    render: (id: number) => <Link to={`/ingestion/${id}`}>{id}</Link>,
  },
  { title: 'Stage', dataIndex: 'stage' },
  {
    title: 'Status',
    dataIndex: 'status',
    render: (status: string) => <Tag color={statusColor(status)}>{status}</Tag>,
  },
  {
    title: 'Started',
    dataIndex: 'started_at',
    render: (value: string) => formatTime(value),
  },
  {
    title: 'Duration',
    dataIndex: 'duration_ms',
    render: (ms: number) => formatDuration(ms),
  },
]

const boundaryColumns: ColumnsType<Boundary> = [
  { title: 'ID', dataIndex: 'boundary_id', width: 80 },
  { title: 'Name', dataIndex: 'name' },
  { title: 'Kind', dataIndex: 'kind' },
  { title: 'Loaded', dataIndex: 'loaded_at', render: formatTime },
]

const migrationColumns: ColumnsType<Migration> = [
  { title: 'Version', dataIndex: 'version' },
  { title: 'Applied', dataIndex: 'applied_at', render: formatTime },
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
      <Row gutter={16}>
        <Col span={5}>
          <Card size="small" loading={overviewQuery.isLoading}>
            <Statistic
              title="Restaurants (active)"
              value={tables?.restaurants_total ?? 0}
              suffix={tables ? `/ ${tables.restaurants_active}` : ''}
            />
          </Card>
        </Col>
        <Col span={5}>
          <Card size="small" loading={overviewQuery.isLoading}>
            <Statistic
              title={
                <span>
                  Reviews{' '}
                  {tables?.reviews_estimated ? (
                    <Tag>estimated</Tag>
                  ) : null}
                </span>
              }
              value={tables?.reviews_estimate ?? 0}
            />
          </Card>
        </Col>
        <Col span={5}>
          <Card size="small" loading={overviewQuery.isLoading}>
            <Statistic
              title="Active documents"
              value={tables?.documents_active ?? 0}
            />
          </Card>
        </Col>
        <Col span={5}>
          <Card size="small" loading={overviewQuery.isLoading}>
            <Statistic title="Batches" value={tables?.batches_total ?? 0} />
          </Card>
        </Col>
      </Row>

      {overview && (
        <Alert
          showIcon
          type={overview.active_documents_without_vector === 0 ? 'success' : 'error'}
          message={
            overview.active_documents_without_vector === 0
              ? 'Vector health: every active document carries a vector.'
              : `Vector health violation: ${overview.active_documents_without_vector} active documents lack a vector.`
          }
        />
      )}

      <Row gutter={16}>
        <Col span={10}>
          <Card title="Document distribution (active)" size="small">
            <Table<DocumentCount>
              size="small"
              rowKey={(row) => `${row.retrieval_scope}-${row.doc_type}`}
              columns={breakdownColumns}
              dataSource={overview?.document_breakdown ?? []}
              pagination={false}
              loading={overviewQuery.isLoading}
            />
          </Card>
        </Col>
        <Col span={14}>
          <Card title="Recent batches" size="small">
            <Table<BatchListItem>
              size="small"
              rowKey="batch_id"
              columns={batchColumns}
              dataSource={overview?.recent_batches ?? []}
              pagination={false}
              loading={overviewQuery.isLoading}
            />
          </Card>
        </Col>
      </Row>

      <Row gutter={16}>
        <Col span={8}>
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
        <Col span={8}>
          <Card title="Migrations" size="small">
            <Table<Migration>
              size="small"
              rowKey="version"
              columns={migrationColumns}
              dataSource={overview?.migrations ?? []}
              pagination={false}
              loading={overviewQuery.isLoading}
            />
          </Card>
        </Col>
        <Col span={8}>
          <Card title="Boundaries" size="small">
            <Table<Boundary>
              size="small"
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
