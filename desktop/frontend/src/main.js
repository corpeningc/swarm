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

// Claude Code reads the OS clipboard itself for image pastes, triggered by
// Alt+V on Windows and Ctrl+V elsewhere.
const IMAGE_PASTE_CHORD = navigator.platform?.startsWith("Win") ? "\x1bv" : "\x16";

// wirePaste makes Ctrl(+Shift)+V work inside a terminal. The WebView2 host
// never delivers the native paste event to xterm's hidden textarea, so we
// intercept the chord and paste manually: clipboard text goes through
// term.paste() (which applies bracketed-paste wrapping); no text means an
// image is likely on the clipboard, so forward the agent's image-paste chord
// and let it read the clipboard natively.
function wirePaste(term, send) {
  const doPaste = async () => {
    let text = "";
    try { text = (await wails?.ClipboardGetText()) || ""; } catch (_) {}
    if (!text) { try { text = (await navigator.clipboard.readText()) || ""; } catch (_) {} }
    if (text) term.paste(text);
    else send(IMAGE_PASTE_CHORD);
  };
  term.attachCustomKeyEventHandler((ev) => {
    if (ev.type === "keydown" && ev.ctrlKey && !ev.altKey && (ev.key === "v" || ev.key === "V")) {
      ev.preventDefault();
      doPaste();
      return false;
    }
    return true;
  });
}

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
  wirePaneDrag(pane, title, id); // grid tiles reorder by dragging the title bar

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
  wirePaste(term, (d) => App?.SendInput(id, d));

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

// ---- repo grouping ----
// Sessions are held grouped by repo (repos in order of first appearance) so
// the sidebar can head each run with a single separator. Normalising the order
// here rather than only at render time means a drag that drops a row among
// another repo's sessions settles back into its own group, instead of
// splitting that repo across two separators.
function groupByRepo(list) {
  const groups = new Map();
  for (const s of list) {
    const key = s.repo || "";
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(s);
  }
  return [...groups.values()].flat();
}

// repoName is the trailing path segment — the sidebar is too narrow for more.
function repoName(repo) {
  if (!repo) return "(no repo)";
  const parts = repo.replace(/[\\/]+$/, "").split(/[\\/]/);
  return parts[parts.length - 1] || repo;
}

function renderSidebar() {
  listEl.innerHTML = "";
  // With every session in one repo the header is just noise — the window is
  // already that repo. Separators only earn their space once repos mix.
  const repos = [...new Set(sessions.map((s) => s.repo || ""))];
  let lastRepo = null;
  for (const s of sessions) {
    const repo = s.repo || "";
    if (repos.length > 1 && repo !== lastRepo) {
      lastRepo = repo;
      const sep = document.createElement("li");
      sep.className = "repo-sep";
      sep.title = repo || "no repository";
      const count = sessions.filter((x) => (x.repo || "") === repo).length;
      sep.innerHTML = `<span class="repo-name">${escapeHtml(repoName(repo))}</span>` +
        `<span class="repo-count">${count}</span>`;
      listEl.appendChild(sep);
    }
    const li = document.createElement("li");
    li.className = "session" + (s.id === focusedId ? " focused" : "");
    li.dataset.id = s.id;
    li.innerHTML = `
      <div class="row1">
        <span class="dot ${s.status}"></span>
        <span class="label">${escapeHtml(s.label)}</span>
      </div>
      <div class="meta">${escapeHtml(s.agentName || "claude")} · ${escapeHtml(s.branch || "")} ${s.live ? "" : "· (stopped)"}</div>`;
    if (s.id === renamingId) {
      const input = document.createElement("input");
      input.className = "rename-input";
      input.type = "text";
      input.value = renameValue;
      input.placeholder = s.name || s.id;
      input.title = "Nickname — display only, branch/worktree keep their name. Empty clears it.";
      input.addEventListener("input", () => { renameValue = input.value; });
      input.addEventListener("keydown", (e) => {
        e.stopPropagation();
        if (e.key === "Enter") commitRename();
        if (e.key === "Escape") cancelRename();
      });
      input.addEventListener("blur", () => { if (renamingId === s.id) commitRename(); });
      li.querySelector(".label").replaceWith(input);
    } else {
      li.addEventListener("click", () => focusSession(s.id));
      li.addEventListener("dblclick", () => startRename(s.id));
      wireRowDrag(li, s.id);
    }
    listEl.appendChild(li);
  }
  // Reflect live set into grid panes: focus ring + label (nicknames change).
  for (const [id, entry] of terms) {
    entry.pane.classList.toggle("focused", id === focusedId);
    entry.pane.querySelector(".pane-title").textContent = labelFor(id);
  }
}

