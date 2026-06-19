import { defineConfig } from 'vite';
import preact from '@preact/preset-vite';

// Builds the front-end into web/dist/, which is embedded into the k3c binary
// via go:embed. In dev, `npm run dev` proxies /api to a running `k3c web`.
export default defineConfig({
  plugins: [preact()],
  base: './',
  build: { outDir: 'dist', emptyOutDir: true },
  server: { proxy: { '/api': 'http://127.0.0.1:7654' } },
});
