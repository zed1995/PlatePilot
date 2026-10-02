import { useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Alert,
  Card,
  Collapse,
  Descriptions,
  Result,
  Space,
  Table,
  Tabs,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { Link, useParams } from 'react-router-dom'

import { adminApi } from '../api/client'
import type {
  DocumentSummary,
  RestaurantDetail as RestaurantDetailType,
  ReviewListItem,
  ReviewSummary,
} from '../api/types'
import JsonBlock from '../components/JsonBlock'
import KeySetTable from '../components/KeySetTable'
import PageHeader from '../components/PageHeader'
import StatusTag from '../components/StatusTag'
import { formatTime } from '../format'

const documentColumns: ColumnsType<DocumentSummary> = [
  {
    title: 'Document',
    dataIndex: 'document_id',
    width: 100,
    render: (documentId: number) => (
      <Link to={`/documents/${documentId}`}>{documentId}</Link>
    ),
  },
  { title: 'Scope', dataIndex: 'retrieval_scope', width: 100 },
  { title: 'Type', dataIndex: 'doc_type', ellipsis: true },
  { title: 'Version', dataIndex: 'version', width: 80 },
  {
    title: 'Active',
    dataIndex: 'is_active',
    width: 90,
    render: (active: boolean) => (
      <StatusTag tone={active ? 'green' : 'grey'}>{active ? 'active' : 'inactive'}</StatusTag>
    ),
  },
  {
    title: 'Embedding',
    dataIndex: 'has_embedding',
    width: 100,
    render: (has: boolean) => (
      <StatusTag tone={has ? 'blue' : 'grey'}>{has ? 'vector' : 'none'}</StatusTag>
    ),
  },
  {
    title: 'Snapshot',
    dataIndex: 'snapshot_at',
    render: formatTime,
  },
]

const reviewColumns: ColumnsType<ReviewListItem> = [
  { title: 'ID', dataIndex: 'review_id', width: 90 },
  { title: 'Rating', dataIndex: 'rating', width: 70 },
  {
    title: 'Reviewed',
    dataIndex: 'reviewed_at',
    width: 150,
    render: formatTime,
  },
  {
    title: 'Text',
    dataIndex: 'text',
    ellipsis: true,
    render: (text: string) => (
      <Typography.Text
        ellipsis={{ tooltip: text }}
        style={{ maxWidth: 460 }}
      >
        {text}
      </Typography.Text>
    ),
  },
  {
    title: 'Representative',
    dataIndex: 'is_representative',
    width: 120,
    render: (representative: boolean) =>
      representative ? (
        <StatusTag tone="orange">representative</StatusTag>
      ) : null,
  },
  {
    title: 'Topics',
    dataIndex: 'topic_tags',
    render: (tags?: string[]) =>
      tags?.length ? (
        <Typography.Text type="secondary">{tags.join(' · ')}</Typography.Text>
      ) : (
        '-'
      ),
  },
]

const summaryColumns: ColumnsType<ReviewSummary> = [
  { title: 'Topic', dataIndex: 'topic', width: 120 },
  {
    title: 'Sentiment',
    dataIndex: 'sentiment',
    width: 100,
    render: (sentiment: number) => sentiment.toFixed(2),
  },
  {
    title: 'Positive ratio',
    dataIndex: 'positive_ratio',
    width: 120,
    render: (ratio: number) => `${Math.round(ratio * 100)}%`,
  },
  { title: 'Evidence', dataIndex: 'evidence_count', width: 90 },
  { title: 'Summary', dataIndex: 'summary' },
]

function BasicInfo({ restaurant }: { restaurant: RestaurantDetailType }) {
  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Descriptions size="small" column={3}>
        <Descriptions.Item label="ID">{restaurant.restaurant_id}</Descriptions.Item>
        <Descriptions.Item label="Name">{restaurant.name}</Descriptions.Item>
        <Descriptions.Item label="Source">{restaurant.source}</Descriptions.Item>
        <Descriptions.Item label="Address" span={2}>
          {restaurant.address || '-'}
        </Descriptions.Item>
        <Descriptions.Item label="Borough">
          {restaurant.borough || '-'}
        </Descriptions.Item>
        <Descriptions.Item label="Cuisines" span={3}>
          {restaurant.cuisines?.length ? (
            <Typography.Text type="secondary">
              {restaurant.cuisines.join(' · ')}
            </Typography.Text>
          ) : (
            '-'
          )}
        </Descriptions.Item>
        <Descriptions.Item label="Price">
          {restaurant.price_level ?? '-'} ({restaurant.price_raw || 'n/a'})
        </Descriptions.Item>
        <Descriptions.Item label="Rating (computed)">
          {restaurant.rating_computed_avg ?? '-'}
        </Descriptions.Item>
        <Descriptions.Item label="Rating count">
          {restaurant.rating_count}
        </Descriptions.Item>
        <Descriptions.Item label="Stored reviews">
          {restaurant.stored_review_count}
        </Descriptions.Item>
        <Descriptions.Item label="Text reviews">
          {restaurant.text_review_count}
        </Descriptions.Item>
        <Descriptions.Item label="Representative reviews">
          {restaurant.representative_review_count}
        </Descriptions.Item>
        <Descriptions.Item label="Embedded reviews">
          {restaurant.embedded_review_count}
        </Descriptions.Item>
        <Descriptions.Item label="Last reviewed">
          {restaurant.last_reviewed_at ? formatTime(restaurant.last_reviewed_at) : '-'}
        </Descriptions.Item>
        <Descriptions.Item label="Snapshot status">
          {restaurant.snapshot_status}
        </Descriptions.Item>
      </Descriptions>

      <Collapse
        items={[
          {
            key: 'attributes',
            label: 'attributes',
            children: <JsonBlock value={restaurant.attributes} />,
          },
          {
            key: 'hours',
            label: 'hours',
            children: <JsonBlock value={restaurant.hours} />,
          },
        ]}
      />
    </Space>
  )
}