// ---- drag to reorder ----
// HTML5 DnD; the dragged row is moved in the DOM live (no re-render — that
// would destroy the drag source and abort the drag), then the DOM order is
// committed to the backend on dragend.
let dragId = null;

function wireRowDrag(li, id) {
  li.draggable = true;
  li.addEventListener("dragstart", (e) => {
    dragId = id;
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", id); // some webviews need data to start a drag
    requestAnimationFrame(() => li.classList.add("dragging"));
  });
  li.addEventListener("dragover", (e) => {
    if (!dragId || dragId === id) return;
    e.preventDefault();
    const dragging = listEl.querySelector(`[data-id="${dragId}"]`);
    if (!dragging) return;
    const r = li.getBoundingClientRect();
    listEl.insertBefore(dragging, e.clientY < r.top + r.height / 2 ? li : li.nextSibling);
  });
  li.addEventListener("drop", (e) => e.preventDefault());
  li.addEventListener("dragend", () => {
    li.classList.remove("dragging");
    if (!dragId) return;
    dragId = null;
    commitOrder([...listEl.querySelectorAll(".session")].map((el) => el.dataset.id));
  });
}

// commitOrder applies a full id order locally (sidebar + grid panes) and
// persists it. The backend emits sessions:change, which re-syncs everything.
async function commitOrder(order) {
  sessions.sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id));
  sessions = groupByRepo(sessions);
  renderSidebar();
  orderPanes();
  await App?.ReorderSessions(sessions.map((s) => s.id));
}

// wirePaneDrag makes a grid tile draggable by its title bar. Tiles shift live
// while dragging (left half = drop before, right half = after); the resulting
// pane order is merged back into the full session order on dragend.
function wirePaneDrag(pane, handle, id) {
  handle.draggable = true;
  handle.addEventListener("dragstart", (e) => {
    dragId = id;
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", id);
    requestAnimationFrame(() => pane.classList.add("dragging"));
  });
  pane.addEventListener("dragover", (e) => {
    if (!dragId || dragId === id || !gridMode) return;
    e.preventDefault();
    const dragging = terms.get(dragId)?.pane;
    if (!dragging || dragging === pane) return;
    const r = pane.getBoundingClientRect();
    termHost.insertBefore(dragging, e.clientX < r.left + r.width / 2 ? pane : pane.nextSibling);
  });
  pane.addEventListener("drop", (e) => e.preventDefault());
  handle.addEventListener("dragend", () => {
    pane.classList.remove("dragging");
    if (!dragId) return;
    dragId = null;
    commitPaneOrder();
    fitVisible();
  });
}

// commitPaneOrder derives the new session order from the grid's DOM order.
// Sessions without a pane (restored, no terminal yet) keep their positions;
// pane-backed sessions are re-slotted into the remaining spots in pane order.
function commitPaneOrder() {
  const paneOrder = [...termHost.querySelectorAll(".term-pane")].map((p) => p.dataset.id);
  const inPanes = new Set(paneOrder);
  let k = 0;
  commitOrder(sessions.map((s) => (inPanes.has(s.id) ? paneOrder[k++] : s.id)));
}

// orderPanes keeps the grid tiles in session order. appendChild moves panes
// in place; skip entirely when already ordered so terminals aren't churned.
function orderPanes() {
  const want = sessions.map((s) => terms.get(s.id)?.pane).filter(Boolean);
  const have = [...termHost.querySelectorAll(".term-pane")];
  if (want.length === have.length && want.every((p, i) => p === have[i])) return;
  for (const p of want) termHost.appendChild(p);
  fitVisible();
}

// ---- rename (nickname) ----
// The nickname is a display-only alias — the branch and worktree keep the
// session's real name. Enter or blur commits; empty input clears; Esc cancels.
let renamingId = null;
let renameValue = "";

