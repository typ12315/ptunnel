import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 前端产物直接输出到 Go 的 embed 目录
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../internal/server/web/dist',
    emptyOutDir: true
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'https://127.0.0.1:9443', changeOrigin: true, secure: false }
    }
  }
})