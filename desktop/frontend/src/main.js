import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "./style.css";

// Wails injects these globals into the webview at runtime. Guard so the page
// still loads (degraded) in a plain browser during frontend-only dev.
const App = window.go?.main?.App;
const wails = window.runtime;

// ---- state ----
const terms = new Map();      // id -> { term, fit, pane, termEl }
const shellTerms = new Map(); // id -> { term, fit }
let sessions = [];
let focusedId = null;
let view = "terminal";        // terminal | diff | shell
let gridMode = false;
let attached = false;         // true = keystrokes routed to the focused agent

// ---- element refs ----
const $ = (sel) => document.querySelector(sel);
const listEl = $("#session-list");
const termHost = $("#term-host");
const shellHost = $("#shell-host");
const termEmpty = $("#term-empty");
const focusTitle = $("#focus-title");

// ---- terminal factory ----
const TERM_OPTS = {
  fontFamily: '"Cascadia Code", "JetBrains Mono", Consolas, monospace',
  fontSize: 13,
  cursorBlink: true,
  scrollback: 10000,
  theme: {
    background: "#16181d",
    foreground: "#d7dbe0",
    cursor: "#6aa3ff",
    selectionBackground: "#33415e",
  },
};

function ensureTerm(id) {
  let entry = terms.get(id);
  if (entry) return entry;

  const pane = document.createElement("div");
  pane.className = "term-pane";
  pane.dataset.id = id;

  const title = document.createElement("div");
  title.className = "pane-title";
  title.textContent = labelFor(id);
  pane.appendChild(title);

  const termEl = document.createElement("div");
  termEl.className = "pane-term";
  pane.appendChild(termEl);

  // Clicking a terminal selects it; in single-pane view it also attaches, so a
  // click-and-type lands in the agent like any terminal. In grid it only
  // selects, so you can click around without hijacking the keyboard.
  pane.addEventListener("mousedown", () => {
    focusSession(id);
    if (!gridMode) attach();
  });
  termHost.appendChild(pane);

  const term = new Terminal(TERM_OPTS);
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.open(termEl);
  // Keystrokes / paste flow back to the agent PTY.
  term.onData((d) => App?.SendInput(id, d));

  entry = { term, fit, pane, termEl };
  terms.set(id, entry);

  // Repaint anything that streamed before this terminal existed.
  App?.GetBuffer(id).then((buf) => { if (buf) term.write(buf); });
  return entry;
}

function fitTerm(entry, id) {
  if (!entry || !entry.pane.offsetParent) return; // not visible
  try {
    entry.fit.fit();
    App?.ResizeSession(id, entry.term.cols, entry.term.rows);
  } catch (_) { /* pane has zero size; ignore */ }
}

function fitVisible() {
  requestAnimationFrame(() => {
    for (const [id, entry] of terms) {
      if (entry.pane.offsetParent) fitTerm(entry, id);
    }
    const sh = shellTerms.get(focusedId);
    if (sh && shellHost.offsetParent) {
      try { sh.fit.fit(); App?.ResizeShell(focusedId, sh.term.cols, sh.term.rows); } catch (_) {}
    }
  });
}

// ---- rendering ----
function labelFor(id) {
  const s = sessions.find((x) => x.id === id);
  return s ? s.label : id;
}

function renderSidebar() {
  listEl.innerHTML = "";
  for (const s of sessions) {
    const li = document.createElement("li");
    li.className = "session" + (s.id === focusedId ? " focused" : "");
    li.innerHTML = `
      <div class="row1">
        <span class="dot ${s.status}"></span>
        <span class="label">${escapeHtml(s.label)}</span>
      </div>
      <div class="meta">${escapeHtml(s.agentName || "claude")} · ${escapeHtml(s.branch || "")} ${s.live ? "" : "· (stopped)"}</div>`;
    li.addEventListener("click", () => focusSession(s.id));
    listEl.appendChild(li);
  }
  // Reflect live set into grid panes (mark which terminals belong to live ones).
  for (const [id, entry] of terms) {
    entry.pane.classList.toggle("focused", id === focusedId);
  }
}

