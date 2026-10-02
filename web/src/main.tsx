import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ConfigProvider } from 'antd'
import { BrowserRouter } from 'react-router-dom'

import App from './App'
import { consoleTheme } from './theme'
import './styles.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Administrative views refresh on demand; focus-driven refetches make
      // paged tables jump while the operator reads them.
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
})

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <ConfigProvider theme={consoleTheme}>
          <App />
        </ConfigProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
)