function startRename(id) {
  const s = sessions.find((x) => x.id === id);
  if (!s) return;
  renamingId = id;
  renameValue = s.nickname || "";
  renderSidebar();
  const input = listEl.querySelector(".rename-input");
  if (input) { input.focus(); input.select(); }
}

async function commitRename() {
  const id = renamingId;
  if (id === null) return;
  renamingId = null;
  await App?.SetNickname(id, renameValue.trim());
  await refreshSessions();
}

function cancelRename() {
  renamingId = null;
  renderSidebar();
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
  wirePaste(term, (d) => App?.SendShellInput(id, d));
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
  sessions = groupByRepo((await App.ListSessions()) || []);
  if (!focusedId && sessions.length) focusedId = sessions[0].id;
  // Pre-create terminals for live sessions so grid mode shows them and no
  // early output is lost.
  for (const s of sessions) if (s.live) ensureTerm(s.id);
  renderSidebar();
  orderPanes();
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
// ---- discard modal ----
// Two checkboxes escalate the teardown; both off (the default) just removes
// the session from the panel, keeping worktree and branch reattachable.
const discardBackdrop = $("#discard-backdrop");
let discardTargetId = null;

function openDiscardModal() {
  if (!focusedId) return;
  discardTargetId = focusedId;
  $("#d-label").textContent = `Remove "${labelFor(focusedId)}" from the panel?`;
  $("#d-worktree").checked = false;
  $("#d-branch").checked = false;
  discardBackdrop.classList.remove("hidden");
  $("#d-confirm").focus();
}

function closeDiscardModal() {
  discardBackdrop.classList.add("hidden");
  discardTargetId = null;
}

async function confirmDiscard() {
  const id = discardTargetId;
  if (!id) return;
  const removeWorktree = $("#d-worktree").checked;
  const deleteBranch = $("#d-branch").checked;
  closeDiscardModal();
  try {
    await App.DiscardSession(id, removeWorktree, deleteBranch);
  } catch (e) {
    // The orchestrator removes the session from the registry even when the
    // worktree can't be fully deleted, so still tear down the UI and refresh
    // below to reflect reality; just surface the warning to the log.
    wails?.LogError?.(`discard ${id}: ${e}`);
    console.error("discard failed", e);
  }
  const entry = terms.get(id);
  if (entry) { entry.term.dispose(); entry.pane.remove(); terms.delete(id); }
  shellTerms.delete(id);
  if (focusedId === id) focusedId = null;
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
  // An active rename input owns the keyboard (it handles Enter/Esc itself).
  if (renamingId !== null) return;
  if (!discardBackdrop.classList.contains("hidden")) {
    if (ev.key === "Escape") closeDiscardModal();
    if (ev.key === "Enter") { ev.preventDefault(); confirmDiscard(); }
    return;
  }
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
    case "d": ev.preventDefault(); openDiscardModal(); break;
    case "r": ev.preventDefault(); if (focusedId) startRename(focusedId); break;
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
$("#discard-btn").addEventListener("click", openDiscardModal);
$("#d-cancel").addEventListener("click", closeDiscardModal);
$("#d-confirm").addEventListener("click", confirmDiscard);
// Deleting the branch only works once the worktree holding it is gone, so the
// branch checkbox drags the worktree one along (and clearing worktree clears it).
$("#d-branch").addEventListener("change", (e) => { if (e.target.checked) $("#d-worktree").checked = true; });
$("#d-worktree").addEventListener("change", (e) => { if (!e.target.checked) $("#d-branch").checked = false; });
document.querySelectorAll(".tab").forEach((t) => t.addEventListener("click", () => setView(t.dataset.view)));
$("#grid-toggle").addEventListener("click", toggleGrid);
backdrop.addEventListener("mousedown", (e) => { if (e.target === backdrop) closeModal(); });
discardBackdrop.addEventListener("mousedown", (e) => { if (e.target === discardBackdrop) closeDiscardModal(); });
window.addEventListener("resize", fitVisible);
new ResizeObserver(fitVisible).observe(termHost);

// ---- boot ----
refreshSessions().then(() => { fitVisible(); updateModeHint(); });

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
