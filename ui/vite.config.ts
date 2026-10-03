import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: { outDir: '../web/dist', emptyOutDir: true, chunkSizeWarningLimit: 900 },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/v1': 'http://localhost:8080',
      '/auth': 'http://localhost:8080',
      '/.well-known': 'http://localhost:8080',
    },
  },
})
