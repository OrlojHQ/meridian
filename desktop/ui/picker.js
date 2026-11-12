const form = document.querySelector("#gateway-form");
const urlField = document.querySelector("#url-field");
const urlInput = form.querySelector('input[name="url"]');
const status = document.querySelector("#status");

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
  return "Could not open the gateway.";
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

function apply(state) {
  const mode = state && state.mode === "remote" ? "remote" : "local";
  const radio = form.querySelector(`input[name="mode"][value="${mode}"]`);
  if (radio) radio.checked = true;
  if (state && state.origin && !urlInput.value) urlInput.value = state.origin;
  syncUrlField();
  const busy = state && state.phase === "starting";
  for (const control of form.querySelectorAll("input, button")) control.disabled = busy;
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

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const mode = selectedMode();
  const url = urlInput.value.trim();
  setStatus(mode === "remote" ? "Opening the gateway…" : "Connecting to the local gateway…", false);
  for (const control of form.querySelectorAll("input, button")) control.disabled = true;
  try {
    await invoke("gateway_connect", { mode, url: url || null });
  } catch (error) {
    for (const control of form.querySelectorAll("input, button")) control.disabled = false;
    syncUrlField();
    setStatus(errorText(error), true);
  }
});

syncUrlField();
void refresh();
