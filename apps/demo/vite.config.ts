import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    // Replaces vite-tsconfig-paths, native since Vite 8.
    tsconfigPaths: true,
  },
  server: {
    port: 3002,
  },
});
