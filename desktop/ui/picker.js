const app = document.querySelector("#app");

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

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function logo(small) {
  const wrap = el("div", small ? "logo-wrap small" : "logo-wrap");
  const img = document.createElement("img");
  img.src = "mark.svg";
  img.alt = "";
  img.width = small ? 64 : 104;
  img.height = small ? 64 : 104;
  wrap.append(img);
  return wrap;
}

let state = null;
let selectedId = "local";
let picked = false;
let remoteOpen = false;
let renaming = null;
let confirmRemove = null;
let addOpen = false;
let sawAdd = false;
let busy = false;

function setBusy(next) {
  busy = next;
  for (const control of document.querySelectorAll("input, button")) control.disabled = next;
}

function statusNode(message, isError) {
  const status = el("p", isError ? "error" : "");
  status.id = "status";
  status.setAttribute("role", isError ? "alert" : "status");
  status.textContent = message || "";
  if (isError) {
    status.style.padding = "0.65rem 0.75rem";
    status.style.borderRadius = "0.6rem";
    status.style.background = "var(--danger-bg)";
    status.style.color = "var(--danger)";
  }
  return status;
}

async function connect(args) {
  const status = document.querySelector("#status");
  if (status) status.textContent = args.mode === "remote" ? "Opening the Station…" : "Connecting to the local Station…";
  setBusy(true);
  try {
    await invoke("gateway_connect", args);
  } catch (error) {
    setBusy(false);
    render(errorText(error), true);
  }
}

function field(label, optional, input) {
  const wrap = el("label", "field");
  const title = el("span");
  title.append(label);
  if (optional) title.append(el("em", null, "Optional"));
  wrap.append(title, input);
  return wrap;
}

function renderWelcome(message, isError) {
  const root = el("section", "welcome");
  root.append(logo(false), el("h1", null, "Meridian"));
  root.append(el("p", "tagline", state && state.mac ? "Run Meridian on this Mac" : "Run Meridian on this computer"));
  const start = el("button", null, "Get started");
  start.type = "button";
  start.addEventListener("click", () => void connect({ mode: "local" }));
  root.append(start);
  const link = el("button", "text-link", "Connect to a remote Station");
  link.type = "button";
  link.append(el("span", null, remoteOpen ? " ▾" : " ›"));
  link.addEventListener("click", () => {
    remoteOpen = !remoteOpen;
    render();
  });
  root.append(link);
  if (remoteOpen) {
    const panel = el("section", "remote-panel");
    const header = document.createElement("header");
    header.append(el("strong", null, "Remote Station"));
    const close = el("button", "icon-button", "×");
    close.type = "button";
    close.setAttribute("aria-label", "Close");
    close.addEventListener("click", () => {
      remoteOpen = false;
      render();
    });
    header.append(close);
    const name = document.createElement("input");
    name.placeholder = "Spark";
    name.autocomplete = "off";
    const address = document.createElement("input");
    address.className = "mono";
    address.placeholder = "spark.local:8080";
    address.spellcheck = false;
    address.autocomplete = "off";
    panel.append(
      header,
      field("Name", true, name),
      field("Address", false, address),
      el("p", "hint", "Sign-in stays on the Station. Meridian doesn’t save that page’s token."),
    );
    const connectButton = el("button", null, "Connect");
    connectButton.type = "button";
    connectButton.addEventListener("click", () =>
      void connect({ mode: "remote", url: address.value.trim(), name: name.value.trim() || null }),
    );
    panel.append(connectButton);
    root.append(panel);
    window.setTimeout(() => address.focus(), 0);
  }
  if (message) root.append(statusNode(message, isError));
  return root;
}

function renderList(message, isError) {
  const stage = el("div", "picker-list");
  stage.append(logo(true), el("h1", null, "Meridian"), el("p", null, "Choose a Station"));
  const card = el("section", "station-card");
  const list = document.createElement("ul");
  for (const station of state.stations || []) {
    const item = document.createElement("li");
    const row = el("div", station.id === selectedId ? "station selected" : "station");
    const dot = el("span", "dot");
    if (station.online === true) dot.classList.add("online");
    if (station.online === false) dot.classList.add("offline");
    const copy = el("span", "station-copy");
    const name = el("span", "station-name");
    if (renaming === station.id) {
      const input = document.createElement("input");
      input.className = "rename-input";
      input.value = station.name;
      input.addEventListener("keydown", (event) => {
        if (event.key === "Escape") {
          renaming = null;
          render();
        }
        if (event.key === "Enter") {
          event.preventDefault();
          void saveName(station.id, input.value);
        }
      });
      name.append(input);
      window.setTimeout(() => input.focus(), 0);
    } else {
      name.append(station.name);
      if (station.local) name.append(el("span", "badge", "Local"));
    }
    const host = el("span", "station-host");
    host.append(el("span", "mono", station.local ? "This computer" : station.host));
    if (station.online === false) host.append(el("span", "offline-label", "Offline"));
    copy.append(name, host);
    row.append(dot, copy);
    row.addEventListener("click", (event) => {
      if (event.target.closest("button, input")) return;
      picked = true;
      selectedId = station.id;
      render();
    });
    const actions = el("span", "row-actions");
    const rename = el("button", "quiet", renaming === station.id ? "Save" : "Rename");
    rename.type = "button";
    rename.addEventListener("click", () => {
      if (renaming === station.id) {
        const input = row.querySelector("input");
        void saveName(station.id, input ? input.value : station.name);
        return;
      }
      renaming = station.id;
      render();
    });
    actions.append(rename);
    if (!station.local) {
      const remove = el("button", "quiet remove", "Remove");
      remove.type = "button";
      remove.addEventListener("click", () => {
        confirmRemove = station;
        render();
      });
      actions.append(remove);
    }
    row.append(actions);
    item.append(row);
    list.append(item);
  }
  card.append(list);
  const selected = (state.stations || []).find((station) => station.id === selectedId) || (state.stations || [])[0];
  const open = el("button", null, selected ? `Open ${selected.name}` : "Open");
  open.type = "button";
  open.addEventListener("click", () => {
    if (!selected) return;
    void connect(selected.local ? { mode: "local", id: "local" } : { id: selected.id });
  });
  const add = el("button", "add-station", "+ Add a Station");
  add.type = "button";
  add.addEventListener("click", () => {
    addOpen = true;
    render();
  });
  stage.append(card, open, add);
  if (message) stage.append(statusNode(message, isError));
  return stage;
}

