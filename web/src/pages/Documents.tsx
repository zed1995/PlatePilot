import { useCallback, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Select,
  Space,
  Tag,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { Link } from 'react-router-dom'

import { adminApi, type DocumentsParams } from '../api/client'
import type { DocumentListItem } from '../api/types'
import KeySetTable from '../components/KeySetTable'
import { formatTime, shortHash } from '../format'

interface FilterValues {
  scope?: string
  doc_type?: string
  is_active?: 'true' | 'false' | undefined
  has_embedding?: 'true' | 'false' | undefined
  restaurant_id?: string
}

const triStateOptions = [
  { value: '', label: 'Any' },
  { value: 'true', label: 'Yes' },
  { value: 'false', label: 'No' },
]

const scopeOptions = [
  { value: 'restaurant', label: 'restaurant' },
  { value: 'query', label: 'query' },
]

const docTypeOptions = [
  { value: 'restaurant_profile', label: 'restaurant_profile' },
  { value: 'restaurant_attributes', label: 'restaurant_attributes' },
  { value: 'restaurant_hours', label: 'restaurant_hours' },
  { value: 'restaurant_review_summary', label: 'restaurant_review_summary' },
  { value: 'restaurant_representative_reviews', label: 'restaurant_representative_reviews' },
]

const columns = (): ColumnsType<DocumentListItem> => [
  {
    title: 'Document',
    dataIndex: 'document_id',
    width: 100,
    render: (id: number) => <Link to={`/documents/${id}`}>{id}</Link>,
  },
  {
    title: 'Restaurant',
    dataIndex: 'restaurant_id',
    width: 100,
    render: (id: number) => <Link to={`/restaurants/${id}`}>{id}</Link>,
  },
  { title: 'Scope', dataIndex: 'retrieval_scope', width: 100 },
  { title: 'Type', dataIndex: 'doc_type' },
  { title: 'Version', dataIndex: 'version', width: 80 },
  {
    title: 'Hash',
    dataIndex: 'content_hash',
    width: 150,
    render: (hash: string) => shortHash(hash),
  },
  {
    title: 'Active',
    dataIndex: 'is_active',
    width: 90,
    render: (active: boolean) =>
      active ? <Tag color="green">active</Tag> : <Tag>inactive</Tag>,
  },
  {
    title: 'Embedding',
    dataIndex: 'has_embedding',
    width: 100,
    render: (has: boolean) =>
      has ? <Tag color="blue">vector</Tag> : <Tag>none</Tag>,
  },
  {
    title: 'Snapshot',
    dataIndex: 'snapshot_at',
    render: formatTime,
  },
]

export default function Documents() {
  const [form] = Form.useForm<FilterValues>()
  const [applied, setApplied] = useState<FilterValues>({})

  const fetchPage = useCallback(
    (cursor?: string) => {
      const params: DocumentsParams = {
        cursor,
        limit: 25,
        scope: applied.scope || undefined,
        doc_type: applied.doc_type || undefined,
        is_active:
          applied.is_active === undefined ? undefined : applied.is_active === 'true',
        has_embedding:
          applied.has_embedding === undefined
            ? undefined
            : applied.has_embedding === 'true',
        restaurant_id: applied.restaurant_id
          ? Number(applied.restaurant_id)
          : undefined,
      }
      return adminApi.documents(params)
    },
    [applied],
  )

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Alert
        type="info"
        showIcon
        message="is_active is not filtered by default: superseded versions are listed too. Inactive rows are shown grayed out."
      />

      <Card size="small">
        <Form<FilterValues>
          form={form}
          layout="inline"
          onFinish={(values) => setApplied(values)}
        >
          <Form.Item label="Scope" name="scope">
            <Select
              options={scopeOptions}
              allowClear
              style={{ width: 130 }}
              placeholder="any"
            />
          </Form.Item>
          <Form.Item label="Doc type" name="doc_type">
            <Select
              options={docTypeOptions}
              allowClear
              style={{ width: 240 }}
              placeholder="any"
            />
          </Form.Item>
          <Form.Item label="Is active" name="is_active">
            <Select
              options={triStateOptions}
              style={{ width: 90 }}
            />
          </Form.Item>
          <Form.Item label="Has embedding" name="has_embedding">
            <Select
              options={triStateOptions}
              style={{ width: 90 }}
            />
          </Form.Item>
          <Form.Item label="Restaurant ID" name="restaurant_id">
            <Input style={{ width: 110 }} />
          </Form.Item>
          <Form.Item>
            <Space>
              <Button type="primary" htmlType="submit">
                Apply
              </Button>
              <Button
                onClick={() => {
                  form.resetFields()
                  setApplied({})
                }}
              >
                Reset
              </Button>
            </Space>
          </Form.Item>
        </Form>
      </Card>

      <KeySetTable<DocumentListItem>
        columns={columns()}
        rowKey={(row) => row.document_id}
        fetchPage={fetchPage}
        resetKey={JSON.stringify(applied)}
        rowClassName={(row) => (row.is_active ? '' : 'inactive-row')}
      />
    </Space>
  )
}
