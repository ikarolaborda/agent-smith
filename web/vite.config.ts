import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        /*
         * Split heavy vendor groups into separate chunks so the main bundle
         * stays small and browsers can cache react/bootstrap/markdown across
         * deploys when only app code changes.
         */
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined;
          if (id.includes('react-bootstrap') || id.includes('bootstrap')) return 'vendor-bootstrap';
          if (id.includes('react-markdown') || id.includes('rehype') || id.includes('remark') || id.includes('highlight.js')) {
            return 'vendor-markdown';
          }
          if (id.includes('/react/') || id.includes('/react-dom/') || id.includes('/scheduler/')) return 'vendor-react';
          return undefined;
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/v1': 'http://localhost:9090',
      '/healthz': 'http://localhost:9090',
    },
  },
});