// focusSession selects a session without sending it any input. Attaching
// (routing keystrokes to the agent) is a separate, explicit step — see attach().
function focusSession(id) {
  if (attached && id !== focusedId) detach(); // switching focus drops the old attachment
  focusedId = id;
  const s = sessions.find((x) => x.id === id);
  focusTitle.textContent = s ? `${s.label} — ${s.branch || ""}` : "";
  termEmpty.classList.toggle("hidden", !!id);
  applyPaneVisibility();
  if (view === "diff") loadDiff(id);
  if (view === "shell") openShell(id);
  renderSidebar();
  updateModeHint();
  fitVisible();
}

// attach routes the keyboard to the focused session's agent. Resumes a stopped
// session first. Mirrors the TUI's ModeAttached; Ctrl+Q detaches.
function attach() {
  if (!focusedId) return;
  const s = sessions.find((x) => x.id === focusedId);
  const entry = ensureTerm(focusedId);
  if (s && !s.live) {
    entry.term.write(`\r\n\x1b[33m[resuming…]\x1b[0m\r\n`);
    App?.ResumeSession(focusedId).catch((e) => entry.term.write(`\r\n\x1b[31m${e}\x1b[0m\r\n`));
  }
  if (view !== "terminal") setView("terminal");
  attached = true;
  document.body.classList.add("attached");
  entry.term.focus();
  updateModeHint();
}

function detach() {
  attached = false;
  document.body.classList.remove("attached");
  const entry = terms.get(focusedId);
  if (entry) entry.term.blur();
  if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
  updateModeHint();
}

function updateModeHint() {
  const hint = $("#mode-hint");
  if (!focusedId) { hint.textContent = ""; hint.classList.remove("attached"); return; }
  if (attached) {
    hint.textContent = "● attached — Ctrl+Q to detach";
    hint.classList.add("attached");
  } else {
    hint.textContent = "navigation — ↵ to attach";
    hint.classList.remove("attached");
  }
}

function applyPaneVisibility() {
  termHost.classList.toggle("grid", gridMode);
  for (const [id, entry] of terms) {
    if (gridMode) {
      entry.pane.classList.add("show"); // grid CSS shows all
    } else {
      entry.pane.classList.toggle("show", id === focusedId);
    }
  }
}

// ---- views / tabs ----
function setView(v) {
  view = v;
  document.querySelectorAll(".tab").forEach((t) => t.classList.toggle("active", t.dataset.view === v));
  $("#view-terminal").classList.toggle("active", v === "terminal");
  $("#view-diff").classList.toggle("active", v === "diff");
  $("#view-shell").classList.toggle("active", v === "shell");
  if (v === "diff" && focusedId) loadDiff(focusedId);
  if (v === "shell" && focusedId) openShell(focusedId);
  fitVisible();
}

async function loadDiff(id) {
  const pre = $("#diff-pre");
  pre.textContent = "loading…";
  try {
    const raw = await App.GetDiff(id);
    pre.innerHTML = colorizeDiff(raw || "(no changes vs base)");
  } catch (e) {
    pre.textContent = String(e);
  }
}

function colorizeDiff(text) {
  return text.split("\n").map((line) => {
    const e = escapeHtml(line);
    if (line.startsWith("+++") || line.startsWith("---")) return `<span class="diff-meta">${e}</span>`;
    if (line.startsWith("+")) return `<span class="diff-add">${e}</span>`;
    if (line.startsWith("-")) return `<span class="diff-del">${e}</span>`;
    if (line.startsWith("@@")) return `<span class="diff-hunk">${e}</span>`;
    if (line.startsWith("diff ") || line.startsWith("index ")) return `<span class="diff-meta">${e}</span>`;
    return e;
  }).join("\n");
}

