import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [svelte()],
  // One bundle without dynamic imports: the module preload polyfill would have nothing to preload.
  build: { outDir: 'dist', emptyOutDir: true, modulePreload: { polyfill: false } },
  server: { proxy: { '/api': 'http://localhost:9898' } },
});
