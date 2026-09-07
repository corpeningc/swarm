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

// ---- font scale ----
// Ctrl +/- resizes every terminal, like a normal terminal emulator. The choice
// is per-machine and outlives a restart.
const FONT_MIN = 8, FONT_MAX = 32, FONT_DEFAULT = 13;
let termFontSize = clampFont(Number(localStorage.getItem("swarm.fontSize")) || FONT_DEFAULT);

function clampFont(px) {
  return Number.isFinite(px) ? Math.min(FONT_MAX, Math.max(FONT_MIN, Math.round(px))) : FONT_DEFAULT;
}

function setFontSize(px) {
  const next = clampFont(px);
  if (next === termFontSize) return;
  termFontSize = next;
  TERM_OPTS.fontSize = next; // terminals created later inherit it
  try { localStorage.setItem("swarm.fontSize", String(next)); } catch (_) {}
  for (const map of [terms, shellTerms]) {
    for (const entry of map.values()) entry.term.options.fontSize = next;
  }
  fitVisible(); // the cell grid changed, so every pane re-fits and resizes its PTY
}

// ---- terminal factory ----
const TERM_OPTS = {
  fontFamily: '"Cascadia Code", "JetBrains Mono", Consolas, monospace',
  fontSize: termFontSize,
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

function renderSidebar() {
  listEl.innerHTML = "";
  for (const s of sessions) {
    const li = document.createElement("li");
    li.className = "session" + (s.id === focusedId ? " focused" : "");
    li.dataset.id = s.id;
    li.innerHTML = `
      <div class="row1">
        <span class="dot ${s.status}"></span>
        <span class="label">${escapeHtml(s.label)}</span>
      </div>
      <div class="meta">${escapeHtml(s.agentName || "claude")} · ${escapeHtml(s.branch || "")}${s.inPlace ? ' <span class="tag">in-place</span>' : ""} ${s.live ? "" : "· (stopped)"}</div>`;
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
  renderSidebar();
  orderPanes();
  await App?.ReorderSessions(order);
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

// attach routes the keyboard to whatever the current tab shows: the Shell tab
// attaches to that session's shell, every other tab to its agent (switching to
// the Agent tab first). Resumes a stopped agent; a shell needs no agent, so
// attaching to one never resumes. Mirrors the TUI's ModeAttached; Ctrl+Q detaches.
function attach() {
  if (!focusedId) return;
  if (view === "shell") {
    openShell(focusedId);
    attached = true;
    document.body.classList.add("attached");
    ensureShellTerm(focusedId).term.focus();
    updateModeHint();
    return;
  }
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
  for (const map of [terms, shellTerms]) {
    const entry = map.get(focusedId);
    if (entry) entry.term.blur();
  }
  if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
  updateModeHint();
}

function updateModeHint() {
  const hint = $("#mode-hint");
  if (!focusedId) { hint.textContent = ""; hint.classList.remove("attached"); return; }
  const target = view === "shell" ? " to shell" : "";
  if (attached) {
    hint.textContent = "● attached" + target + " — Ctrl+Q to detach";
    hint.classList.add("attached");
  } else {
    hint.textContent = "navigation — ↵ to attach" + target;
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
  // An attachment points at one pane, so changing tabs has to move it — or
  // drop it, since the Diff tab has nothing to type into.
  if (attached) {
    if (v === "diff") detach();
    else if (focusedId) (v === "shell" ? ensureShellTerm(focusedId) : ensureTerm(focusedId)).term.focus();
  }
  updateModeHint();
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
  // Clicking the shell pane attaches to it, matching the agent panes.
  term.element?.addEventListener("mousedown", () => { focusSession(id); attach(); });
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
  sessions = (await App.ListSessions()) || [];
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
  // An in-place session's "worktree" is the repo itself — never offer to
  // delete it (the backend refuses too).
  const inPlace = !!sessions.find((x) => x.id === focusedId)?.inPlace;
  $("#d-worktree-opts").classList.toggle("hidden", inPlace);
  $("#d-inplace-note").classList.toggle("hidden", !inPlace);
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
  setModalBusy(false);
  await refreshWorkspaces();
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
// A spawn can take minutes (`git worktree add` on a big repo), so the form is
// frozen while it runs: edits made mid-spawn were silently dropped, and Enter
// twice would fire a second spawn.
let spawning = false;
function setModalBusy(busy) {
  spawning = busy;
  for (const el of $("#modal").querySelectorAll("input, select, textarea, button")) el.disabled = busy;
  $("#modal").classList.toggle("busy", busy);
}

function closeModal() {
  if (spawning) return;
  backdrop.classList.add("hidden");
}

// Workspace values mirror the Go side: "" makes a fresh worktree, "@main" runs
// in the repository itself, anything else is an existing worktree's slug.
const WS_NEW = "", WS_MAIN = "@main";

function addOption(sel, value, text) {
  const opt = document.createElement("option");
  opt.value = value; opt.textContent = text;
  sel.appendChild(opt);
  return opt;
}

// refreshWorkspaces rebuilds the picker for whatever repo is typed in, keeping
// the current choice when it still exists.
async function refreshWorkspaces() {
  const sel = $("#m-workspace");
  const prev = sel.value;
  sel.innerHTML = "";
  addOption(sel, WS_NEW, "New worktree — isolated copy on its own branch");
  let list = [];
  try { list = (await App?.Workspaces($("#m-repo").value.trim())) || []; } catch (_) {}
  for (const w of list) {
    const label = w.value === WS_MAIN
      ? "Main working tree — run here, on the current branch"
      : `Existing worktree — ${w.label}`;
    addOption(sel, w.value, label + (w.inUse ? " (in use)" : "")).disabled = w.inUse;
  }
  sel.value = [...sel.options].some((o) => o.value === prev) ? prev : WS_NEW;
  await syncWorkspace();
}

// syncWorkspace reflects the chosen workspace into the rest of the form: the
// name field (an existing worktree's slug *is* its session name) and the list
// of conversations recorded there.
async function syncWorkspace() {
  const ws = $("#m-workspace").value;
  const note = $("#m-workspace-note");
  const nameEl = $("#m-name");
  if (ws === WS_MAIN) {
    note.textContent = "The agent edits the repository directly. Nothing is isolated, and discarding never deletes anything.";
  } else if (ws !== WS_NEW) {
    note.textContent = "Reattaches to that worktree and its branch — nothing new is created.";
  }
  note.classList.toggle("hidden", ws === WS_NEW);
  $("#m-name-label").textContent = ws === WS_NEW ? "Name / branch" : "Name";
  if (ws !== WS_NEW && ws !== WS_MAIN) {
    nameEl.value = ws;
    nameEl.disabled = true;
  } else {
    if (nameEl.disabled) nameEl.value = "";
    nameEl.disabled = false;
  }
  await refreshConversations();
}

// Conversations are keyed by working directory, so only an existing workspace
// has any — a worktree that doesn't exist yet has no history to continue.
async function refreshConversations() {
  const sel = $("#m-convo");
  sel.innerHTML = "";
  addOption(sel, "", "New conversation");
  let list = [];
  try {
    list = (await App?.Conversations($("#m-repo").value.trim(), $("#m-workspace").value)) || [];
  } catch (_) {}
  for (const c of list) addOption(sel, c.id, `${c.summary || c.id.slice(0, 8)} — ${timeAgo(c.updatedAt)}`);
  sel.value = "";
  $("#m-convo-row").classList.toggle("hidden", list.length === 0);
}

function timeAgo(iso) {
  const mins = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 60000));
  if (mins < 60) return `${mins}m ago`;
  if (mins < 60 * 24) return `${Math.round(mins / 60)}h ago`;
  return `${Math.round(mins / (60 * 24))}d ago`;
}

async function browseForRepo() {
  try {
    const p = await App.BrowseForRepo();
    if (p) { $("#m-repo").value = p; await refreshWorkspaces(); }
  } catch (e) {
    $("#modal-err").textContent = String(e);
  }
}

async function spawn() {
  if (spawning) return;
  const repo = $("#m-repo").value.trim();
  if (!repo) { $("#modal-err").textContent = "repository path is required"; return; }
  const btn = $("#m-spawn");
  setModalBusy(true);
  btn.textContent = "Spawning…";
  $("#modal-err").textContent = "";
  try {
    const dto = await App.SpawnSession(
      repo, $("#m-prompt").value, $("#m-name").value.trim(), $("#m-agent").value,
      $("#m-workspace").value, $("#m-convo").value, $("#m-mcp").checked
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
    setModalBusy(false);
    btn.textContent = "Spawn";
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
  // Terminal zoom works in both modes, so it comes before the attached
  // short-circuit. stopPropagation keeps xterm from also seeing the chord.
  if (ev.ctrlKey && !ev.altKey && !ev.metaKey) {
    const zoom = { "+": 1, "=": 1, "-": -1, "_": -1, "0": 0 }[ev.key];
    if (zoom !== undefined) {
      ev.preventDefault(); ev.stopPropagation();
      setFontSize(zoom === 0 ? FONT_DEFAULT : termFontSize + zoom);
      return;
    }
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
$("#m-workspace").addEventListener("change", syncWorkspace);
$("#m-repo").addEventListener("change", refreshWorkspaces);
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

// ---- update check ----
// Swarm ships unsigned with no auto-updater, so the app asks GitHub once at
// startup whether a newer release exists. A failed check (offline, rate
// limited) stays silent — it is never worth a visible error.
const SKIP_UPDATE_KEY = "swarm.skipUpdate";
let pendingUpdate = null;

async function checkForUpdate() {
  let info;
  try {
    info = await App?.CheckUpdate();
  } catch (_) {
    return;
  }
  if (!info?.available) return;
  let skipped = null;
  try { skipped = localStorage.getItem(SKIP_UPDATE_KEY); } catch (_) {}
  if (skipped === info.latest) return;
  pendingUpdate = info;
  $("#update-text").innerHTML =
    `swarm <b>v${escapeHtml(info.latest)}</b> is available — you're on v${escapeHtml(info.current)}.`;
  $("#update-banner").classList.remove("hidden");
  fitVisible(); // the banner steals height from the terminal
}

$("#update-get").addEventListener("click", () => {
  if (pendingUpdate) App?.OpenReleasePage(pendingUpdate.url).catch(() => {});
});
$("#update-dismiss").addEventListener("click", () => {
  try { localStorage.setItem(SKIP_UPDATE_KEY, pendingUpdate?.latest || ""); } catch (_) {}
  $("#update-banner").classList.add("hidden");
  fitVisible();
});

// ---- boot ----
refreshSessions().then(() => { fitVisible(); updateModeHint(); });
checkForUpdate();

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
