import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import { resolve } from 'path'

export default defineConfig({
  plugins: [
    vue(),
    AutoImport({
      resolvers: [ElementPlusResolver()],
      dts: resolve(__dirname, 'src/auto-imports.d.ts'),
    }),
    Components({
      resolvers: [ElementPlusResolver()],
      dts: resolve(__dirname, 'src/components.d.ts'),
    }),
  ],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
    },
  },
  server: {
    proxy: {
      '/api/v1': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        // 浏览器 Origin 是 localhost:5173；后端 CSRF 要求它与代理目标一致。
        configure: (proxy) => {
          proxy.on('proxyReq', (request) => {
            request.setHeader('Origin', 'http://127.0.0.1:8080')
          })
        },
      },
      '/ws': {
        target: 'ws://127.0.0.1:8080',
        ws: true,
        changeOrigin: true,
        // 后端 WebSocket 同样执行 host-based Origin 校验。
        configure: (proxy) => {
          proxy.on('proxyReqWs', (request) => {
            request.setHeader('Origin', 'http://127.0.0.1:8080')
          })
        },
      }
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})