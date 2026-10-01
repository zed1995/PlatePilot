import { Alert, Descriptions, Space, Table } from 'antd'
import type { ColumnsType } from 'antd/es/table'

import type {
  CandidateScore,
  ChannelScore,
  ChannelSummary,
  Trace,
} from '../api/types'

const channelColumns: ColumnsType<ChannelSummary> = [
  { title: 'Channel', dataIndex: 'channel' },
  {
    title: 'Ran',
    dataIndex: 'ran',
    render: (ran: boolean) => (ran ? 'yes' : 'no'),
  },
  { title: 'Weight', dataIndex: 'weight' },
  { title: 'Results', dataIndex: 'results' },
  { title: 'Note', dataIndex: 'note' },
]

const channelScoreColumns: ColumnsType<ChannelScore> = [
  { title: 'Channel', dataIndex: 'channel' },
  { title: 'Raw', dataIndex: 'raw_score' },
  { title: 'Weight', dataIndex: 'weight' },
  { title: 'Normalized', dataIndex: 'normalized_score' },
  { title: 'Contribution', dataIndex: 'contribution' },
  { title: 'Reason', dataIndex: 'reason' },
]

const candidateColumns: ColumnsType<CandidateScore> = [
  { title: 'Restaurant ID', dataIndex: 'restaurant_id' },
  { title: 'Total', dataIndex: 'total' },
]

// TracePanel breaks one retrieval trace apart: degradation warnings, the run
// summary, per-channel activity, and each candidate's channel-level scoring.
export default function TracePanel({ trace }: { trace: Trace }) {
  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      {trace.warnings && trace.warnings.length > 0 && (
        <Alert
          type="warning"
          showIcon
          message="Warnings"
          description={
            <ul style={{ margin: 0, paddingLeft: 20 }}>
              {trace.warnings.map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
          }
        />
      )}

      <Descriptions size="small" bordered column={3}>
        <Descriptions.Item label="Candidate pool">
          {trace.candidate_pool}
        </Descriptions.Item>
        <Descriptions.Item label="Returned">{trace.returned}</Descriptions.Item>
        <Descriptions.Item label="Top K">{trace.top_k}</Descriptions.Item>
        <Descriptions.Item label="Embedding model">
          {trace.embedding_model_id ?? '-'}
        </Descriptions.Item>
        <Descriptions.Item label="Query dim">
          {trace.query_embedding_dim ?? '-'}
        </Descriptions.Item>
        <Descriptions.Item label="Rerank">
          {trace.rerank_applied
            ? `applied (${trace.rerank_model_id})`
            : trace.rerank_model_id
              ? `failed (${trace.rerank_model_id})`
              : 'off'}
        </Descriptions.Item>
      </Descriptions>

      <Table<ChannelSummary>
        size="small"
        rowKey="channel"
        columns={channelColumns}
        dataSource={trace.channels ?? []}
        pagination={false}
      />

      <Table<CandidateScore>
        size="small"
        rowKey="restaurant_id"
        columns={candidateColumns}
        dataSource={trace.candidates ?? []}
        pagination={false}
        expandable={{
          expandedRowRender: (candidate) => (
            <Table<ChannelScore>
              size="small"
              rowKey="channel"
              columns={channelScoreColumns}
              dataSource={candidate.channels ?? []}
              pagination={false}
            />
          ),
        }}
      />
    </Space>
  )
}
