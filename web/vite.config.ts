/// <reference types="vitest" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The console talks to chat-service. In development leave VITE_API_BASE
// unset and let this proxy carry /admin and /v1 to the local service.
const backendTarget = process.env.VITE_BACKEND_TARGET ?? 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/admin': backendTarget,
      '/v1': backendTarget,
    },
  },
  build: {
    // AntD alone exceeds Vite's default 500 kB warning; the console is a
    // desktop-only internal tool where that size is acceptable.
    chunkSizeWarningLimit: 1000,
    rollupOptions: {
      output: {
        // Split the big UI libraries into vendor chunks that cache
        // independently of the application code.
        manualChunks: {
          react: ['react', 'react-dom', 'react-router-dom'],
          antd: ['antd', '@ant-design/icons'],
          query: ['@tanstack/react-query'],
        },
      },
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: './vitest.setup.ts',
  },
})
