import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";

import { App } from "./App";
import { queryClient } from "./api/queries";
import { applyAppearance, readAppearance } from "./components/ui/appearance";
import { applyTheme, readTheme } from "./components/ui/theme";
// Optional faces for Settings > Appearance. Each loads only when selected.
import "@fontsource-variable/inter/wght.css";
import "@fontsource-variable/geist/wght.css";
import "@fontsource-variable/geist-mono/wght.css";
import "@fontsource-variable/ibm-plex-sans/wght.css";
import "@fontsource-variable/jetbrains-mono/wght.css";
import "@fontsource/ibm-plex-mono/400.css";
import "@fontsource/ibm-plex-mono/500.css";
import "@fontsource/ibm-plex-mono/600.css";
import "./styles.css";
import "./styles/tokens.css";
import "./styles/shell.css";
import "./styles/features.css";
import "./styles/components.css";
import "./styles/loading.css";
import "./styles/workbench.css";
import "./styles/notes.css";
import "./styles/depth.css";

const root = document.getElementById("root");
if (!root) {
  throw new Error("root element not found");
}

async function boot() {
  if (import.meta.env.DEV && new URLSearchParams(window.location.search).has("shots")) {
    const { installShotFixture } = await import("./shots/fixture");
    installShotFixture();
  }
  applyTheme(readTheme());
  applyAppearance(readAppearance());
  createRoot(root!).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </QueryClientProvider>
    </StrictMode>,
  );
}

void boot();
