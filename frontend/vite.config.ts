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
      "/board": "http://127.0.0.1:8080",
      "/harness-setups": "http://127.0.0.1:8080",
      "/provider-connections": "http://127.0.0.1:8080",
      "/claude-login": "http://127.0.0.1:8080",
      "/secrets": "http://127.0.0.1:8080",
      "/coordination-requests": "http://127.0.0.1:8080",
      "/coordination-grants": "http://127.0.0.1:8080",
      "/project-thread-intents": "http://127.0.0.1:8080",
      "/thread-links": "http://127.0.0.1:8080",
    },
  },
});
