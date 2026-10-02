import { Breadcrumb, Space, Typography } from 'antd'
import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'

interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  extra?: ReactNode
  breadcrumb?: { title: ReactNode; path?: string }[]
}

// PageHeader gives every page the same open title block: an optional
// breadcrumb trail, a large title, a muted description, and optional
// right-side actions.
export default function PageHeader({
  title,
  description,
  extra,
  breadcrumb,
}: PageHeaderProps) {
  const breadcrumbItems = breadcrumb?.map((crumb, index) =>
    crumb.path
      ? { key: index, title: <Link to={crumb.path}>{crumb.title}</Link> }
      : { key: index, title: crumb.title },
  )

  return (
    <Space direction="vertical" size={4} style={{ display: 'flex', marginBottom: 24 }}>
      {breadcrumbItems && breadcrumbItems.length > 0 && (
        <Breadcrumb items={breadcrumbItems} />
      )}
      <Space
        align="center"
        style={{ display: 'flex', width: '100%', justifyContent: 'space-between' }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          {title}
        </Typography.Title>
        {extra && <Space align="center">{extra}</Space>}
      </Space>
      {description && (
        <Typography.Text type="secondary">{description}</Typography.Text>
      )}
    </Space>
  )
}