// ---- shell tab ----
function ensureShellTerm(id) {
  let entry = shellTerms.get(id);
  if (entry) return entry;
  shellHost.innerHTML = "";
  const term = new Terminal(TERM_OPTS);
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.open(shellHost);
  term.onData((d) => App?.SendShellInput(id, d));
  entry = { term, fit };
  shellTerms.set(id, entry);
  return entry;
}

function openShell(id) {
  const entry = ensureShellTerm(id);
  // Move this session's shell term into the host (one shell pane shown at a time).
  if (entry.term.element && entry.term.element.parentElement !== shellHost) {
    shellHost.innerHTML = "";
    shellHost.appendChild(entry.term.element);
  }
  App?.OpenShell(id).catch((e) => entry.term.write(`\r\n\x1b[31m${e}\x1b[0m\r\n`));
}

// ---- actions ----
async function refreshSessions() {
  if (!App) return;
  sessions = (await App.ListSessions()) || [];
  if (!focusedId && sessions.length) focusedId = sessions[0].id;
  // Pre-create terminals for live sessions so grid mode shows them and no
  // early output is lost.
  for (const s of sessions) if (s.live) ensureTerm(s.id);
  renderSidebar();
  if (focusedId) {
    const s = sessions.find((x) => x.id === focusedId);
    focusTitle.textContent = s ? `${s.label} — ${s.branch || ""}` : "";
  }
  termEmpty.classList.toggle("hidden", sessions.length > 0);
  applyPaneVisibility();
}

async function killFocused() {
  if (focusedId) { await App.KillSession(focusedId); refreshSessions(); }
}
async function discardFocused() {
  if (!focusedId) return;
  const s = sessions.find((x) => x.id === focusedId);
  if (!confirm(`Discard "${s?.label}"? The worktree will be destroyed.`)) return;
  const id = focusedId;
  await App.DiscardSession(id);
  const entry = terms.get(id);
  if (entry) { entry.term.dispose(); entry.pane.remove(); terms.delete(id); }
  shellTerms.delete(id);
  focusedId = null;
  await refreshSessions();
}

// ---- modal ----
const backdrop = $("#modal-backdrop");
async function openModal() {
  // Populate the repo type-ahead with known repos; default to the first (the
  // launch repo). The user can type a path, pick a suggestion, or Browse…
  const repos = (await App?.KnownRepos()) || [];
  const dl = $("#repo-list");
  dl.innerHTML = "";
  for (const r of repos) {
    const opt = document.createElement("option");
    opt.value = r;
    dl.appendChild(opt);
  }
  $("#m-repo").value = repos[0] || (await App?.DefaultRepo()) || "";
  $("#m-name").value = "";
  $("#m-prompt").value = "";
  $("#m-mcp").checked = false;
  const sel = $("#m-agent");
  sel.innerHTML = "";
  for (const name of (await App?.AgentNames()) || ["claude"]) {
    const opt = document.createElement("option");
    opt.value = name; opt.textContent = name;
    sel.appendChild(opt);
  }
  $("#modal-err").textContent = "";
  backdrop.classList.remove("hidden");
  $("#m-name").focus();
}
function closeModal() { backdrop.classList.add("hidden"); }

async function browseForRepo() {
  try {
    const p = await App.BrowseForRepo();
    if (p) $("#m-repo").value = p;
  } catch (e) {
    $("#modal-err").textContent = String(e);
  }
}

async function spawn() {
  const repo = $("#m-repo").value.trim();
  if (!repo) { $("#modal-err").textContent = "repository path is required"; return; }
  const btn = $("#m-spawn");
  btn.disabled = true; btn.textContent = "Spawning…";
  try {
    const dto = await App.SpawnSession(
      repo, $("#m-prompt").value, $("#m-name").value.trim(), $("#m-agent").value, $("#m-mcp").checked
    );
    closeModal();
    focusedId = dto.id;
    ensureTerm(dto.id);
    await refreshSessions();
    focusSession(dto.id);
    attach(); // land the user inside the fresh agent
  } catch (e) {
    $("#modal-err").textContent = String(e);
  } finally {
    btn.disabled = false; btn.textContent = "Spawn";
  }
}