export default function RestaurantDetailPage() {
  const { id: idParam } = useParams()
  const id = Number(idParam)

  const restaurantQuery = useQuery<RestaurantDetailType>({
    queryKey: ['restaurant', id],
    queryFn: () => adminApi.restaurant(id),
    enabled: Number.isInteger(id),
  })
  const summariesQuery = useQuery<ReviewSummary[]>({
    queryKey: ['summaries', id],
    queryFn: () => adminApi.summaries(id),
    enabled: Number.isInteger(id),
  })
  const documentsQuery = useQuery<DocumentSummary[]>({
    queryKey: ['restaurant-documents', id],
    queryFn: () => adminApi.restaurantDocuments(id),
    enabled: Number.isInteger(id),
  })

  const fetchReviews = useCallback(
    (cursor?: string) => adminApi.reviews(id, { cursor, limit: 25 }),
    [id],
  )

  if (!Number.isInteger(id)) {
    return <Result status="warning" title="Bad restaurant id" />
  }

  if (restaurantQuery.isError) {
    return (
      <Alert
        type="error"
        showIcon
        message="Failed to load the restaurant"
        description={
          restaurantQuery.error instanceof Error
            ? restaurantQuery.error.message
            : String(restaurantQuery.error)
        }
      />
    )
  }

  const restaurant = restaurantQuery.data
  const subtitle = restaurant
    ? [restaurant.borough, restaurant.address].filter(Boolean).join(' · ')
    : undefined

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <PageHeader
        title={restaurant?.name ?? 'Restaurant'}
        description={subtitle}
        breadcrumb={[
          { title: 'Restaurants', path: '/restaurants' },
          { title: restaurant?.name ?? id },
        ]}
      />

      <Tabs
        items={[
          {
            key: 'basic',
            label: 'Basic information',
            children: restaurant ? (
              <BasicInfo restaurant={restaurant} />
            ) : (
              <Card loading style={{ minHeight: 200 }} />
            ),
          },
          {
            key: 'documents',
            label: `Knowledge documents (${documentsQuery.data?.length ?? 0})`,
            children: (
              <Table<DocumentSummary>
                size="middle"
                tableLayout="fixed"
                rowKey="document_id"
                columns={documentColumns}
                dataSource={documentsQuery.data ?? []}
                pagination={false}
                loading={documentsQuery.isLoading}
              />
            ),
          },
          {
            key: 'reviews',
            label: 'Reviews',
            children: (
              <KeySetTable<ReviewListItem>
                columns={reviewColumns}
                rowKey={(row) => row.review_id}
                fetchPage={fetchReviews}
                resetKey={String(id)}
              />
            ),
          },
          {
            key: 'summaries',
            label: `Topic summaries (${summariesQuery.data?.length ?? 0})`,
            children: (
              <Table<ReviewSummary>
                size="middle"
                rowKey={(row) => `${row.topic}-${row.generated_at}`}
                columns={summaryColumns}
                dataSource={summariesQuery.data ?? []}
                pagination={false}
                loading={summariesQuery.isLoading}
              />
            ),
          },
        ]}
      />
    </Space>
  )
}
