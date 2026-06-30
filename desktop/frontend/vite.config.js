import { defineConfig } from "vite";

// Relative base so the built assets resolve under Wails' embedded asset server.
export default defineConfig({
  base: "./",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
