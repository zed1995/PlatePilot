import { useQuery } from '@tanstack/react-query'
import {
  Alert,
  Card,
  Collapse,
  Descriptions,
  Progress,
  Result,
  Space,
  Table,
  Typography,
} from 'antd'
import { useParams } from 'react-router-dom'
import type { ColumnsType } from 'antd/es/table'

import { adminApi } from '../api/client'
import type { BatchDetail, RejectionItem } from '../api/types'
import JsonBlock from '../components/JsonBlock'
import PageHeader from '../components/PageHeader'
import StatusTag from '../components/StatusTag'
import {
  formatDuration,
  formatTime,
  shortHash,
  statusColor,
} from '../format'

const rejectionColumns: ColumnsType<RejectionItem> = [
  { title: 'Stage', dataIndex: 'stage', width: 100 },
  { title: 'Line', dataIndex: 'line_no', width: 80 },
  { title: 'Reason', dataIndex: 'reason' },
  { title: 'Source record', dataIndex: 'source_record_id' },
]

function RejectReasons({ reasons }: { reasons?: Record<string, number> }) {
  const entries = Object.entries(reasons ?? {})
  if (entries.length === 0) {
    return <Typography.Text type="secondary">No rejections.</Typography.Text>
  }

  const max = Math.max(...entries.map(([, count]) => count))

  return (
    <Space direction="vertical" size={8} style={{ width: '100%' }}>
      {entries.map(([reason, count]) => (
        <Space key={reason} style={{ width: '100%' }} align="center">
          <Typography.Text style={{ width: 260 }}>{reason}</Typography.Text>
          <Progress
            percent={Math.round((count / max) * 100)}
            size="small"
            style={{ width: 260, marginBottom: 0 }}
            format={() => String(count)}
          />
        </Space>
      ))}
    </Space>
  )
}

export default function IngestionDetailPage() {
  const { id: idParam } = useParams()
  const id = Number(idParam)

  const batchQuery = useQuery<BatchDetail>({
    queryKey: ['batch', id],
    queryFn: () => adminApi.batch(id),
    enabled: Number.isInteger(id),
  })

  if (!Number.isInteger(id)) {
    return <Result status="warning" title="Bad batch id" />
  }

  if (batchQuery.isError) {
    return (
      <Alert
        type="error"
        showIcon
        message="Failed to load the batch"
        description={
          batchQuery.error instanceof Error
            ? batchQuery.error.message
            : String(batchQuery.error)
        }
      />
    )
  }

  const batch = batchQuery.data

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <PageHeader
        title={`Batch ${id}`}
        breadcrumb={[
          { title: 'Ingestion', path: '/ingestion' },
          { title: `Batch ${id}` },
        ]}
        extra={
          batch ? (
            <StatusTag tone={statusColor(batch.status)}>{batch.status}</StatusTag>
          ) : undefined
        }
      />

      {batch && (
        <Card size="small">
          <Descriptions size="small" column={4}>
            <Descriptions.Item label="Stage">{batch.stage}</Descriptions.Item>
            <Descriptions.Item label="Curation version">
              {batch.curation_version}
            </Descriptions.Item>
            <Descriptions.Item label="Started">
              {formatTime(batch.started_at)}
            </Descriptions.Item>
            <Descriptions.Item label="Finished">
              {formatTime(batch.finished_at)}
            </Descriptions.Item>
            <Descriptions.Item label="Duration">
              {formatDuration(batch.duration_ms)}
            </Descriptions.Item>
            <Descriptions.Item label="Source file" span={2}>
              {batch.source_file || '-'}
            </Descriptions.Item>
            <Descriptions.Item label="Source sha256">
              {batch.source_sha256 ? shortHash(batch.source_sha256, 16) : '-'}
            </Descriptions.Item>

            <Descriptions.Item label="Rows read">{batch.rows_read}</Descriptions.Item>
            <Descriptions.Item label="Accepted">{batch.accepted}</Descriptions.Item>
            <Descriptions.Item label="Written">{batch.written}</Descriptions.Item>
            <Descriptions.Item label="Deduped">{batch.deduped}</Descriptions.Item>
            <Descriptions.Item label="Filtered">{batch.filtered}</Descriptions.Item>
            <Descriptions.Item label="Rejected">{batch.rejected}</Descriptions.Item>
            <Descriptions.Item label="Unmatched">{batch.unmatched}</Descriptions.Item>
            <Descriptions.Item label="Documents built">
              {batch.documents_built ?? '-'}
            </Descriptions.Item>
            <Descriptions.Item label="Documents embedded">
              {batch.documents_embedded ?? '-'}
            </Descriptions.Item>
            <Descriptions.Item label="Documents rejected">
              {batch.documents_rejected ?? '-'}
            </Descriptions.Item>
            <Descriptions.Item label="Embedding model">
              {batch.embedding_model || '-'}
            </Descriptions.Item>
            <Descriptions.Item label="Dimensions">
              {batch.embedding_dimensions || '-'}
            </Descriptions.Item>
            <Descriptions.Item label="Error code">
              {batch.error_code || '-'}
            </Descriptions.Item>
          </Descriptions>
        </Card>
      )}

      <Collapse
        items={[
          {
            key: 'missing-fields',
            label: 'missing_fields',
            children: <JsonBlock value={batch?.missing_fields} />,
          },
        ]}
      />

      <Card title="Reject reasons" size="small" loading={batchQuery.isLoading}>
        <RejectReasons reasons={batch?.reject_reasons} />
      </Card>

      <Card
        title={`Rejection details (${batch?.rejections?.length ?? 0})`}
        size="small"
        loading={batchQuery.isLoading}
      >
        {batch?.rejections_truncated && (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 12 }}
            message="The rejection list is truncated: only the first entries kept by the service are shown."
          />
        )}
        <Table<RejectionItem>
          size="middle"
          rowKey={(row) => `${row.stage}-${row.line_no}-${row.reason}`}
          columns={rejectionColumns}
          dataSource={batch?.rejections ?? []}
          pagination={false}
        />
      </Card>
    </Space>
  )
}
