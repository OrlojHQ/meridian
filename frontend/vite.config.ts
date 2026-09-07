import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/ui/",
  plugins: [react()],
  server: {
    proxy: {
      "/capabilities": "http://127.0.0.1:8080",
      "/auth": "http://127.0.0.1:8080",
      "/projects": "http://127.0.0.1:8080",
      "/capsules": "http://127.0.0.1:8080",
      "/threads": "http://127.0.0.1:8080",
      "/deliveries": "http://127.0.0.1:8080",
      "/runs": {
        target: "http://127.0.0.1:8080",
        ws: true,
      },
      "/moments": "http://127.0.0.1:8080",
      "/timelines": "http://127.0.0.1:8080",
    },
  },
});