// ---- events from Go ----
if (wails) {
  wails.EventsOn("pty:data", ({ id, data }) => {
    ensureTerm(id).term.write(data);
  });
  wails.EventsOn("shell:data", ({ id, data }) => {
    const e = shellTerms.get(id);
    if (e) e.term.write(data);
  });
  wails.EventsOn("session:exit", ({ id }) => {
    const e = terms.get(id);
    if (e) e.term.write("\r\n\x1b[90m[process exited]\x1b[0m\r\n");
  });
  wails.EventsOn("sessions:change", () => refreshSessions());
}

// ---- navigation helpers ----
function navFocus(delta) {
  if (!sessions.length) return;
  let idx = sessions.findIndex((s) => s.id === focusedId);
  if (idx < 0) idx = 0;
  else idx = (idx + delta + sessions.length) % sessions.length;
  focusSession(sessions[idx].id);
}

function toggleGrid() {
  gridMode = !gridMode;
  $("#grid-toggle").classList.toggle("on", gridMode);
  if (gridMode) { detach(); setView("terminal"); }
  applyPaneVisibility();
  fitVisible();
}

const VIEWS = ["terminal", "diff", "shell"];
function cycleView() {
  setView(VIEWS[(VIEWS.indexOf(view) + 1) % VIEWS.length]);
}

// ---- global keyboard ----
// Two modes mirror the TUI: navigation (shortcuts) and attached (keystrokes go
// to the agent, Ctrl+Q detaches). Capture phase so we beat xterm to the detach
// chord. While attached, every other key falls through to the terminal.
document.addEventListener("keydown", (ev) => {
  const modalOpen = !backdrop.classList.contains("hidden");
  if (modalOpen) {
    if (ev.key === "Escape") closeModal();
    if (ev.key === "Enter" && ev.target.id !== "m-prompt") { ev.preventDefault(); spawn(); }
    return;
  }
  if (attached) {
    if (ev.ctrlKey && (ev.key === "q" || ev.key === "Q")) {
      ev.preventDefault(); ev.stopPropagation(); detach();
    }
    return; // all other keys belong to the agent
  }
  switch (ev.key) {
    case "j": case "ArrowDown": ev.preventDefault(); navFocus(1); break;
    case "k": case "ArrowUp": ev.preventDefault(); navFocus(-1); break;
    case "n": ev.preventDefault(); openModal(); break;
    case "x": ev.preventDefault(); killFocused(); break;
    case "d": ev.preventDefault(); discardFocused(); break;
    case "g": ev.preventDefault(); toggleGrid(); break;
    case "Enter": ev.preventDefault(); attach(); break;
    case "Tab": ev.preventDefault(); cycleView(); break;
    case "1": ev.preventDefault(); setView("terminal"); break;
    case "2": ev.preventDefault(); setView("diff"); break;
    case "3": ev.preventDefault(); setView("shell"); break;
  }
}, true);

// ---- wiring ----
$("#new-btn").addEventListener("click", openModal);
$("#m-cancel").addEventListener("click", closeModal);
$("#m-spawn").addEventListener("click", spawn);
$("#m-browse").addEventListener("click", browseForRepo);
$("#kill-btn").addEventListener("click", killFocused);
$("#discard-btn").addEventListener("click", discardFocused);
document.querySelectorAll(".tab").forEach((t) => t.addEventListener("click", () => setView(t.dataset.view)));
$("#grid-toggle").addEventListener("click", toggleGrid);
backdrop.addEventListener("mousedown", (e) => { if (e.target === backdrop) closeModal(); });
window.addEventListener("resize", fitVisible);
new ResizeObserver(fitVisible).observe(termHost);

// ---- boot ----
refreshSessions().then(() => { fitVisible(); updateModeHint(); });

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
