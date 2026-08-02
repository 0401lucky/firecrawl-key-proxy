import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物必须落在 internal/webui/dist（go:embed 只能引用包目录树内的文件）。
// emptyOutDir 必须开启，否则旧产物残留会让 index.html 引用的哈希文件名对不上。
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    // 开发期把 /api 转发到本地 Go 服务，避免跨域。
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
})
