import { useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Col,
  Collapse,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Select,
  Space,
  Table,
  Tag,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { Link } from 'react-router-dom'

import { adminApi } from '../api/client'
import type {
  Evidence,
  RestaurantCandidate,
  SearchRequest,
  SearchResponse,
} from '../api/types'
import TracePanel from '../components/TracePanel'

interface DebugFormValues {
  query?: string
  text?: string
  borough?: string
  cuisine?: string
  price?: number
  minRating?: number
  topK?: number
}

const boroughOptions = [
  { value: 'manhattan', label: 'Manhattan' },
  { value: 'brooklyn', label: 'Brooklyn' },
  { value: 'queens', label: 'Queens' },
  { value: 'bronx', label: 'Bronx' },
  { value: 'staten island', label: 'Staten Island' },
]

const candidateColumns = (
  onEvidence: (candidate: RestaurantCandidate) => void,
): ColumnsType<RestaurantCandidate> => [
  {
    title: 'Restaurant',
    dataIndex: 'name',
    render: (_, candidate) => (
      <Space direction="vertical" size={0}>
        <Link to={`/restaurants/${candidate.restaurant_id}`}>{candidate.name}</Link>
        <span style={{ color: '#999', fontSize: 12 }}>{candidate.address}</span>
      </Space>
    ),
  },
  { title: 'Score', dataIndex: 'score', width: 90 },
  {
    title: 'Rating',
    dataIndex: 'rating',
    width: 90,
    render: (rating?: number) => rating ?? '-',
  },
  {
    title: 'Reasons',
    dataIndex: 'reasons',
    render: (reasons?: string[]) => (
      <Space size={[0, 4]} wrap>
        {reasons?.map((reason) => <Tag key={reason}>{reason}</Tag>)}
      </Space>
    ),
  },
  {
    title: '',
    width: 130,
    render: (_, candidate) => (
      <Button size="small" onClick={() => onEvidence(candidate)}>
        Evidence
      </Button>
    ),
  },
]

interface EvidenceState {
  restaurantName: string
  loading: boolean
  evidence: Evidence[]
  error?: string
}

export default function RetrievalDebug() {
  const [form] = Form.useForm<DebugFormValues>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>()
  const [result, setResult] = useState<SearchResponse>()
  const [evidenceState, setEvidenceState] = useState<EvidenceState>()

  const runSearch = async (values: DebugFormValues) => {
    const request: SearchRequest = {
      query: values.query?.trim() || undefined,
      text: values.text?.trim() || undefined,
      top_k: values.topK || undefined,
      filter:
        values.borough || values.cuisine || values.price || values.minRating
          ? {
              borough: values.borough || undefined,
              cuisines: values.cuisine ? [values.cuisine] : undefined,
              price_levels: values.price ? [values.price] : undefined,
              min_rating: values.minRating || undefined,
            }
          : undefined,
    }

    setLoading(true)
    setError(undefined)
    setResult(undefined)
    try {
      setResult(await adminApi.debugSearch(request))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setLoading(false)
    }
  }

  const loadEvidence = async (candidate: RestaurantCandidate) => {
    setEvidenceState({ restaurantName: candidate.name, loading: true, evidence: [] })
    try {
      const bundle = await adminApi.debugEvidence({
        restaurant_ids: [candidate.restaurant_id],
      })
      setEvidenceState({
        restaurantName: candidate.name,
        loading: false,
        evidence: bundle.evidence,
      })
    } catch (cause) {
      setEvidenceState({
        restaurantName: candidate.name,
        loading: false,
        evidence: [],
        error: cause instanceof Error ? cause.message : String(cause),
      })
    }
  }

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Row gutter={16}>
        <Col span={8}>
          <Card title="Request" size="small">
            <Form<DebugFormValues>
              form={form}
              layout="vertical"
              onFinish={runSearch}
              initialValues={{ topK: 5 }}
            >
              <Form.Item
                label="Query — a whole natural-language question"
                name="query"
              >
                <Input placeholder="where should I go for a date?" />
              </Form.Item>
              <Form.Item
                label="Text — a restaurant name or address fragment"
                name="text"
              >
                <Input placeholder="Joe's Pizza, Carmine St" />
              </Form.Item>
              <Form.Item label="Borough" name="borough">
                <Select options={boroughOptions} allowClear />
              </Form.Item>
              <Form.Item label="Cuisine" name="cuisine">
                <Input placeholder="italian" />
              </Form.Item>
              <Space>
                <Form.Item label="Price level" name="price">
                  <InputNumber min={1} max={4} style={{ width: 110 }} />
                </Form.Item>
                <Form.Item label="Min rating" name="minRating">
                  <InputNumber min={0} max={5} step={0.1} style={{ width: 110 }} />
                </Form.Item>
                <Form.Item label="Top K" name="topK">
                  <InputNumber min={1} max={50} style={{ width: 100 }} />
                </Form.Item>
              </Space>
              <Button type="primary" htmlType="submit" loading={loading} block>
                Run retrieval
              </Button>
              <p style={{ marginTop: 8, color: '#999', fontSize: 12 }}>
                Channels and weights come from the service configuration and
                cannot be changed here.
              </p>
            </Form>
          </Card>
        </Col>

        <Col span={16}>
          {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }} />}
          {!result && !error && !loading && (
            <Empty description="Submit a request to inspect the serving retrieval path" />
          )}
          {result && (
            <Collapse
              defaultActiveKey={['candidates', 'trace']}
              items={[
                {
                  key: 'candidates',
                  label: `Candidates (${result.candidates.length})`,
                  children: (
                    <Table<RestaurantCandidate>
                      size="small"
                      rowKey="restaurant_id"
                      columns={candidateColumns(loadEvidence)}
                      dataSource={result.candidates}
                      pagination={false}
                    />
                  ),
                },
                {
                  key: 'trace',
                  label: 'Trace — channels, fusion scores, warnings',
                  children: result.trace ? (
                    <TracePanel trace={result.trace} />
                  ) : (
                    <Empty description="no trace returned" />
                  ),
                },
              ]}
            />
          )}
        </Col>
      </Row>

      <Modal
        open={Boolean(evidenceState)}
        title={`Evidence — ${evidenceState?.restaurantName ?? ''}`}
        width={800}
        footer={null}
        onCancel={() => setEvidenceState(undefined)}
      >
        {evidenceState?.error && (
          <Alert type="error" showIcon message={evidenceState.error} />
        )}
        {evidenceState?.loading ? (
          <p>Loading…</p>
        ) : (
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            {evidenceState?.evidence.map((item) => (
              <Card
                key={item.evidence_id}
                size="small"
                title={
                  <Space wrap>
                    <Tag>{item.doc_type}</Tag>
                    {item.title}
                  </Space>
                }
              >
                <p style={{ whiteSpace: 'pre-wrap' }}>{item.content}</p>
                <Space wrap>
                  {item.source_record_ids?.map((id) => (
                    <Tag key={id}>{id}</Tag>
                  ))}
                </Space>
              </Card>
            ))}
            {evidenceState?.evidence.length === 0 && !evidenceState.loading && (
              <Empty description="no evidence returned" />
            )}
          </Space>
        )}
      </Modal>
    </Space>
  )
}
