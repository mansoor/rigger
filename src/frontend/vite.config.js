import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        ws: true,  // proxy WebSocket connections too
      },
    },
  },
  build: {
    outDir: '../backend/cmd/server/dist',
    emptyOutDir: true,
  },
})
