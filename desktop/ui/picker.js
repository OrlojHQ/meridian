const form = document.querySelector("#gateway-form");
const urlField = document.querySelector("#url-field");
const urlInput = form.querySelector('input[name="url"]');
const status = document.querySelector("#status");
const recentSection = document.querySelector("#recent");
const recentList = document.querySelector("#recent-list");

function invoke(command, args) {
  const internals = window.__TAURI_INTERNALS__;
  if (!internals || typeof internals.invoke !== "function") {
    return Promise.reject(new Error("This page only runs inside the Meridian window."));
  }
  return internals.invoke(command, args ?? {});
}

function errorText(error) {
  if (typeof error === "string" && error.trim() !== "") return error;
  if (error && typeof error === "object" && "message" in error && error.message) {
    return String(error.message);
  }
  return "Could not open the Station.";
}

function setStatus(message, isError) {
  status.textContent = message || "";
  status.classList.toggle("error", Boolean(isError) && Boolean(message));
  status.setAttribute("role", isError ? "alert" : "status");
}

function selectedMode() {
  const picked = form.querySelector('input[name="mode"]:checked');
  return picked ? picked.value : "local";
}

function syncUrlField() {
  const remote = selectedMode() === "remote";
  urlField.hidden = !remote;
  urlInput.required = remote;
}

function setBusy(busy) {
  for (const control of document.querySelectorAll("input, button")) control.disabled = busy;
}

async function connect(mode, url) {
  setStatus(mode === "remote" ? "Opening the Station…" : "Connecting to the local Station…", false);
  setBusy(true);
  try {
    await invoke("gateway_connect", { mode, url: url || null });
  } catch (error) {
    setBusy(false);
    syncUrlField();
    setStatus(errorText(error), true);
  }
}

// Recent gateways come from the shell's own config: origins and host labels,
// nothing else. Built with textContent only.
function renderRecent(state) {
  const recent = Array.isArray(state && state.recent) ? state.recent : [];
  recentList.replaceChildren();
  recentSection.hidden = recent.length === 0;
  for (const entry of recent) {
    if (!entry || typeof entry.origin !== "string" || typeof entry.label !== "string") continue;
    const item = document.createElement("li");
    const open = document.createElement("button");
    open.type = "button";
    open.className = "recent-open";
    const name = document.createElement("strong");
    name.textContent = entry.label;
    const origin = document.createElement("small");
    origin.textContent = entry.origin;
    open.append(name, origin);
    if (state.mode === "remote" && state.origin === entry.origin) {
      open.setAttribute("aria-current", "true");
    }
    open.addEventListener("click", () => void connect("remote", entry.origin));
    const forget = document.createElement("button");
    forget.type = "button";
    forget.className = "recent-forget";
    forget.textContent = "Forget";
    forget.setAttribute("aria-label", `Forget ${entry.label}`);
    forget.addEventListener("click", async () => {
      try {
        await invoke("gateway_forget", { origin: entry.origin });
        await refresh();
      } catch (error) {
        setStatus(errorText(error), true);
      }
    });
    item.append(open, forget);
    recentList.append(item);
  }
}

function apply(state) {
  const mode = state && state.mode === "remote" ? "remote" : "local";
  const radio = form.querySelector(`input[name="mode"][value="${mode}"]`);
  if (radio) radio.checked = true;
  if (state && state.origin && !urlInput.value) urlInput.value = state.origin;
  syncUrlField();
  renderRecent(state);
  setBusy(Boolean(state && state.phase === "starting"));
  if (state && state.message) setStatus(state.message, state.phase === "error");
}

async function refresh() {
  try {
    const state = await invoke("gateway_state");
    apply(state);
    if (state && state.phase === "starting") window.setTimeout(refresh, 400);
  } catch (error) {
    setStatus(errorText(error), true);
  }
}

for (const radio of form.querySelectorAll('input[name="mode"]')) {
  radio.addEventListener("change", syncUrlField);
}

form.addEventListener("submit", (event) => {
  event.preventDefault();
  void connect(selectedMode(), urlInput.value.trim());
});

syncUrlField();
void refresh();

// "Check this computer for setup changes" (ADR 0049). On by default.
const setupCheck = document.querySelector("#setup-check");
if (setupCheck) {
  invoke("setup_check_state").then(
    (enabled) => { setupCheck.checked = enabled !== false; },
    () => { setupCheck.closest("label").hidden = true; },
  );
  setupCheck.addEventListener("change", () => {
    invoke("setup_check_set", { enabled: setupCheck.checked }).catch((error) => {
      setupCheck.checked = !setupCheck.checked;
      setStatus(errorText(error), true);
    });
  });
}
