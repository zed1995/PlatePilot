import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Alert,
  Card,
  Collapse,
  Descriptions,
  List,
  Result,
  Space,
  Switch,
  Tag,
  Typography,
} from 'antd'
import { Link, useParams } from 'react-router-dom'

import { adminApi } from '../api/client'
import type { DocumentDetail as DocumentDetailType } from '../api/types'
import JsonBlock from '../components/JsonBlock'
import { formatTime, shortHash } from '../format'

export default function DocumentDetailPage() {
  const { id: idParam } = useParams()
  const id = Number(idParam)
  const [vectorPreview, setVectorPreview] = useState(false)

  const documentQuery = useQuery<DocumentDetailType>({
    queryKey: ['document', id, vectorPreview],
    queryFn: () => adminApi.document(id, vectorPreview),
    enabled: Number.isInteger(id),
  })

  if (!Number.isInteger(id)) {
    return <Result status="warning" title="Bad document id" />
  }

  if (documentQuery.isError) {
    return (
      <Alert
        type="error"
        showIcon
        message="Failed to load the document"
        description={
          documentQuery.error instanceof Error
            ? documentQuery.error.message
            : String(documentQuery.error)
        }
      />
    )
  }

  const document = documentQuery.data

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Card size="small" loading={documentQuery.isLoading}>
        <Space style={{ justifyContent: 'space-between', width: '100%' }}>
          <Typography.Title level={4} style={{ margin: 0 }}>
            {document?.title || document?.doc_type}
          </Typography.Title>
          <Space>
            <Switch
              checked={vectorPreview}
              onChange={setVectorPreview}
              id="vector-preview-switch"
            />
            <span>Load vector preview</span>
          </Space>
        </Space>
      </Card>

      {document && (
        <Descriptions bordered size="small" column={3}>
          <Descriptions.Item label="Document ID">
            {document.document_id}
          </Descriptions.Item>
          <Descriptions.Item label="Restaurant">
            <Link to={`/restaurants/${document.restaurant_id}`}>
              {document.restaurant_id}
            </Link>
          </Descriptions.Item>
          <Descriptions.Item label="Scope">
            {document.retrieval_scope}
          </Descriptions.Item>
          <Descriptions.Item label="Doc type">
            {document.doc_type}
          </Descriptions.Item>
          <Descriptions.Item label="Version">
            {document.version}
          </Descriptions.Item>
          <Descriptions.Item label="Content hash">
            {shortHash(document.content_hash, 20)}
          </Descriptions.Item>
          <Descriptions.Item label="Is active">
            {document.is_active ? (
              <Tag color="green">active</Tag>
            ) : (
              <Tag>inactive</Tag>
            )}
          </Descriptions.Item>
          <Descriptions.Item label="Embedding model">
            {document.embedding_model || '-'}
          </Descriptions.Item>
          <Descriptions.Item label="Dimensions">
            {document.embedding_dimensions || '-'}
          </Descriptions.Item>
          <Descriptions.Item label="Snapshot" span={3}>
            {formatTime(document.snapshot_at)}
          </Descriptions.Item>
        </Descriptions>
      )}

      <Card title="Content" size="small" loading={documentQuery.isLoading}>
        <pre
          style={{
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
            margin: 0,
            fontFamily:
              'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
          }}
        >
          {document?.content}
        </pre>
      </Card>

      <RowPanels document={document} vectorPreview={vectorPreview} />
    </Space>
  )
}

function RowPanels({
  document,
  vectorPreview,
}: {
  document?: DocumentDetailType
  vectorPreview: boolean
}) {
  if (!document) return null

  const items = [
    {
      key: 'metadata',
      label: 'metadata',
      children: <JsonBlock value={document.metadata} />,
    },
    {
      key: 'source-record-ids',
      label: `source_record_ids (${document.source_record_ids?.length ?? 0})`,
      children: (
        <List
          size="small"
          dataSource={document.source_record_ids ?? []}
          renderItem={(recordId) => <List.Item>{recordId}</List.Item>}
        />
      ),
    },
  ]

  if (vectorPreview) {
    items.push({
      key: 'vector-preview',
      label: `vector_preview (${document.vector_preview?.length ?? 0} values)`,
      children: <JsonBlock value={document.vector_preview} />,
    })
  }

  return <Collapse items={items} />
}
