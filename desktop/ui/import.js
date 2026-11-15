// The import window (ADR 0049). It shows what the bundled importer found on
// this computer and saves only when Save is clicked. Everything is rendered
// with textContent; nothing here is HTML from the importer or a gateway.
const destination = document.querySelector("#destination");
const found = document.querySelector("#found");
const harnesses = document.querySelector("#harnesses");
const git = document.querySelector("#git");
const hooks = document.querySelector("#hooks");
const logins = document.querySelector("#logins");
const save = document.querySelector("#save");
const dismiss = document.querySelector("#dismiss");
const cancel = document.querySelector("#cancel");
const status = document.querySelector("#status");

// The same names the web UI uses (frontend/src/shell/capsuleActivity.ts).
const names = { codex: "Codex", claude: "Claude Code", opencode: "OpenCode", pi: "Pi", dsh: "DeepSeek Harness" };
const harnessName = (id) => names[id] || id;

function invoke(command) {
  const internals = window.__TAURI_INTERNALS__;
  if (!internals || typeof internals.invoke !== "function") {
    return Promise.reject(new Error("This page only runs inside the Meridian window."));
  }
  return internals.invoke(command, {});
}

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}

function list(items) {
  if (items.length <= 1) return items.join("");
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

function setStatus(text, isError) {
  status.textContent = text || "";
  status.setAttribute("role", isError ? "alert" : "status");
  status.classList.toggle("error", Boolean(isError));
}

// What an import leaves out, one line per reason. A reason shared by more
// than three items, such as Codex's runtime caches and logs, is one line with
// a count that expands to the full list.
function exclusions(issues) {
  const groups = new Map();
  for (const issue of issues) {
    if (!groups.has(issue.reason)) groups.set(issue.reason, []);
    groups.get(issue.reason).push(issue.path);
  }
  const list = element("ul");
  for (const [reason, paths] of groups) {
    if (paths.length <= 3) {
      for (const path of paths) list.append(element("li", `${path}: ${reason}`));
      continue;
    }
    const item = element("li");
    const details = element("details");
    details.append(element("summary", `${paths.length} items: ${reason}`));
    const inner = element("ul");
    for (const path of paths) inner.append(element("li", path));
    details.append(inner);
    item.append(details);
    list.append(item);
  }
  return list;
}

function renderReview(message, label) {
  const account = message.account || {};
  const who = account.mode === "team" ? account.displayName || account.email || "your account" : "this installation";
  destination.textContent = `Saving to ${label} as ${who}.`;
  const review = message.review || {};
  const rows = Array.isArray(review.harnesses) ? review.harnesses : [];
  found.textContent = rows.length
    ? `We found ${list(rows.map((row) => harnessName(row.preview && row.preview.harness)))} on this computer.`
    : "No harness configuration was found on this computer.";
  harnesses.replaceChildren();
  for (const row of rows) {
    const preview = row.preview || {};
    const section = element("section", undefined, "harness");
    const change = row.status === "unchanged" ? "unchanged" : row.status === "new" ? "new" : "changed";
    section.append(element("h2", `${harnessName(preview.harness)} — ${change}`));
    // Against the saved profile: + new, ~ changed. A harness with nothing
    // saved yet is all new, so its files are listed plainly.
    const added = new Set(row.status === "changed" ? row.added || [] : []);
    const edited = new Set(row.status === "changed" ? row.changed || [] : []);
    const kept = element("ul");
    for (const path of preview.files || []) {
      const mark = added.has(path) ? ["+", "new", "added"] : edited.has(path) ? ["~", "changed", "changed"] : null;
      const item = element("li", undefined, mark ? `diff ${mark[2]}` : "diff");
      item.append(element("span", mark ? mark[0] : "", "marker"));
      item.append(document.createTextNode(path));
      if (mark) item.append(element("span", ` ${mark[1]}`, "tag"));
      kept.append(item);
    }
    if (kept.childElementCount) {
      section.append(element("h3", "Kept"));
      section.append(kept);
    }
    const removed = row.status === "changed" ? row.removed || [] : [];
    if (removed.length) {
      const gone = element("ul");
      for (const path of removed) {
        const item = element("li", undefined, "diff removed");
        item.append(element("span", "−", "marker"));
        item.append(document.createTextNode(path));
        item.append(element("span", " removed", "tag"));
        gone.append(item);
      }
      section.append(element("h3", "No longer on this computer"));
      section.append(gone);
    }
    const left = exclusions(preview.issues || []);
    if (left.childElementCount) {
      section.append(element("h3", "Left out"));
      section.append(left);
    }
    const warned = element("ul");
    for (const issue of preview.warnings || []) warned.append(element("li", `${issue.path}: ${issue.reason}`));
    if (warned.childElementCount) {
      section.append(element("h3", "Kept with a warning"));
      section.append(warned);
    }
    section.append(element("p", row.connection ? `Login: your default connection “${row.connection}”.` : "Login: no default connection yet.", "note"));
    harnesses.append(section);
  }
  for (const issue of review.notImported || []) {
    harnesses.append(element("p", `${harnessName(issue.path)}: not imported. ${issue.reason}`, "note"));
  }
  const local = review.git && review.git.local;
  git.hidden = !local;
  git.replaceChildren();
  if (local && local.identity && local.identity.name) {
    git.append(element("h2", "Git commit identity"));
    git.append(element("p", review.git.changed
      ? `Use ${local.identity.name} <${local.identity.email}> from ${local.path}.`
      : "Unchanged."));
  }
  if (local) for (const note of local.notes || []) git.append(element("p", note, "note"));
  hooks.hidden = rows.length === 0;
  logins.hidden = rows.length === 0;
  save.disabled = false;
  save.textContent = message.changes ? "Save" : "Nothing to save";
  if (!message.changes) save.disabled = true;
  dismiss.hidden = false;
}

let done = false;
// The review arrives once; rendering it again on every poll would close any
// expanded group and replace the buttons under the pointer.
let rendered = false;
async function refresh() {
  if (done) return;
  let state;
  try {
    state = await invoke("import_state");
  } catch (error) {
    setStatus(String(error && error.message ? error.message : error), true);
    return;
  }
  const label = state.label || state.origin || "this Station";
  const messages = Array.isArray(state.messages) ? state.messages : [];
  const last = messages[messages.length - 1];
  const review = messages.find((message) => message.type === "review");
  if (review && !rendered) {
    renderReview(review, label);
    rendered = true;
  }
  if (!last) {
    if (state.finished) {
      setStatus("The import stopped. Close this window and try again.", true);
      done = true;
    }
    return;
  }
  if (last.type === "signedOut") {
    destination.textContent = `Sign in to ${label} in the Meridian window, then open this again.`;
    save.hidden = true;
    dismiss.hidden = true;
    cancel.textContent = "Close";
    done = true;
  } else if (last.type === "error") {
    setStatus(last.message || "The import could not finish.", true);
    save.hidden = true;
    dismiss.hidden = true;
    cancel.textContent = "Close";
    done = true;
  } else if (last.type === "saved") {
    const saved = Array.isArray(last.saved) ? last.saved.map(harnessName) : [];
    setStatus(saved.length
      ? `Saved ${list(saved)}. New Capsules use this profile; existing Capsules keep theirs until you update them.`
      : "Your profile is up to date.", false);
    save.hidden = true;
    dismiss.hidden = true;
    cancel.textContent = "Close";
    done = true;
  } else if (last.type === "cancelled") {
    done = true;
  }
}

save.addEventListener("click", async () => {
  save.disabled = true;
  dismiss.hidden = true;
  setStatus("Saving…", false);
  try {
    await invoke("import_save");
  } catch (error) {
    setStatus(String(error && error.message ? error.message : error), true);
  }
});
dismiss.addEventListener("click", () => void invoke("import_dismiss"));
cancel.addEventListener("click", () => void invoke("import_cancel"));

void refresh();
setInterval(() => void refresh(), 500);
