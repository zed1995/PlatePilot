import { useCallback, useState } from 'react'
import {
  Button,
  Card,
  Form,
  Input,
  Select,
  Space,
  Switch,
  Tag,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { Link } from 'react-router-dom'

import { adminApi, type RestaurantsParams } from '../api/client'
import type { RestaurantListItem } from '../api/types'
import KeySetTable from '../components/KeySetTable'
import { formatTime } from '../format'

interface FilterValues {
  borough?: string
  cuisine?: string
  active?: boolean
  q?: string
}

const columns = (): ColumnsType<RestaurantListItem> => [
  {
    title: 'ID',
    dataIndex: 'restaurant_id',
    width: 90,
    render: (id: number) => <Link to={`/restaurants/${id}`}>{id}</Link>,
  },
  {
    title: 'Name',
    dataIndex: 'name',
    render: (name: string, row) => (
      <Space direction="vertical" size={0}>
        <Link to={`/restaurants/${row.restaurant_id}`}>{name}</Link>
        <span style={{ color: '#999', fontSize: 12 }}>{row.address}</span>
      </Space>
    ),
  },
  { title: 'Borough', dataIndex: 'borough', width: 110 },
  {
    title: 'Cuisines',
    dataIndex: 'cuisines',
    render: (cuisines: string[]) => (
      <Space size={[0, 4]} wrap>
        {cuisines?.map((cuisine) => <Tag key={cuisine}>{cuisine}</Tag>)}
      </Space>
    ),
  },
  {
    title: 'Price',
    dataIndex: 'price_level',
    width: 70,
    render: (price?: number) => price ?? '-',
  },
  {
    title: 'Rating',
    dataIndex: 'rating_computed_avg',
    width: 110,
    render: (rating: number | undefined, row) => (
      <Space direction="vertical" size={0}>
        <span>{rating ?? '-'}</span>
        <span style={{ color: '#999', fontSize: 12 }}>{row.rating_count} reviews</span>
      </Space>
    ),
  },
  {
    title: 'Active',
    dataIndex: 'is_active_for_demo',
    width: 80,
    render: (active: boolean) =>
      active ? <Tag color="green">active</Tag> : <Tag>inactive</Tag>,
  },
  {
    title: 'Observed',
    dataIndex: 'observed_at',
    width: 170,
    render: formatTime,
  },
]

const boroughOptions = [
  { value: 'manhattan', label: 'Manhattan' },
  { value: 'brooklyn', label: 'Brooklyn' },
  { value: 'queens', label: 'Queens' },
  { value: 'bronx', label: 'Bronx' },
  { value: 'staten island', label: 'Staten Island' },
]

export default function Restaurants() {
  const [form] = Form.useForm<FilterValues>()
  const [applied, setApplied] = useState<FilterValues>({})

  const fetchPage = useCallback(
    (cursor?: string) => {
      const params: RestaurantsParams = {
        cursor,
        limit: 25,
        borough: applied.borough || undefined,
        cuisine: applied.cuisine || undefined,
        active: applied.active,
        q: applied.q || undefined,
      }
      return adminApi.restaurants(params)
    },
    [applied],
  )

  return (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      <Card size="small">
        <Form<FilterValues>
          form={form}
          layout="inline"
          onFinish={(values) => setApplied(values)}
        >
          <Form.Item label="Borough" name="borough">
            <Select
              options={boroughOptions}
              allowClear
              style={{ width: 150 }}
              placeholder="any"
            />
          </Form.Item>
          <Form.Item label="Cuisine" name="cuisine">
            <Input placeholder="pizza" style={{ width: 120 }} />
          </Form.Item>
          <Form.Item label="Name contains" name="q">
            <Input placeholder="Joe" style={{ width: 140 }} />
          </Form.Item>
          <Form.Item label="Active only" name="active" valuePropName="checked">
            <Switch />
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

      <KeySetTable<RestaurantListItem>
        columns={columns()}
        rowKey={(row) => row.restaurant_id}
        fetchPage={fetchPage}
        resetKey={JSON.stringify(applied)}
      />
    </Space>
  )
}
