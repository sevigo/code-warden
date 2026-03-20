import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        proxyTimeout: 600000,  // 10 min — LLM calls can be slow
        timeout: 600000,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
  },
})