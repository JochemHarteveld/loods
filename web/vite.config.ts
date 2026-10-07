import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// `npm run dev` serves the UI on :5173 and proxies the API to a running `loods --no-open --dev`.
export default defineConfig({
	plugins: [svelte()],
	server: {
		proxy: { '/api': { target: 'http://127.0.0.1:7777', changeOrigin: true, ws: true } }
	},
	build: { outDir: 'dist', emptyOutDir: true }
});
