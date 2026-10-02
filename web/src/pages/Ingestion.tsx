import { useCallback, useState } from 'react'
import { Button, Card, Form, Input, Space, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { Link } from 'react-router-dom'

import { adminApi, type BatchesParams } from '../api/client'
import type { BatchListItem } from '../api/types'
import KeySetTable from '../components/KeySetTable'
import PageHeader from '../components/PageHeader'
import StatusTag from '../components/StatusTag'
import { formatDuration, formatTime, statusColor } from '../format'

interface FilterValues {
  stage?: string
}

const columns = (): ColumnsType<BatchListItem> => [
  {
    title: 'Batch',
    dataIndex: 'batch_id',
    width: 80,
    render: (id: number) => <Link to={`/ingestion/${id}`}>{id}</Link>,
  },
  { title: 'Stage', dataIndex: 'stage', width: 90 },
  {
    title: 'Status',
    dataIndex: 'status',
    width: 100,
    render: (status: string) => (
      <StatusTag tone={statusColor(status)}>{status}</StatusTag>
    ),
  },
  {
    title: 'Started',
    dataIndex: 'started_at',
    width: 180,
    render: formatTime,
  },
  {
    title: 'Duration',
    dataIndex: 'duration_ms',
    width: 100,
    render: formatDuration,
  },
  { title: 'Rows read', dataIndex: 'rows_read', width: 100 },
  { title: 'Accepted', dataIndex: 'accepted', width: 90 },
  { title: 'Written', dataIndex: 'written', width: 90 },
  {
    title: 'Rejected',
    dataIndex: 'rejected',
    width: 90,
    render: (rejected: number) =>
      rejected > 0 ? (
        <Typography.Text style={{ color: '#ff3b30' }}>{rejected}</Typography.Text>
      ) : (
        rejected
      ),
  },
  {
    title: 'Docs built',
    dataIndex: 'documents_built',
    width: 100,
    render: (value?: number) => value ?? '-',
  },
  {
    title: 'Docs embedded',
    dataIndex: 'documents_embedded',
    width: 120,
    render: (value?: number) => value ?? '-',
  },
]

export default function Ingestion() {
  const [form] = Form.useForm<FilterValues>()
  const [applied, setApplied] = useState<FilterValues>({})

  const fetchPage = useCallback(
    (cursor?: string) => {
      const params: BatchesParams = {
        cursor,
        limit: 25,
        stage: applied.stage || undefined,
      }
      return adminApi.batches(params)
    },
    [applied],
  )

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <PageHeader
        title="Ingestion"
        description="Batches that loaded, curated, and embedded source data."
      />

      <Card size="small">
        <Form<FilterValues>
          form={form}
          layout="inline"
          onFinish={(values) => setApplied(values)}
        >
          <Form.Item label="Stage" name="stage">
            <Input placeholder="m2" style={{ width: 120 }} />
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

      <KeySetTable<BatchListItem>
        columns={columns()}
        rowKey={(row) => row.batch_id}
        fetchPage={fetchPage}
        resetKey={JSON.stringify(applied)}
      />
    </Space>
  )
}