async function saveName(id, name) {
  try {
    await invoke("gateway_rename", { id, name });
    renaming = null;
    await refresh();
  } catch (error) {
    render(errorText(error), true);
  }
}

function renderAdd() {
  const backdrop = el("div", "backdrop");
  const sheet = el("section", "sheet");
  sheet.setAttribute("role", "dialog");
  sheet.setAttribute("aria-label", "Add a Station");
  const header = document.createElement("header");
  header.append(el("h1", null, "Add a Station"));
  const close = el("button", "icon-button", "×");
  close.type = "button";
  close.setAttribute("aria-label", "Close");
  close.addEventListener("click", () => {
    addOpen = false;
    render();
  });
  header.append(close);
  const name = document.createElement("input");
  name.value = "";
  name.placeholder = "North";
  const address = document.createElement("input");
  address.className = "mono";
  address.placeholder = "10.0.0.8:8080";
  address.spellcheck = false;
  const row = el("div", "row");
  const cancel = el("button", "secondary", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", () => {
    addOpen = false;
    render();
  });
  const save = el("button", null, "Add Station");
  save.type = "button";
  save.addEventListener("click", async () => {
    try {
      await invoke("gateway_connect", { mode: "remote", url: address.value.trim(), name: name.value.trim() || null });
    } catch (error) {
      // Stay on the sheet and surface the error under the form.
      const note = sheet.querySelector(".hint.error") || el("p", "hint");
      note.className = "hint";
      note.style.color = "var(--danger)";
      note.textContent = errorText(error);
      if (!note.parentElement) sheet.insertBefore(note, row);
    }
  });
  row.append(cancel, save);
  sheet.append(
    header,
    field("Name", false, name),
    field("Address", false, address),
    el("p", "hint", "Host and port. Sign-in stays on the Station. Meridian doesn’t save that page’s token."),
    row,
  );
  backdrop.append(sheet);
  backdrop.addEventListener("click", (event) => {
    if (event.target === backdrop) {
      addOpen = false;
      render();
    }
  });
  return backdrop;
}

function renderConfirm() {
  const station = confirmRemove;
  const backdrop = el("div", "backdrop");
  const dialog = el("section", "confirm");
  dialog.setAttribute("role", "dialog");
  dialog.setAttribute("aria-label", "Remove Station");
  dialog.append(el("h1", null, `Remove ${station.name}?`));
  dialog.append(el("p", null, "Meridian forgets this address. The Station itself is left as it is."));
  const row = el("div", "row");
  const cancel = el("button", "secondary", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", () => {
    confirmRemove = null;
    render();
  });
  const remove = el("button", null, "Remove");
  remove.type = "button";
  remove.addEventListener("click", async () => {
    try {
      await invoke("gateway_remove", { id: station.id });
      confirmRemove = null;
      if (selectedId === station.id) selectedId = "local";
      await refresh();
    } catch (error) {
      confirmRemove = null;
      render(errorText(error), true);
    }
  });
  row.append(cancel, remove);
  dialog.append(row);
  backdrop.append(dialog);
  return backdrop;
}

function render(message, isError) {
  app.replaceChildren();
  const first = !state || state.firstRun;
  app.append(first ? renderWelcome(message, isError) : renderList(message, isError));
  if (!first && confirmRemove) app.append(renderConfirm());
  else if (!first && addOpen) app.append(renderAdd());
  if (state && state.message && !message) {
    const existing = document.querySelector("#status");
    if (!existing) app.append(statusNode(state.message, state.phase === "error"));
  }
}

async function refresh() {
  try {
    state = await invoke("gateway_state");
    if (state.addSheet && !sawAdd) {
      addOpen = true;
      sawAdd = true;
    }
    const current = (state.stations || []).find((station) => station.current);
    if (!picked && current) selectedId = current.id;
    render();
    setBusy(state.phase === "starting");
    if (state.phase === "starting") window.setTimeout(refresh, 400);
    void loadStatus();
  } catch (error) {
    render(errorText(error), true);
  }
}

async function loadStatus() {
  try {
    const statuses = await invoke("gateway_status");
    if (!Array.isArray(statuses) || !state) return;
    const online = new Map(statuses.map((item) => [item.id, item.online === true]));
    for (const station of state.stations || []) station.online = online.has(station.id) ? online.get(station.id) : undefined;
    if (!addOpen && !confirmRemove && !renaming) render();
  } catch {
    // Dots stay unknown. Opening a Station does not wait on this.
  }
}

void refresh();
