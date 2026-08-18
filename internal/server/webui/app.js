"use strict";

const $ = (sel) => document.querySelector(sel);
const messagesEl = $("#messages");
const sessionListEl = $("#session-list");
const inputEl = $("#input");
const sendBtn = $("#btn-send");

let currentSession = null; // 当前 session id
let busy = false;          // 正在等待回复
let chatAbort = null;      // 进行中对话的中断控制器，非 null 表示可停止

// 发送按钮在等待回复期间变为「停止」，点击可中断本轮生成。
// 两个图标都在 DOM 里，由 .stop 决定显示哪个，因此这里只需切类。
function setSending(on) {
  busy = on;
  sendBtn.classList.toggle("stop", on);
  sendBtn.title = on ? "停止生成" : "发送";
  sendBtn.setAttribute("aria-label", sendBtn.title);
}

// ---------- 主题（三段滑块：跟随系统 / 浅色 / 深色） ----------

const themeSeg = $("#theme-seg");
const darkMedia = window.matchMedia("(prefers-color-scheme: dark)");
let themeSetting = localStorage.getItem("wen-theme") || "system";

function applyTheme() {
  const dark = themeSetting === "dark" || (themeSetting === "system" && darkMedia.matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  themeSeg.dataset.active = themeSetting; // 滑块指示位置由 CSS 按此属性移动
  for (const btn of themeSeg.querySelectorAll(".theme-seg-btn")) {
    btn.classList.toggle("active", btn.dataset.themeOpt === themeSetting);
  }
}

themeSeg.addEventListener("click", (e) => {
  const btn = e.target.closest(".theme-seg-btn");
  if (!btn) return;
  themeSetting = btn.dataset.themeOpt;
  localStorage.setItem("wen-theme", themeSetting);
  applyTheme();
});
darkMedia.addEventListener("change", () => {
  if (themeSetting === "system") applyTheme();
});
applyTheme();

// ---------- 通用设置：聊天栏宽度 ----------
// 与主题同一套做法：JS 只写 data-chat-width 标记，三档的具体宽度写在 style.css 里，
// 免得同一组数值散在两处。首屏前由 index.html 的内联脚本先套上，避免布局跳动。

const chatWidthSeg = $("#chat-width-seg");
const chatWidthDesc = $("#chat-width-desc");

function applyChatWidth(name) {
  document.documentElement.dataset.chatWidth = name;
  localStorage.setItem("wen-chat-width", name);
  for (const btn of chatWidthSeg.querySelectorAll(".seg-btn")) {
    btn.classList.toggle("active", btn.dataset.width === name);
  }
  // 实际宽度取自计算样式而不是 JS 里的常量，改 CSS 就会跟着变
  const px = getComputedStyle(document.documentElement).getPropertyValue("--chat-width").trim();
  chatWidthDesc.textContent = `消息与输入框所占的栏宽，窗口不够宽时自动收窄。当前 ${px}。`;
}

chatWidthSeg.addEventListener("click", (e) => {
  const btn = e.target.closest(".seg-btn");
  if (btn) applyChatWidth(btn.dataset.width);
});
applyChatWidth(localStorage.getItem("wen-chat-width") || "medium");

// ---------- 输入框工具条上的弹出菜单 ----------
// 筛选与模型切换共用一套开合逻辑：同时最多开一个，点击别处或按 Esc 关闭。

const openPopups = new Set();

function closePopups(except) {
  for (const p of openPopups) {
    if (p.menu === except) continue;
    p.menu.classList.add("hidden");
    p.chip.classList.remove("open");
    openPopups.delete(p);
  }
}

function togglePopup(chip, menu, build) {
  const willOpen = menu.classList.contains("hidden");
  closePopups(willOpen ? menu : null);
  if (!willOpen) {
    menu.classList.add("hidden");
    chip.classList.remove("open");
    return;
  }
  build();
  menu.classList.remove("hidden");
  chip.classList.add("open");
  openPopups.add({ chip, menu });
}

document.addEventListener("click", (e) => {
  if (!e.target.closest(".composer-anchor")) closePopups(null);
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closePopups(null);
});

const checkIconSVG =
  '<svg class="menu-check" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" ' +
  'stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>';
const blankIconSVG = '<span class="menu-check"></span>';
const arrowIconSVG =
  '<svg class="menu-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ' +
  'stroke-linecap="round" stroke-linejoin="round"><path d="M9 18l6-6-6-6"/></svg>';

// ---------- 显示内容筛选 ----------
// 纯展示开关：加在消息区上的类由 CSS 决定藏什么，因此对历史与正在流式输出的
// 内容一视同仁，切换时不需要重新渲染，也不会跟轮询的指纹比对打架。
// 唯一的例外是「提示词」——它还要让服务端开始采集，见 sendMessage。

const FILTERS = [
  { key: "prompt", label: "发给模型的提示词", note: "调试用" },
  { key: "tools", label: "工具调用" },
  { key: "thinking", label: "思考过程" },
  { key: "heartbeat", label: "心跳唤醒" },
];
const FILTER_DEFAULTS = { prompt: false, tools: true, thinking: true, heartbeat: true };

const filterChip = $("#btn-filter");
const filterMenu = $("#filter-menu");
let filters = loadFilters();

function loadFilters() {
  const saved = { ...FILTER_DEFAULTS };
  try {
    Object.assign(saved, JSON.parse(localStorage.getItem("wen-filters") || "{}"));
  } catch {
    // 存档损坏时退回默认值，不值得为此打断使用
  }
  return saved;
}

function applyFilters() {
  for (const f of FILTERS) {
    messagesEl.classList.toggle("hide-" + f.key, !filters[f.key]);
  }
  // 有项被关掉时点亮入口，否则「东西藏起来了」这件事本身看不出来
  const changed = FILTERS.some((f) => filters[f.key] !== FILTER_DEFAULTS[f.key]);
  filterChip.classList.toggle("filtered", changed);
  localStorage.setItem("wen-filters", JSON.stringify(filters));
}

function buildFilterMenu() {
  filterMenu.textContent = "";
  const title = document.createElement("div");
  title.className = "popup-title";
  title.textContent = "聊天区显示以下内容";
  filterMenu.appendChild(title);

  for (const f of FILTERS) {
    const item = document.createElement("button");
    item.type = "button";
    item.className = "menu-item";
    item.innerHTML = filters[f.key] ? checkIconSVG : blankIconSVG;
    const label = document.createElement("span");
    label.className = "menu-grow";
    label.textContent = f.label;
    item.appendChild(label);
    if (f.note) {
      const note = document.createElement("span");
      note.className = "menu-note";
      note.textContent = f.note;
      item.appendChild(note);
    }
    item.addEventListener("click", () => {
      filters[f.key] = !filters[f.key];
      applyFilters();
      buildFilterMenu(); // 就地重建，菜单保持展开便于连续勾选
    });
    filterMenu.appendChild(item);
  }
}

filterChip.addEventListener("click", () => togglePopup(filterChip, filterMenu, buildFilterMenu));
applyFilters();

// ---------- 模型快捷切换 ----------
// 与设置页的模型配置互不相干：这里只读提供商与模型清单、只写「当前选中项」，
// 各自持有自己的一份数据，免得两个视图共用一份状态互相踩。

const modelChip = $("#btn-model");
const modelMenu = $("#model-menu");
const modelLabel = $("#model-label");
let switcherDoc = null; // 快捷切换用的 /api/models 副本
let subTimer = null;

async function loadModelSwitcher() {
  try {
    switcherDoc = await fetch("/api/models").then((r) => r.json());
    renderModelLabel();
  } catch {
    modelLabel.textContent = "模型不可用";
  }
}

function renderModelLabel() {
  const cur = (switcherDoc && switcherDoc.current) || {};
  if (!cur.provider) {
    modelLabel.textContent = "未选择模型";
    modelChip.title = "切换当前使用的模型";
    return;
  }
  const p = switcherDoc.providers.find((x) => x.name === cur.provider);
  const m = p && (p.models || []).find((x) => x.id === cur.model);
  const text = cur.provider + " / " + ((m && m.name) || cur.model || "—");
  modelLabel.textContent = text;
  modelChip.title = text + "（点击切换）";
}

function buildModelMenu() {
  modelMenu.textContent = "";
  if (!switcherDoc) {
    const tip = document.createElement("div");
    tip.className = "popup-title";
    tip.textContent = "模型清单加载中…";
    modelMenu.appendChild(tip);
    loadModelSwitcher().then(() => {
      if (!modelMenu.classList.contains("hidden")) buildModelMenu();
    });
    return;
  }
  const cur = switcherDoc.current || {};
  const title = document.createElement("div");
  title.className = "popup-title";
  title.textContent = "切换模型";
  modelMenu.appendChild(title);

  for (const p of switcherDoc.providers) {
    const row = document.createElement("div");
    row.className = "menu-row";

    const item = document.createElement("button");
    item.type = "button";
    item.className = "menu-item";
    item.innerHTML = p.name === cur.provider ? checkIconSVG : blankIconSVG;
    const name = document.createElement("span");
    name.className = "menu-grow";
    name.textContent = p.name;
    item.appendChild(name);

    // 不可用的提供商置灰保留而非隐藏——不然会以为配置丢了
    const models = p.models || [];
    const reason = models.length === 0 ? "无模型" : !p.has_api_key ? "未配置密钥" : "";
    if (reason) {
      item.disabled = true;
      const note = document.createElement("span");
      note.className = "menu-note";
      note.textContent = reason;
      item.appendChild(note);
    } else {
      item.insertAdjacentHTML("beforeend", arrowIconSVG);
    }
    row.appendChild(item);

    if (!reason) {
      const sub = buildModelSubmenu(p, cur);
      sub.classList.add("hidden");
      row.appendChild(sub);
      const open = () => {
        clearTimeout(subTimer);
        for (const s of modelMenu.querySelectorAll(".menu-sub")) s.classList.add("hidden");
        sub.classList.remove("hidden");
        // 右侧放不下时翻到左边展开
        sub.classList.toggle("flip", sub.getBoundingClientRect().right > window.innerWidth - 8);
      };
      // 悬停展开，离开留一点余量：鼠标斜着移向子菜单时不该被判成「离开」
      row.addEventListener("mouseenter", open);
      row.addEventListener("mouseleave", () => {
        subTimer = setTimeout(() => sub.classList.add("hidden"), 220);
      });
      item.addEventListener("click", open); // 触摸设备没有悬停，点击也能展开
    }
    modelMenu.appendChild(row);
  }
}

function buildModelSubmenu(p, cur) {
  const sub = document.createElement("div");
  sub.className = "menu-sub";
  for (const m of p.models || []) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "menu-item";
    const active = p.name === cur.provider && m.id === cur.model;
    btn.innerHTML = active ? checkIconSVG : blankIconSVG;
    const label = document.createElement("span");
    label.className = "menu-grow";
    label.textContent = m.name || m.id;
    btn.appendChild(label);
    btn.title = m.id;
    btn.addEventListener("click", () => quickSwitchModel(p.name, m.id));
    sub.appendChild(btn);
  }
  return sub;
}

// 名字不能叫 switchModel：设置页的模型配置那节已经有一个同名函数，
// 而函数声明同名时后写的会覆盖先写的，两处都会调到设置页那个版本。
async function quickSwitchModel(provider, model) {
  const prev = modelLabel.textContent;
  modelLabel.textContent = "切换中…";
  closePopups(null);
  try {
    const res = await fetch("/api/models/current", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ provider, model }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || "HTTP " + res.status);
    }
    switcherDoc = await res.json();
    renderModelLabel();
  } catch (e) {
    modelLabel.textContent = prev;
    addError("切换模型失败：" + e.message);
  }
}

modelChip.addEventListener("click", () => togglePopup(modelChip, modelMenu, buildModelMenu));
loadModelSwitcher();

// ---------- Markdown 渲染 ----------

marked.setOptions({ gfm: true, breaks: true });

function renderMarkdown(el, text) {
  el.innerHTML = DOMPurify.sanitize(marked.parse(text || ""));
  for (const a of el.querySelectorAll("a")) {
    a.target = "_blank";
    a.rel = "noopener noreferrer";
  }
}

// ---------- Session 列表 ----------

async function loadSessions() {
  const res = await fetch("/api/sessions");
  const metas = await res.json();
  sessionListEl.innerHTML = "";
  for (const m of metas) {
    const li = document.createElement("li");
    li.className = "session-item" + (m.id === currentSession ? " active" : "");
    li.dataset.id = m.id;

    const title = document.createElement("span");
    title.className = "title";
    title.textContent = m.title || "（新会话）";
    li.appendChild(title);

    const del = document.createElement("button");
    del.className = "btn-del";
    del.textContent = "✕";
    del.title = "删除会话";
    del.addEventListener("click", async (e) => {
      e.stopPropagation();
      if (!confirm("删除该会话？")) return;
      await fetch(`/api/sessions/${m.id}`, { method: "DELETE" });
      if (currentSession === m.id) {
        currentSession = null;
        clearMessages();
      }
      loadSessions();
    });
    li.appendChild(del);

    li.addEventListener("click", () => selectSession(m.id));
    sessionListEl.appendChild(li);
  }
}

async function newSession() {
  const res = await fetch("/api/sessions", { method: "POST" });
  const meta = await res.json();
  currentSession = meta.id;
  clearMessages();
  renderedFp = historyFp([]);
  await loadSessions();
  inputEl.focus();
  return meta.id;
}

async function selectSession(id) {
  if (busy) return;
  currentSession = id;
  const res = await fetch(`/api/sessions/${id}`);
  if (!res.ok) { await loadSessions(); return; }
  const data = await res.json();
  renderHistory(data.messages);
  renderedFp = historyFp(data.messages);
  loadSessions();
}

// ---------- 当前会话的后台同步 ----------
// QQ 远程对话、心跳、定时任务都会往会话里写消息，而消息区只在选中时渲染一次。
// 用「条数 + 末条时间戳」做指纹，变了才整体重渲染，避免无谓的闪动。

let renderedFp = ""; // 消息区当前渲染内容对应的指纹

function historyFp(msgs) {
  const last = msgs[msgs.length - 1];
  return msgs.length + "|" + (last ? last.ts : "");
}

async function syncCurrentSession() {
  if (!currentSession || busy) return;
  try {
    const res = await fetch(`/api/sessions/${currentSession}`);
    if (!res.ok) return;
    const data = await res.json();
    const fp = historyFp(data.messages);
    if (fp === renderedFp) return;
    renderHistory(data.messages);
    renderedFp = fp;
    scrollBottom();
  } catch { /* 网络抖动下次再试 */ }
}

// 静默校准指纹（内容已经在流式过程中渲染到界面上，不需要重画）
async function refreshFp() {
  if (!currentSession) return;
  try {
    const res = await fetch(`/api/sessions/${currentSession}`);
    if (res.ok) renderedFp = historyFp((await res.json()).messages);
  } catch { /* 忽略 */ }
}

setInterval(syncCurrentSession, 5000);

// ---------- 消息渲染 ----------

function clearMessages() {
  messagesEl.innerHTML = '<div class="empty-hint" id="empty-hint">新建或选择一个会话开始对话</div>';
}

function hideHint() {
  const hint = $("#empty-hint");
  if (hint) hint.remove();
}

function scrollBottom() {
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

function addBubble(role, text) {
  hideHint();
  const wrap = document.createElement("div");
  wrap.className = `msg ${role}`;
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  if (role === "assistant") {
    bubble.classList.add("md");
    renderMarkdown(bubble, text);
  } else {
    bubble.textContent = text;
  }
  wrap.appendChild(bubble);
  messagesEl.appendChild(wrap);
  scrollBottom();
  return bubble;
}

// kind 供显示内容筛选按块过滤；不传表示不受筛选影响（如「已停止生成」这类提示）
function addSysBlock(text, kind) {
  hideHint();
  const div = document.createElement("div");
  div.className = "sys-block";
  if (kind) div.dataset.kind = kind;
  div.textContent = text;
  messagesEl.appendChild(div);
  scrollBottom();
  return div;
}

function addSummaryBlock(text) {
  hideHint();
  const details = document.createElement("details");
  details.className = "thinking-block";
  const summary = document.createElement("summary");
  summary.textContent = "📦 历史摘要（已压缩）";
  details.appendChild(summary);
  const div = document.createElement("div");
  div.className = "thinking-content";
  div.textContent = text;
  details.appendChild(div);
  messagesEl.appendChild(details);
  scrollBottom();
  return details;
}

function addThinkingBlock(text, open) {
  hideHint();
  const details = document.createElement("details");
  details.className = "thinking-block";
  details.dataset.kind = "thinking";
  if (open) details.open = true;
  const summary = document.createElement("summary");
  summary.textContent = "🧠 思考过程";
  details.appendChild(summary);
  const div = document.createElement("div");
  div.className = "thinking-content";
  div.textContent = text;
  details.appendChild(div);
  messagesEl.appendChild(details);
  scrollBottom();
  return details;
}

function addToolBlock(name, args, result) {
  hideHint();
  const details = document.createElement("details");
  details.className = "tool-block";
  details.dataset.kind = "tool";

  const summary = document.createElement("summary");
  summary.innerHTML = `🔧 调用工具 <span class="tool-name"></span>`;
  summary.querySelector(".tool-name").textContent = name;
  details.appendChild(summary);

  const detail = document.createElement("div");
  detail.className = "tool-detail";
  setToolDetail(detail, args, result);
  details.appendChild(detail);

  messagesEl.appendChild(details);
  scrollBottom();
  return details;
}

function setToolDetail(detailEl, args, result) {
  detailEl.textContent = "";
  const argsLabel = document.createElement("span");
  argsLabel.className = "label";
  argsLabel.textContent = "参数: ";
  detailEl.appendChild(argsLabel);
  detailEl.appendChild(document.createTextNode(formatArgs(args) + "\n"));
  if (result !== undefined && result !== null) {
    const resLabel = document.createElement("span");
    resLabel.className = "label";
    resLabel.textContent = "结果: ";
    detailEl.appendChild(resLabel);
    detailEl.appendChild(document.createTextNode(result));
  } else {
    const running = document.createElement("span");
    running.className = "label";
    running.textContent = "执行中…";
    detailEl.appendChild(running);
  }
}

// addPromptBlock 展示本次调用实际提交给模型的请求体。内容可能有几十万字，
// 因此默认折叠且延迟到展开那一刻才格式化并写进 DOM——每轮都渲染一遍会明显卡顿。
function addPromptBlock(payload) {
  hideHint();
  const details = document.createElement("details");
  details.className = "prompt-block";
  details.dataset.kind = "prompt";

  const text = JSON.stringify(payload, null, 2);
  const summary = document.createElement("summary");
  summary.textContent = "📤 发给模型的提示词 ";
  const size = document.createElement("span");
  size.className = "prompt-size";
  const msgCount = (payload && payload.messages && payload.messages.length) || 0;
  size.textContent = `（${msgCount} 条消息，${formatSize(text.length)}）`;
  summary.appendChild(size);
  details.appendChild(summary);

  const content = document.createElement("div");
  content.className = "prompt-content";
  details.appendChild(content);

  let filled = false;
  details.addEventListener("toggle", () => {
    if (!details.open || filled) return;
    filled = true;
    content.textContent = text;
  });

  messagesEl.appendChild(details);
  scrollBottom();
  return details;
}

function formatSize(chars) {
  if (chars < 1000) return chars + " 字符";
  if (chars < 1000000) return (chars / 1000).toFixed(1) + "k 字符";
  return (chars / 1000000).toFixed(2) + "M 字符";
}

function formatArgs(args) {
  if (args == null) return "{}";
  try {
    return JSON.stringify(typeof args === "string" ? JSON.parse(args) : args);
  } catch {
    return String(args);
  }
}

function addError(text) {
  hideHint();
  const div = document.createElement("div");
  div.className = "msg-error";
  div.textContent = text;
  messagesEl.appendChild(div);
  scrollBottom();
}

// addConfirmBlock 渲染一次操作确认请求。这一轮对话正阻塞在这里等回答，
// 因此不设默认动作：必须显式点一个按钮，或让它超时（按拒绝处理）。
function addConfirmBlock(ev) {
  hideHint();
  const box = document.createElement("div");
  box.className = "confirm-block";

  const head = document.createElement("div");
  head.className = "confirm-head";
  head.textContent = `⚠️ ${ev.title || "需要确认"}`;
  box.appendChild(head);

  if (ev.reason) {
    const reason = document.createElement("div");
    reason.className = "confirm-reason";
    reason.textContent = ev.reason;
    box.appendChild(reason);
  }

  const detail = document.createElement("pre");
  detail.className = "confirm-detail";
  detail.textContent = ev.detail || "";
  box.appendChild(detail);

  const actions = document.createElement("div");
  actions.className = "confirm-actions";
  const status = document.createElement("span");
  status.className = "confirm-status";
  const deny = document.createElement("button");
  deny.type = "button";
  deny.className = "btn-ghost";
  deny.textContent = "拒绝";
  const allow = document.createElement("button");
  allow.type = "button";
  allow.className = "btn-danger";
  allow.textContent = "允许执行";

  const settle = (approved, note) => {
    allow.remove();
    deny.remove();
    box.classList.add(approved ? "confirm-allowed" : "confirm-denied");
    status.textContent = note;
  };
  const answer = async (approved) => {
    allow.disabled = true;
    deny.disabled = true;
    try {
      const res = await fetch("/api/confirmations/" + encodeURIComponent(ev.id), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ approved }),
      });
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err.error || "HTTP " + res.status);
      }
      // 定稿交给 confirm_done 事件统一处理，避免两处各写一遍
    } catch (e) {
      allow.disabled = false;
      deny.disabled = false;
      status.textContent = "提交失败：" + e.message;
    }
  };
  deny.addEventListener("click", () => answer(false));
  allow.addEventListener("click", () => answer(true));

  actions.append(status, deny, allow);
  box.appendChild(actions);
  messagesEl.appendChild(box);
  scrollBottom();
  return { el: box, settle };
}

function renderHistory(messages) {
  clearMessages();
  if (!messages || messages.length === 0) return;
  hideHint();
  const toolBlocks = {}; // tool_call id -> {el, args}
  for (const m of messages) {
    if (m.kind === "summary") {
      addSummaryBlock(m.content);
    } else if (m.kind === "notice") {
      // 会话注记：只给人看的一行，模型从来看不到它
      addSysBlock(m.content, "notice");
    } else if (m.role === "user" && (m.kind === "ephemeral" || m.origin === "heartbeat")) {
      // 机器注入的一次性输入：不渲染成用户气泡，只留一行来源提示。
      // origin=heartbeat 的兜底覆盖标记机制上线前落盘的旧心跳。
      addSysBlock(
        m.origin === "heartbeat" ? "💓 心跳唤醒" : "⏱ 后台唤醒（" + (m.origin || "系统") + "）",
        m.origin === "heartbeat" ? "heartbeat" : "");
    } else if (m.role === "user") {
      addBubble("user", m.content);
    } else if (m.role === "assistant") {
      if (m.reasoning_content) addThinkingBlock(m.reasoning_content, false);
      if (m.content) addBubble("assistant", m.content);
      for (const tc of m.tool_calls || []) {
        toolBlocks[tc.id] = { el: addToolBlock(tc.name, tc.arguments), args: tc.arguments };
      }
    } else if (m.role === "tool") {
      const tb = toolBlocks[m.tool_call_id];
      if (tb) setToolDetail(tb.el.querySelector(".tool-detail"), tb.args, m.content);
    }
  }
}

// ---------- 发送消息 + SSE 流 ----------

async function sendMessage() {
  const text = inputEl.value.trim();
  if (!text || busy) return;
  if (text.startsWith("/")) {
    await handleCommand(text);
    return;
  }
  if (!currentSession) await newSession();

  chatAbort = new AbortController();
  setSending(true);
  inputEl.value = "";
  autoGrow();
  addBubble("user", text);

  let aborted = false; // 用户点了停止，结束后按落盘内容重载历史

  let assistantBubble = null; // 惰性创建，收到第一个 delta 才建
  let assistantRaw = "";      // 当前气泡的原始 Markdown 文本
  let thinkingBlock = null;   // 当前轮的思考块
  let compactBlock = null;    // 自动压缩的动态展示块
  let autoCompacted = false;  // 本轮发生过自动压缩，结束后需重载历史
  const toolBlocks = {};
  const confirmBlocks = {};
  const finishBubble = () => {
    if (assistantBubble) assistantBubble.classList.remove("streaming");
    assistantBubble = null;
    assistantRaw = "";
  };
  const finishThinking = () => {
    if (thinkingBlock) thinkingBlock.open = false;
    thinkingBlock = null;
  };

  try {
    const res = await fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // 提示词采集由界面开关驱动：关着时服务端不组装也不下发，
      // 代价是它只对开启之后的轮次生效，回溯不了之前的
      body: JSON.stringify({
        session_id: currentSession,
        message: text,
        debug_prompt: !!filters.prompt,
      }),
      signal: chatAbort.signal,
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || `HTTP ${res.status}`);
    }

    for await (const ev of sseEvents(res.body)) {
      if (ev.type === "prompt") {
        finishBubble();   // 属于下一次调用，先给上一段文本定稿
        finishThinking();
        addPromptBlock(ev.prompt);
      } else if (ev.type === "thinking") {
        if (!thinkingBlock) thinkingBlock = addThinkingBlock("", true);
        thinkingBlock.querySelector(".thinking-content").textContent += ev.content || "";
        scrollBottom();
      } else if (ev.type === "delta") {
        if (thinkingBlock) thinkingBlock.open = false; // 正文开始，折叠思考
        if (!assistantBubble) {
          assistantBubble = addBubble("assistant", "");
          assistantBubble.classList.add("streaming");
        }
        assistantRaw += ev.content || "";
        renderMarkdown(assistantBubble, assistantRaw);
        scrollBottom();
      } else if (ev.type === "tool_start") {
        finishBubble();   // 工具前的文本气泡定稿
        finishThinking(); // 本轮思考结束，下一轮会新建
        toolBlocks[ev.tool_call_id] = { el: addToolBlock(ev.tool_name, ev.tool_args), args: ev.tool_args };
      } else if (ev.type === "tool_result") {
        const tb = toolBlocks[ev.tool_call_id];
        if (tb) setToolDetail(tb.el.querySelector(".tool-detail"), tb.args, ev.tool_result);
      } else if (ev.type === "confirm_request") {
        finishBubble();   // 等待期间正文气泡先定稿
        finishThinking();
        confirmBlocks[ev.id] = addConfirmBlock(ev);
      } else if (ev.type === "confirm_done") {
        const cb = confirmBlocks[ev.id];
        if (cb) {
          cb.settle(ev.approved,
            ev.approved ? "已允许" : ev.expired ? "已超时，按拒绝处理" : "已拒绝");
        }
      } else if (ev.type === "compact_start") {
        finishBubble();
        finishThinking();
        compactBlock = document.createElement("details");
        compactBlock.className = "thinking-block";
        compactBlock.open = true;
        const cs = document.createElement("summary");
        cs.textContent = "📦 上下文接近上限，正在自动压缩会话…";
        compactBlock.appendChild(cs);
        const cc = document.createElement("div");
        cc.className = "thinking-content";
        compactBlock.appendChild(cc);
        messagesEl.appendChild(compactBlock);
        scrollBottom();
      } else if (ev.type === "compact_delta") {
        if (compactBlock) {
          compactBlock.querySelector(".thinking-content").textContent += ev.content || "";
          scrollBottom();
        }
      } else if (ev.type === "compact_done") {
        if (ev.error) {
          addError("自动压缩失败：" + ev.error);
        } else {
          autoCompacted = true; // 流结束后重载历史并提示
          if (compactBlock) compactBlock.open = false;
        }
        compactBlock = null;
      } else if (ev.type === "error") {
        addError("出错了：" + (ev.error || "未知错误"));
      } else if (ev.type === "done") {
        finishBubble();
        finishThinking();
      }
    }
  } catch (e) {
    if (e.name === "AbortError") {
      aborted = true; // 中断 SSE 即取消服务端本轮 ctx，生成随之终止
    } else {
      addError("请求失败：" + e.message);
    }
  } finally {
    finishBubble();
    finishThinking();
    setSending(false);
    chatAbort = null;
    if (aborted) {
      await selectSession(currentSession); // 半截生成不落盘，按磁盘内容重载对齐
      addSysBlock("⏹ 已停止生成");
    } else if (autoCompacted) {
      await selectSession(currentSession); // 重载压缩后的历史
      addSysBlock("✅ 会话已自动压缩");
    } else {
      refreshFp(); // 本轮内容已在流式过程中上屏，只校准指纹防止轮询重画
    }
    loadSessions(); // 刷新标题
    inputEl.focus();
  }
}

// ---------- 命令菜单（输入 / 时弹出，随输入筛选） ----------

const COMMANDS = [
  { cmd: "/status", desc: "显示 Agent 状态：模型、思考深度、上下文用量、会话 ID、插件状态" },
  { cmd: "/compact", desc: "压缩当前会话为摘要" },
];
const cmdMenu = $("#cmd-menu");
let cmdMatches = [];
let cmdIndex = -1;

function updateCmdMenu() {
  const text = inputEl.value;
  // 仅当整个输入是一个正在敲的命令词（以 / 开头、无空白）时才弹出
  if (!text.startsWith("/") || /\s/.test(text)) {
    hideCmdMenu();
    return;
  }
  cmdMatches = COMMANDS.filter((c) => c.cmd.startsWith(text.toLowerCase()));
  if (cmdMatches.length === 0) {
    hideCmdMenu();
    return;
  }
  cmdIndex = 0;
  cmdMenu.innerHTML = "";
  cmdMatches.forEach((c, i) => {
    const item = document.createElement("div");
    item.className = "cmd-item" + (i === cmdIndex ? " active" : "");
    const name = document.createElement("span");
    name.className = "cmd-name";
    name.textContent = c.cmd;
    const desc = document.createElement("span");
    desc.className = "cmd-desc";
    desc.textContent = c.desc;
    item.append(name, desc);
    // mousedown + preventDefault：避免输入框先失焦
    item.addEventListener("mousedown", (e) => {
      e.preventDefault();
      pickCommand(i);
    });
    cmdMenu.appendChild(item);
  });
  cmdMenu.classList.remove("hidden");
}

function hideCmdMenu() {
  cmdMenu.classList.add("hidden");
  cmdMatches = [];
  cmdIndex = -1;
}

function pickCommand(i) {
  inputEl.value = cmdMatches[i].cmd;
  hideCmdMenu();
  autoGrow();
  inputEl.focus();
}

function moveCmdSel(delta) {
  cmdIndex = (cmdIndex + delta + cmdMatches.length) % cmdMatches.length;
  [...cmdMenu.children].forEach((el, i) => el.classList.toggle("active", i === cmdIndex));
}

// ---------- 斜杠命令（本地处理，不进入对话历史） ----------

async function handleCommand(text) {
  inputEl.value = "";
  autoGrow();
  addBubble("user", text);
  const cmd = text.split(/\s+/)[0].toLowerCase();
  if (cmd === "/status") {
    await runStatus();
  } else if (cmd === "/compact") {
    await runCompact();
  } else {
    addSysBlock("⚠️ 未知命令：" + cmd + "\n可用命令：/status、/compact");
  }
  inputEl.focus();
}

async function runStatus() {
  try {
    const q = currentSession ? "?session_id=" + encodeURIComponent(currentSession) : "";
    const st = await fetch("/api/status" + q).then((r) => r.json());
    const lines = [
      st.version ? "📊 Wen Agent " + st.version : "📊 Agent 状态",
      "模型：" + st.provider + " / " + st.model,
      "思考深度：" + st.thinking,
      "上下文窗口：" + st.context_length.toLocaleString() + " tokens",
    ];
    if (st.session) {
      if (st.session.measured_tokens != null) {
        const pct = ((st.session.measured_tokens / st.context_length) * 100).toFixed(2);
        lines.push(
          "当前会话：" + st.session.message_count + " 条消息，实测 " +
          st.session.measured_tokens.toLocaleString() + " tokens（占用 " + pct + "%）"
        );
      } else {
        const pct = ((st.session.est_tokens / st.context_length) * 100).toFixed(2);
        lines.push(
          "当前会话：" + st.session.message_count + " 条消息，约 " +
          st.session.est_tokens.toLocaleString() + " tokens（估算，占用 " + pct + "%）"
        );
      }
      // 提示词缓存：字段只在本轮真的命中或写入过时才下发
      if (st.session.cached_tokens != null) {
        let s = "提示词缓存：命中 " + st.session.cached_tokens.toLocaleString();
        if (st.session.cache_write_tokens) s += " / 写入 " + st.session.cache_write_tokens.toLocaleString();
        s += " tokens";
        if (st.session.prompt_tokens) {
          s += "（占本轮输入 " +
            ((st.session.cached_tokens / st.session.prompt_tokens) * 100).toFixed(2) + "%）";
        }
        lines.push(s);
      }
      // 会话 ID 便于用 read_session / read_archive 定位这次对话
      lines.push("会话 ID：" + (st.session.id || currentSession));
    } else {
      lines.push("当前会话：无");
    }
    // 插件贡献的状态行（如心跳节奏），没有插件可报时字段不存在
    if (Array.isArray(st.plugin_lines)) lines.push(...st.plugin_lines);
    addSysBlock(lines.join("\n"));
  } catch (e) {
    addError("获取状态失败：" + e.message);
  }
}

async function runCompact() {
  if (!currentSession) {
    addSysBlock("⚠️ 没有活动会话，无法压缩");
    return;
  }
  busy = true;
  sendBtn.disabled = true;
  // 压缩过程的动态展示块：摘要内容实时流入
  const block = document.createElement("details");
  block.className = "thinking-block";
  block.open = true;
  const summary = document.createElement("summary");
  summary.textContent = "📦 正在压缩会话…";
  block.appendChild(summary);
  const contentEl = document.createElement("div");
  contentEl.className = "thinking-content";
  block.appendChild(contentEl);
  messagesEl.appendChild(block);
  scrollBottom();

  try {
    const res = await fetch(`/api/sessions/${currentSession}/compact`, { method: "POST" });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || `HTTP ${res.status}`);
    }
    let failed = false;
    for await (const ev of sseEvents(res.body)) {
      if (ev.type === "delta") {
        contentEl.textContent += ev.content || "";
        scrollBottom();
      } else if (ev.type === "error") {
        failed = true;
        addError("压缩失败：" + (ev.error || "未知错误"));
      }
    }
    if (!failed) {
      busy = false; // selectSession 在 busy 时不工作，先复位
      await selectSession(currentSession); // 重新加载压缩后的历史
      addSysBlock("✅ 压缩完成");
    }
  } catch (e) {
    addError("压缩失败：" + e.message);
  } finally {
    busy = false;
    sendBtn.disabled = false;
  }
}

// ---------- 设置页（全屏覆盖，左侧栏目导航 + 右侧内容） ----------

const settingsView = $("#settings-view");
const settingsPluginsEl = $("#settings-plugins");

async function openSettings() {
  settingsView.classList.remove("hidden");
  settingsPluginsEl.textContent = "加载中…";
  loadModels();
  await loadSettingsPlugins();
}

async function loadSettingsPlugins() {
  try {
    renderSettingsPlugins(await fetch("/api/plugins").then((r) => r.json()));
  } catch (e) {
    settingsPluginsEl.textContent = "加载插件列表失败：" + e.message;
  }
}

// 左侧栏目与右侧 section 按 data-section 对应
function showSettingsSection(name) {
  for (const b of document.querySelectorAll(".settings-nav-item")) {
    b.classList.toggle("active", b.dataset.section === name);
  }
  for (const s of document.querySelectorAll(".settings-section")) {
    s.classList.toggle("hidden", s.id !== "section-" + name);
  }
}

$("#settings-nav").addEventListener("click", (e) => {
  const btn = e.target.closest(".settings-nav-item");
  if (btn) showSettingsSection(btn.dataset.section);
});

function closeSettings() {
  settingsView.classList.add("hidden");
  // 设置页里也能改当前模型，回到聊天界面时把工具条上的显示对齐
  loadModelSwitcher();
}

// 与 internal/plugin 的 SourceBuiltin / SourceExternal 对应
const SOURCE_LABELS = { builtin: "内置", external: "外源" };

// 按功能分组分节展示：组间与组内都保持注册顺序（分组名随每个插件由后端给出），
// 未声明分组的插件（如外源插件）由后端归入「其他」，自然落在最后。
function renderSettingsPlugins(list) {
  settingsPluginsEl.textContent = "";
  const order = [];
  const byCat = new Map();
  for (const p of list) {
    const cat = p.category || "其他";
    if (!byCat.has(cat)) {
      byCat.set(cat, []);
      order.push(cat);
    }
    byCat.get(cat).push(p);
  }
  for (const cat of order) {
    const title = document.createElement("div");
    title.className = "plugin-group-title";
    title.textContent = cat;
    const grid = document.createElement("div");
    grid.className = "plugin-grid";
    for (const p of byCat.get(cat)) grid.appendChild(buildPluginCard(p));
    settingsPluginsEl.append(title, grid);
  }
}

function buildPluginCard(p) {
  const card = document.createElement("div");
  card.className = "plugin-card";

  const head = document.createElement("div");
  head.className = "plugin-card-head";
  const name = document.createElement("span");
  name.className = "plugin-card-name";
  name.textContent = p.name;

  const label = document.createElement("label");
  label.className = "switch";
  const input = document.createElement("input");
  input.type = "checkbox";
  input.checked = p.enabled;
  const slider = document.createElement("span");
  slider.className = "slider";
  label.append(input, slider);

  const actions = document.createElement("div");
  actions.className = "plugin-card-actions";
  // 操作入口（如扫码绑定）也走齿轮：入口统一在配置弹窗里，卡片上不单独摆按钮
  if ((p.config_fields || []).length > 0 || (p.actions || []).length > 0) {
    const gear = document.createElement("button");
    gear.type = "button";
    gear.className = "btn-icon btn-square btn-gear";
    gear.title = "配置";
    gear.innerHTML = gearIconSVG;
    gear.addEventListener("click", () => openPluginConfig(p));
    actions.appendChild(gear);
  }
  actions.appendChild(label);
  head.append(name, actions);
  card.appendChild(head);

  const tags = document.createElement("div");
  tags.className = "plugin-card-tags";
  const addTag = (text, extraClass) => {
    const tag = document.createElement("span");
    tag.className = extraClass ? "tag " + extraClass : "tag";
    tag.textContent = text;
    tags.appendChild(tag);
  };
  addTag(SOURCE_LABELS[p.source] || p.source || "内置", "tag-source");
  if (p.has_prompt) addTag("注入提示词");

  // 依赖未满足时不让开：后端也会拒绝，这里只是别让用户白点一次
  const unmet = p.unmet || [];
  if (unmet.length > 0) {
    addTag("需先启用 " + unmet.join("、"), "tag-blocked");
    input.disabled = true;
    label.title = "该插件依赖 " + unmet.join("、") + "，需要先启用后才能打开";
    card.classList.add("plugin-card-blocked");
  } else if ((p.requires || []).length > 0) {
    addTag("依赖 " + p.requires.join("、"));
  }
  // 冲突只告警不阻止：能力相抵的代价由用户自己权衡
  const conflicting = p.conflicting || [];
  if (p.enabled && conflicting.length > 0) {
    addTag("与 " + conflicting.join("、") + " 能力相抵", "tag-warn");
  }
  card.appendChild(tags);

  // 工具名不再逐个占一个标签（数量多时会把版面撑乱），改为悬停查看
  const tools = p.tool_names || [];
  if (tools.length > 0) {
    card.title = "工具：" + tools.join("、");
  }

  const desc = document.createElement("div");
  desc.className = "plugin-card-desc";
  desc.textContent = p.description;
  card.appendChild(desc);

  input.addEventListener("change", async () => {
    const want = input.checked;
    input.disabled = true;
    try {
      const res = await fetch("/api/plugins/" + encodeURIComponent(p.name), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: want }),
      });
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err.error || "HTTP " + res.status);
      }
      renderSettingsPlugins(await res.json());
    } catch (e) {
      input.checked = !want; // 失败回滚开关状态
      input.disabled = false;
      addError("切换插件失败：" + e.message);
    }
  });

  return card;
}

$("#btn-settings").addEventListener("click", openSettings);
$("#btn-settings-back").addEventListener("click", closeSettings);
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  // 弹窗盖在设置页之上，Esc 先关最上层的弹窗
  if (!providerModal.classList.contains("hidden")) closeProviderModal();
  else if (!modelModal.classList.contains("hidden")) closeModelModal();
  else if (!actionModal.classList.contains("hidden")) closePluginAction();
  else if (!configModal.classList.contains("hidden")) closePluginConfig();
  else if (!settingsView.classList.contains("hidden")) closeSettings();
});

// ---------- 插件操作弹窗 ----------

const actionModal = $("#plugin-action-modal");
const actionTitleEl = $("#plugin-action-title");
const actionImageEl = $("#plugin-action-image");
const actionMessageEl = $("#plugin-action-message");

let actionPollTimer = null;

function renderActionState(st) {
  actionMessageEl.textContent = st.message || "";
  if (st.image) {
    actionImageEl.src = "data:image/png;base64," + st.image;
    actionImageEl.classList.remove("hidden");
  } else {
    actionImageEl.removeAttribute("src");
    actionImageEl.classList.add("hidden");
  }
  actionMessageEl.classList.toggle("action-error", st.status === "error");
}

// 触发插件操作并弹出进展窗；长流程由插件在后台推进，这里只轮询展示。
// draft 是配置弹窗里当前填写、尚未保存的值，随请求带给插件，使「测试」类操作
// 能验证还没保存的配置。
async function startPluginAction(pluginName, actionDef, draft) {
  actionTitleEl.textContent = pluginName + " · " + actionDef.label;
  renderActionState({ message: "正在开始…" });
  actionModal.classList.remove("hidden");
  const url =
    "/api/plugins/" + encodeURIComponent(pluginName) +
    "/actions/" + encodeURIComponent(actionDef.key);
  try {
    const res = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ config: draft || {} }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || "HTTP " + res.status);
    }
  } catch (e) {
    renderActionState({ status: "error", message: "操作启动失败：" + e.message });
    return;
  }
  const poll = async () => {
    try {
      const res = await fetch(url);
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err.error || "HTTP " + res.status);
      }
      const st = await res.json();
      renderActionState(st);
      if (st.status === "done" || st.status === "error") {
        actionPollTimer = null;
        if (st.status === "done") loadSettingsPlugins(); // 操作可能改变按钮文案（如「绑定」变「重新绑定」）
        return;
      }
    } catch (e) {
      renderActionState({ status: "error", message: "查询进展失败：" + e.message });
      actionPollTimer = null;
      return;
    }
    actionPollTimer = setTimeout(poll, 1500);
  };
  actionPollTimer = setTimeout(poll, 800);
}

// 关闭弹窗只停轮询，不打断插件侧的流程（插件自带超时）
function closePluginAction() {
  actionModal.classList.add("hidden");
  if (actionPollTimer) {
    clearTimeout(actionPollTimer);
    actionPollTimer = null;
  }
}

$("#btn-action-close").addEventListener("click", closePluginAction);
$("#btn-action-dismiss").addEventListener("click", closePluginAction);
actionModal.addEventListener("mousedown", (e) => {
  if (e.target === actionModal) closePluginAction();
});

// ---------- 插件配置弹窗 ----------

const gearIconSVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
  '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>';

const configModal = $("#plugin-config-modal");
const configTitleEl = $("#plugin-config-title");
const configFormEl = $("#plugin-config-form");
const configErrorEl = $("#plugin-config-error");
const configSaveBtn = $("#btn-config-save");

let configPlugin = null; // 当前编辑的插件状态
let configInputs = new Map(); // 配置项 key -> 输入元素

function openPluginConfig(p) {
  configPlugin = p;
  configInputs = new Map();
  configTitleEl.textContent = p.name + " · 配置";
  configFormEl.textContent = "";
  showConfigError("");
  const fields = p.config_fields || [];
  const values = p.config || {};
  for (const f of fields) {
    configFormEl.appendChild(buildConfigField(f, values[f.key]));
  }
  // 插件的操作入口（如扫码绑定）附在配置项之后；点击后转入操作进展弹窗
  for (const a of p.actions || []) {
    const row = document.createElement("div");
    row.className = "plugin-config-action";
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn-ghost";
    btn.textContent = a.label;
    // 配置弹窗不关：进展窗盖在它上面，测试完回到原处，填了一半的内容还在。
    // 「测试」类操作正是为「保存之前先验一下」而存在的，关掉就等于要求先保存。
    btn.addEventListener("click", () => startPluginAction(p.name, a, readConfigValues()));
    row.appendChild(btn);
    if (a.description) {
      const desc = document.createElement("div");
      desc.className = "field-desc";
      desc.textContent = a.description;
      row.appendChild(desc);
    }
    configFormEl.appendChild(row);
  }
  // 没有配置项（纯操作入口）时保存与恢复默认没有意义
  const hasFields = fields.length > 0;
  configSaveBtn.classList.toggle("hidden", !hasFields);
  $("#btn-config-reset").classList.toggle("hidden", !hasFields);
  configModal.classList.remove("hidden");
  const first = configFormEl.querySelector("input, select, textarea");
  if (first) first.focus();
}

function closePluginConfig() {
  configModal.classList.add("hidden");
  configPlugin = null;
}

// buildConfigField 按字段声明生成一行表单项，并登记其输入元素
function buildConfigField(f, value) {
  const wrap = document.createElement("div");
  wrap.className = "field";

  const label = document.createElement("span");
  label.className = "field-label";
  label.textContent = f.label || f.key;

  let el;
  if (f.type === "bool") {
    el = document.createElement("input");
    el.type = "checkbox";
    const sw = document.createElement("label");
    sw.className = "switch";
    const slider = document.createElement("span");
    slider.className = "slider";
    sw.append(el, slider);
    const row = document.createElement("div");
    row.className = "field-row";
    row.append(label, sw);
    wrap.appendChild(row);
  } else if (f.type === "select") {
    el = document.createElement("select");
    for (const o of f.options || []) {
      const opt = document.createElement("option");
      opt.value = o.value;
      opt.textContent = o.label || o.value;
      el.appendChild(opt);
    }
    wrap.append(label, el);
  } else if (f.type === "text") {
    el = document.createElement("textarea");
    el.rows = 5; // 够看清几行就行，太高会把配置弹窗撑得要滚动
    el.spellcheck = false;
    wrap.append(label, el);
  } else {
    el = document.createElement("input");
    if (f.type === "int") {
      el.type = "number";
      el.step = "1";
      if (f.min !== undefined) el.min = f.min;
      if (f.max !== undefined) el.max = f.max;
    } else {
      el.type = "text";
    }
    wrap.append(label, wrapNumberInput(el));
  }

  setFieldValue(f, el, value === undefined ? f.default : value);
  configInputs.set(f.key, el);

  const descText = [f.description, rangeHint(f)].filter(Boolean).join(" ");
  if (descText) {
    const desc = document.createElement("div");
    desc.className = "field-desc";
    desc.textContent = descText;
    wrap.appendChild(desc);
  }
  return wrap;
}

function rangeHint(f) {
  if (f.type !== "int") return "";
  if (f.min !== undefined && f.max !== undefined) return `取值范围 ${f.min} ~ ${f.max}。`;
  if (f.min !== undefined) return `不小于 ${f.min}。`;
  if (f.max !== undefined) return `不大于 ${f.max}。`;
  return "";
}

function setFieldValue(f, el, v) {
  if (f.type === "bool") el.checked = Boolean(v);
  else el.value = v === undefined || v === null ? "" : String(v);
}

// readConfigValues 收集表单值；数值以字符串提交，由服务端统一校验并给出中文提示。
// 多行文本不做 trim：首尾的空行也是用户排版的一部分，服务端只统一换行符。
function readConfigValues() {
  const out = {};
  for (const f of configPlugin.config_fields || []) {
    const el = configInputs.get(f.key);
    if (!el) continue;
    if (f.type === "bool") out[f.key] = el.checked;
    else if (f.type === "text") out[f.key] = el.value;
    else out[f.key] = el.value.trim();
  }
  return out;
}

function showConfigError(msg) {
  configErrorEl.textContent = msg;
  configErrorEl.classList.toggle("hidden", !msg);
}

async function savePluginConfig() {
  if (!configPlugin) return;
  const name = configPlugin.name;
  configSaveBtn.disabled = true;
  try {
    const res = await fetch("/api/plugins/" + encodeURIComponent(name) + "/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ config: readConfigValues() }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || "HTTP " + res.status);
    }
    renderSettingsPlugins(await res.json());
    closePluginConfig();
  } catch (e) {
    showConfigError("保存失败：" + e.message);
  } finally {
    configSaveBtn.disabled = false;
  }
}

$("#btn-config-save").addEventListener("click", savePluginConfig);
$("#btn-config-cancel").addEventListener("click", closePluginConfig);
$("#btn-config-close").addEventListener("click", closePluginConfig);
$("#btn-config-reset").addEventListener("click", () => {
  if (!configPlugin) return;
  for (const f of configPlugin.config_fields || []) {
    setFieldValue(f, configInputs.get(f.key), f.default);
  }
  showConfigError("");
});
configFormEl.addEventListener("submit", (e) => {
  e.preventDefault(); // 回车提交等同点击保存
  savePluginConfig();
});
configModal.addEventListener("mousedown", (e) => {
  if (e.target === configModal) closePluginConfig(); // 点击遮罩关闭
});

// ---------- 模型配置 ----------

const pencilIconSVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
  '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4z"/></svg>';
const trashIconSVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
  '<polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>' +
  '<path d="M10 11v6"/><path d="M14 11v6"/><path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"/></svg>';

const modelsEl = $("#settings-models");
const modelsErrorEl = $("#models-error");
const modelsCurrentEl = $("#models-current");

let modelsDoc = null; // 最近一次 /api/models 的响应

async function loadModels() {
  modelsEl.textContent = "加载中…";
  try {
    renderModels(await fetch("/api/models").then((r) => r.json()));
  } catch (e) {
    modelsEl.textContent = "加载模型配置失败：" + e.message;
  }
}

function showModelsError(msg) {
  modelsErrorEl.textContent = msg;
  modelsErrorEl.classList.toggle("hidden", !msg);
}

// 提交整档配置：api_key 一律留空表示不修改，需要改的由调用方填上
function payloadFromDoc() {
  return {
    providers: modelsDoc.providers.map((p) => ({
      name: p.name,
      type: p.type,
      base_url: p.base_url,
      api_key: "",
      thinking_dialect: p.thinking_dialect || "",
      // 三态原样带回：null 表示「未设置」，与「明确关过」不同
      prompt_cache: typeof p.prompt_cache === "boolean" ? p.prompt_cache : null,
      models: p.models,
    })),
    current: modelsDoc.current,
  };
}

async function putModels(payload) {
  const res = await fetch("/api/models", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || "HTTP " + res.status);
  }
  renderModels(await res.json());
}

function typeLabel(value) {
  const t = (modelsDoc.types || []).find((x) => x.value === value);
  return t ? t.label : value;
}

function renderModels(doc) {
  modelsDoc = doc;
  showModelsError("");
  modelsEl.textContent = "";

  const cur = doc.current || {};
  modelsCurrentEl.textContent = "";
  modelsCurrentEl.append("当前使用：");
  const b = document.createElement("b");
  const curProvider = doc.providers.find((p) => p.name === cur.provider);
  const curModel = curProvider && (curProvider.models || []).find((m) => m.id === cur.model);
  b.textContent = cur.provider ? cur.provider + " / " + ((curModel && curModel.name) || cur.model || "—") : "未选择";
  modelsCurrentEl.appendChild(b);

  for (const p of doc.providers) {
    modelsEl.appendChild(buildProviderCard(p));
  }
}

function buildProviderCard(p) {
  const card = document.createElement("div");
  card.className = "provider-card";
  if (modelsDoc.current.provider === p.name) card.classList.add("active");

  const head = document.createElement("div");
  head.className = "provider-head";
  const name = document.createElement("span");
  name.className = "provider-name";
  name.textContent = p.name;
  head.append(name, tagEl(typeLabel(p.type)));
  if (p.source === "config") head.appendChild(tagEl("来自配置文件"));

  const actions = document.createElement("div");
  actions.className = "provider-actions";
  actions.append(
    iconButton(gearIconSVG, "编辑提供商", () => openProviderModal(p.name)),
    iconButton(trashIconSVG, "删除提供商", () => deleteProvider(p.name), "btn-icon-danger"),
  );
  head.appendChild(actions);
  card.appendChild(head);

  const meta = document.createElement("div");
  meta.className = "provider-meta";
  meta.textContent = p.base_url + (p.has_api_key ? "  ·  API Key " + p.api_key_masked : "  ·  未配置 API Key");
  card.appendChild(meta);

  const list = document.createElement("div");
  list.className = "model-list";
  for (const m of p.models || []) list.appendChild(buildModelRow(p, m));
  if ((p.models || []).length === 0) {
    const empty = document.createElement("div");
    empty.className = "model-empty";
    empty.textContent = "暂无模型";
    list.appendChild(empty);
  }
  const add = document.createElement("button");
  add.type = "button";
  add.className = "btn-link";
  add.textContent = "＋ 添加模型";
  add.addEventListener("click", () => openModelModal(p.name, null));
  list.appendChild(add);
  card.appendChild(list);
  return card;
}

function buildModelRow(p, m) {
  const row = document.createElement("div");
  row.className = "model-row";
  const active = modelsDoc.current.provider === p.name && modelsDoc.current.model === m.id;
  if (active) row.classList.add("active");
  row.title = active ? "当前使用中" : "点击切换到该模型";

  const radio = document.createElement("span");
  radio.className = "model-radio";
  const name = document.createElement("span");
  name.className = "model-name";
  name.textContent = m.name || m.id;
  row.append(radio, name);
  if (m.name) {
    const id = document.createElement("span");
    id.className = "model-id";
    id.textContent = m.id;
    row.appendChild(id);
  }

  const actions = document.createElement("div");
  actions.className = "model-row-actions";
  actions.append(
    iconButton(pencilIconSVG, "编辑模型", (e) => { e.stopPropagation(); openModelModal(p.name, m.id); }),
    iconButton(trashIconSVG, "删除模型", (e) => { e.stopPropagation(); deleteModel(p.name, m.id); }, "btn-icon-danger"),
  );
  row.appendChild(actions);

  row.addEventListener("click", () => switchModel(p.name, m.id));
  return row;
}

function tagEl(text) {
  const el = document.createElement("span");
  el.className = "tag";
  el.textContent = text;
  return el;
}

function iconButton(svg, title, onClick, extraClass) {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-icon btn-square btn-gear" + (extraClass ? " " + extraClass : "");
  btn.title = title;
  btn.innerHTML = svg;
  btn.addEventListener("click", onClick);
  return btn;
}

async function switchModel(provider, model) {
  if (modelsDoc.current.provider === provider && modelsDoc.current.model === model) return;
  showModelsError("");
  try {
    const res = await fetch("/api/models/current", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ provider, model }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || "HTTP " + res.status);
    }
    renderModels(await res.json());
  } catch (e) {
    showModelsError("切换模型失败：" + e.message);
  }
}

async function deleteProvider(name) {
  if (!confirm(`确定删除提供商「${name}」？`)) return;
  const payload = payloadFromDoc();
  payload.providers = payload.providers.filter((p) => p.name !== name);
  showModelsError("");
  try {
    await putModels(payload);
  } catch (e) {
    showModelsError("删除失败：" + e.message);
  }
}

async function deleteModel(provider, id) {
  if (!confirm(`确定删除模型「${id}」？`)) return;
  const payload = payloadFromDoc();
  const p = payload.providers.find((x) => x.name === provider);
  p.models = (p.models || []).filter((m) => m.id !== id);
  showModelsError("");
  try {
    await putModels(payload);
  } catch (e) {
    showModelsError("删除失败：" + e.message);
  }
}

// ---------- 提供商弹窗 ----------

const providerModal = $("#provider-modal");
const providerForm = $("#provider-form");
const providerErrorEl = $("#provider-error");
const providerTitleEl = $("#provider-modal-title");
let providerEditing = null; // 正在编辑的提供商名，null 表示新增
let providerInputs = {};

function openProviderModal(name) {
  providerEditing = name || null;
  const p = name ? modelsDoc.providers.find((x) => x.name === name) : null;
  providerTitleEl.textContent = p ? "编辑提供商 · " + p.name : "新增提供商";
  providerForm.textContent = "";
  showProviderError("");

  const nameInput = textInput(p ? p.name : "");
  const typeSelect = document.createElement("select");
  for (const t of modelsDoc.types || []) {
    const opt = document.createElement("option");
    opt.value = t.value;
    opt.textContent = t.label;
    typeSelect.appendChild(opt);
  }
  typeSelect.value = p ? p.type : (modelsDoc.types[0] || {}).value;
  const urlInput = textInput(p ? p.base_url : defaultBaseURL(typeSelect.value));
  const keyInput = textInput("");
  keyInput.type = "password";
  keyInput.placeholder = p && p.has_api_key ? "留空表示不修改（当前 " + p.api_key_masked + "）" : "";

  // 思考参数方言：OpenAI 兼容协议里各家的思考扩展互不兼容，按提供商选择
  const dialectSelect = document.createElement("select");
  for (const d of modelsDoc.dialects || []) {
    const opt = document.createElement("option");
    opt.value = d.value;
    opt.textContent = d.label;
    dialectSelect.appendChild(opt);
  }
  dialectSelect.value = (p && p.thinking_dialect) || "deepseek";
  const dialectField = fieldEl("思考参数方言", dialectSelect,
    "各家 OpenAI 兼容接口的思考/推理参数写法不同：DeepSeek 用 thinking+reasoning_effort，MiniMax 用 adaptive+reasoning_split，Qwen 用 enable_thinking。选错会被对方 API 以 400 拒绝或思考内容混入正文。");

  // 提示词缓存：仅 Anthropic 需要在请求里显式打断点，故只在该模式下出现。
  // 默认开启——未设置（null）与明确开启在效果上一致。
  const cacheSwitch = switchInput(p ? p.prompt_cache !== false : true);
  const cacheField = fieldEl("提示词缓存", cacheSwitch.wrap,
    "开启后把不变的部分（工具、system、已有历史）标为可缓存，命中时这部分按约十分之一的价格计费。未命中的写入要多付约四分之一，因此若对话间隔常常超过几分钟（缓存有效期），关掉更省。");

  const syncTypeFields = () => {
    dialectField.classList.toggle("hidden", typeSelect.value !== "openai_compat");
    cacheField.classList.toggle("hidden", typeSelect.value !== "anthropic");
  };

  // 切换 API 模式时，若地址还是另一模式的默认值就跟着换
  typeSelect.addEventListener("change", () => {
    const defaults = (modelsDoc.types || []).map((t) => t.default_base_url);
    if (!urlInput.value.trim() || defaults.includes(urlInput.value.trim())) {
      urlInput.value = defaultBaseURL(typeSelect.value);
    }
    syncTypeFields();
  });

  providerForm.append(
    fieldEl("名称", nameInput, "列表中显示的名字，需唯一。"),
    fieldEl("API 模式", typeSelect, "Anthropic 模式使用 Messages API；OpenAI 兼容适用于 DeepSeek 等服务。"),
    dialectField,
    fieldEl("Base URL", urlInput, "服务地址，需以 http:// 或 https:// 开头。"),
    fieldEl("API Key", keyInput, "保存在本机 models.json 中，不会提交到仓库。"),
    cacheField,
  );
  syncTypeFields();
  providerInputs = { name: nameInput, type: typeSelect, dialect: dialectSelect, base_url: urlInput,
    api_key: keyInput, prompt_cache: cacheSwitch.input };

  providerModal.classList.remove("hidden");
  nameInput.focus();
}

function defaultBaseURL(type) {
  const t = (modelsDoc.types || []).find((x) => x.value === type);
  return t ? t.default_base_url : "";
}

function closeProviderModal() {
  providerModal.classList.add("hidden");
  providerEditing = null;
}

// kind：error（默认，红）/ info（进行中，蓝）/ ok（成功，绿）
function showProviderError(msg, kind) {
  providerErrorEl.textContent = msg;
  providerErrorEl.classList.toggle("hidden", !msg);
  providerErrorEl.classList.toggle("is-info", kind === "info");
  providerErrorEl.classList.toggle("is-ok", kind === "ok");
}

async function saveProvider() {
  const values = {
    name: providerInputs.name.value.trim(),
    type: providerInputs.type.value,
    base_url: providerInputs.base_url.value.trim(),
    api_key: providerInputs.api_key.value.trim(),
    // 方言只对 OpenAI 兼容模式有意义，Anthropic 模式一律留空
    thinking_dialect: providerInputs.type.value === "openai_compat" ? providerInputs.dialect.value : "",
    // 勾上就交回 null（未设置=开启）而不是 true：这样与 config.yaml 一致的条目不会
    // 因为多了一个显式取值就被 models.json 接管过来。
    prompt_cache: promptCacheValue(),
  };
  const payload = payloadFromDoc();
  if (providerEditing) {
    const p = payload.providers.find((x) => x.name === providerEditing);
    Object.assign(p, values, { models: p.models });
    if (payload.current.provider === providerEditing) payload.current.provider = values.name;
  } else {
    payload.providers.push({ ...values, models: [] });
  }
  try {
    await putModels(payload);
    closeProviderModal();
  } catch (e) {
    showProviderError("保存失败：" + e.message);
  }
}

async function testProvider() {
  const p = providerEditing ? modelsDoc.providers.find((x) => x.name === providerEditing) : null;
  const model = p && (p.models || [])[0];
  if (!model) {
    showProviderError("请先为该提供商添加模型后再测试");
    return;
  }
  showProviderError("正在测试…", "info");
  const testBtn = $("#btn-provider-test");
  testBtn.disabled = true;
  try {
    const res = await fetch("/api/models/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        provider: providerEditing,
        type: providerInputs.type.value,
        base_url: providerInputs.base_url.value.trim(),
        api_key: providerInputs.api_key.value.trim(),
        thinking_dialect: providerInputs.type.value === "openai_compat" ? providerInputs.dialect.value : "",
        prompt_cache: promptCacheValue(),
        model: model.id,
      }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || "HTTP " + res.status);
    }
    showProviderError("连接正常（模型 " + model.id + "）", "ok");
  } catch (e) {
    showProviderError(e.message);
  } finally {
    testBtn.disabled = false;
  }
}

// ---------- 模型弹窗 ----------

const modelModal = $("#model-modal");
const modelForm = $("#model-form");
const modelErrorEl = $("#model-error");
const modelTitleEl = $("#model-modal-title");
let modelEditing = null; // {provider, id}，id 为 null 表示新增
let modelInputs = {};

function openModelModal(provider, id) {
  const p = modelsDoc.providers.find((x) => x.name === provider);
  const m = id ? (p.models || []).find((x) => x.id === id) : null;
  modelEditing = { provider, id: id || null };
  modelTitleEl.textContent = m ? "编辑模型 · " + m.id : "添加模型 · " + provider;
  modelForm.textContent = "";
  showModelError("");

  const d = modelsDoc.defaults || {};
  const idInput = textInput(m ? m.id : "");
  const nameInput = textInput(m && m.name ? m.name : "");
  const ctxInput = numberInput(m && m.context_length, `跟随全局（${d.context_length}）`, { min: 1 });
  const maxInput = numberInput(m && m.max_tokens, `跟随全局（${d.max_tokens}）`, { min: 1 });
  const tempInput = numberInput(m && m.temperature, `跟随全局（${d.temperature}）`, { min: 0, max: 2, step: 0.1 });

  const thinkSelect = document.createElement("select");
  const follow = document.createElement("option");
  follow.value = "";
  follow.textContent = `跟随全局（${d.thinking}）`;
  thinkSelect.appendChild(follow);
  for (const lv of modelsDoc.thinking_levels || []) {
    const opt = document.createElement("option");
    opt.value = lv;
    opt.textContent = lv;
    thinkSelect.appendChild(opt);
  }
  thinkSelect.value = m && m.thinking ? m.thinking : "";

  const isAnthropic = p.type === "anthropic";
  modelForm.append(
    fieldEl("模型 ID", idInput, "传给 API 的模型 id，例如 deepseek-v4-flash。"),
    fieldEl("显示名", nameInput, "列表中显示的名字，留空则显示模型 ID。"),
    fieldEl("上下文窗口", ctxInput, "token 数，用于裁剪与自动压缩阈值。留空则使用 config.yaml 的全局值。"),
    fieldEl("最大输出 tokens", maxInput, "单次回复的输出上限。留空则使用全局值。"),
    fieldEl("思考模式", thinkSelect, isAnthropic
      ? "Anthropic 模式下 off 之外的档位映射为 adaptive + effort。"
      : "off 关闭思考，其余为思考强度。"),
    fieldEl("temperature", tempInput, isAnthropic
      ? "Anthropic 模式忽略此项：当前世代 Claude 模型不接受采样参数。"
      : "0 ~ 2，思考开启时不生效。留空则使用全局值。"),
  );
  modelInputs = { id: idInput, name: nameInput, context_length: ctxInput, max_tokens: maxInput, thinking: thinkSelect, temperature: tempInput };

  modelModal.classList.remove("hidden");
  idInput.focus();
}

function closeModelModal() {
  modelModal.classList.add("hidden");
  modelEditing = null;
}

function showModelError(msg) {
  modelErrorEl.textContent = msg;
  modelErrorEl.classList.toggle("hidden", !msg);
}

async function saveModel() {
  const entry = { id: modelInputs.id.value.trim() };
  const name = modelInputs.name.value.trim();
  if (name) entry.name = name;
  // 留空的可选项直接不提交，由服务端回退到全局值
  const ctx = modelInputs.context_length.value.trim();
  if (ctx) entry.context_length = Number(ctx);
  const max = modelInputs.max_tokens.value.trim();
  if (max) entry.max_tokens = Number(max);
  const temp = modelInputs.temperature.value.trim();
  if (temp) entry.temperature = Number(temp);
  if (modelInputs.thinking.value) entry.thinking = modelInputs.thinking.value;

  const payload = payloadFromDoc();
  const p = payload.providers.find((x) => x.name === modelEditing.provider);
  p.models = (p.models || []).slice();
  if (modelEditing.id) {
    const i = p.models.findIndex((m) => m.id === modelEditing.id);
    p.models[i] = entry;
    if (payload.current.provider === p.name && payload.current.model === modelEditing.id) {
      payload.current.model = entry.id;
    }
  } else {
    p.models.push(entry);
  }
  try {
    await putModels(payload);
    closeModelModal();
  } catch (e) {
    showModelError("保存失败：" + e.message);
  }
}

// ---------- 表单小工具 ----------

const chevronUpSVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="18 15 12 9 6 15"/></svg>';
const chevronDownSVG =
  '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"/></svg>';

// wrapNumberInput 给数字输入配上自定义的上下箭头（系统默认样式已在 CSS 中隐藏）
function wrapNumberInput(input) {
  if (!(input instanceof HTMLInputElement) || input.type !== "number") return input;

  const wrap = document.createElement("div");
  wrap.className = "number-field";
  const steppers = document.createElement("div");
  steppers.className = "number-steppers";
  steppers.append(stepButton(chevronUpSVG, input, 1), stepButton(chevronDownSVG, input, -1));
  wrap.append(input, steppers);
  return wrap;
}

function stepButton(svg, input, dir) {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "number-step";
  btn.tabIndex = -1; // 键盘上下键本来就能调值，按钮不进 Tab 顺序
  btn.title = dir > 0 ? "增加" : "减少";
  btn.innerHTML = svg;
  btn.addEventListener("click", () => {
    // 空值时从下限（没有下限则从 0）起步，避免浏览器各自的默认行为
    if (input.value.trim() === "") input.value = input.min !== "" ? input.min : "0";
    else if (dir > 0) input.stepUp();
    else input.stepDown();
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.focus();
  });
  return btn;
}

// promptCacheValue 读提供商弹窗里的缓存开关：非 Anthropic 模式该项无意义，交 null。
function promptCacheValue() {
  if (providerInputs.type.value !== "anthropic") return null;
  return providerInputs.prompt_cache.checked ? null : false;
}

// switchInput 造一个滑动开关，返回 {wrap, input}（与插件配置里的 bool 字段同一套样式）。
function switchInput(checked) {
  const input = document.createElement("input");
  input.type = "checkbox";
  input.checked = !!checked;
  const wrap = document.createElement("label");
  wrap.className = "switch";
  const slider = document.createElement("span");
  slider.className = "slider";
  wrap.append(input, slider);
  return { wrap, input };
}

function textInput(value) {
  const el = document.createElement("input");
  el.type = "text";
  el.value = value || "";
  return el;
}

function numberInput(value, placeholder, opts) {
  const el = document.createElement("input");
  el.type = "number";
  el.value = value === undefined || value === null ? "" : String(value);
  el.placeholder = placeholder || "";
  const o = opts || {};
  if (o.min !== undefined) el.min = o.min;
  if (o.max !== undefined) el.max = o.max;
  if (o.step !== undefined) el.step = o.step;
  return el;
}

function fieldEl(label, input, desc) {
  const wrap = document.createElement("div");
  wrap.className = "field";
  const lab = document.createElement("span");
  lab.className = "field-label";
  lab.textContent = label;
  wrap.append(lab, wrapNumberInput(input));
  if (desc) {
    const d = document.createElement("div");
    d.className = "field-desc";
    d.textContent = desc;
    wrap.appendChild(d);
  }
  return wrap;
}

$("#btn-add-provider").addEventListener("click", () => openProviderModal(null));
$("#btn-provider-save").addEventListener("click", saveProvider);
$("#btn-provider-test").addEventListener("click", testProvider);
$("#btn-provider-cancel").addEventListener("click", closeProviderModal);
$("#btn-provider-close").addEventListener("click", closeProviderModal);
providerForm.addEventListener("submit", (e) => { e.preventDefault(); saveProvider(); });
providerModal.addEventListener("mousedown", (e) => {
  if (e.target === providerModal) closeProviderModal();
});

$("#btn-model-save").addEventListener("click", saveModel);
$("#btn-model-cancel").addEventListener("click", closeModelModal);
$("#btn-model-close").addEventListener("click", closeModelModal);
modelForm.addEventListener("submit", (e) => { e.preventDefault(); saveModel(); });
modelModal.addEventListener("mousedown", (e) => {
  if (e.target === modelModal) closeModelModal();
});

// 解析 POST 响应体中的 SSE 流，逐个产出 {type, ...} 事件对象
async function* sseEvents(body) {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf("\n\n")) >= 0) {
      const frame = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      const dataLines = frame
        .split("\n")
        .filter((l) => l.startsWith("data:"))
        .map((l) => l.slice(5).trimStart());
      if (dataLines.length === 0) continue;
      try {
        yield JSON.parse(dataLines.join("\n"));
      } catch { /* 忽略坏帧 */ }
    }
  }
}

// ---------- 输入框行为 ----------

// 输入框随内容增高，最多 3 行；超过 3 行固定高度并出现滚动条
const inputMaxHeight = (() => {
  const cs = getComputedStyle(inputEl);
  return parseFloat(cs.lineHeight) * 3 +
    parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
})();

function autoGrow() {
  inputEl.style.height = "auto";
  inputEl.style.height = Math.min(inputEl.scrollHeight, inputMaxHeight) + "px";
  inputEl.style.overflowY = inputEl.scrollHeight > inputMaxHeight ? "auto" : "hidden";
  syncComposerHeight(); // 输入框高度一变，悬浮卡片给消息区让出的空白也要跟着变
}

// 输入框是悬浮的，消息区得留出与它等高的底部空白，否则最后一条消息被压在下面。
// 高度随输入内容变化，所以量出来写进 CSS 变量，而不是在样式里写死一个值。
const composerBox = $(".composer-box");
const chatEl = $(".chat");

function syncComposerHeight() {
  // 原本贴着底就继续贴着，不因输入框变高而"浮起"
  const stuck = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 8;
  chatEl.style.setProperty("--composer-h", composerBox.offsetHeight + 20 + "px"); // 20 = 卡片下方留白
  // 消息区为滚动条预留的宽度，输入框要让出同样一段才能与上方内容对齐。
  // 各系统与缩放比例下取值不同，只能实测
  chatEl.style.setProperty("--scrollbar-w", messagesEl.offsetWidth - messagesEl.clientWidth + "px");
  if (stuck) scrollBottom();
}

// 输入时同步调用而不是只靠观察器：观察器要等到下一帧才回调，
// 且页面不在前台时可能一直不回调，留出的空白会停在旧值上。
// 观察器仍然保留，兜住窗口缩放、工具条文字换行这些非输入引起的变化。
syncComposerHeight();
new ResizeObserver(syncComposerHeight).observe(composerBox);
new ResizeObserver(syncComposerHeight).observe(messagesEl); // 窗口缩放改变栏宽时重新对齐

inputEl.addEventListener("input", () => {
  autoGrow();
  updateCmdMenu();
});
inputEl.addEventListener("blur", hideCmdMenu);
inputEl.addEventListener("keydown", (e) => {
  if (cmdMatches.length > 0 && !e.isComposing) {
    if (e.key === "ArrowDown") { e.preventDefault(); moveCmdSel(1); return; }
    if (e.key === "ArrowUp") { e.preventDefault(); moveCmdSel(-1); return; }
    if (e.key === "Enter" || e.key === "Tab") { e.preventDefault(); pickCommand(cmdIndex); return; }
    if (e.key === "Escape") { hideCmdMenu(); return; }
  }
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    sendMessage();
  }
});

$("#chat-form").addEventListener("submit", (e) => {
  e.preventDefault();
  if (busy) {
    // 等待回复期间按钮是「停止」：点击中断本轮生成（Enter 发送不受影响，仍被 sendMessage 的 busy 挡住）
    if (chatAbort) chatAbort.abort();
    return;
  }
  sendMessage();
});

$("#btn-new").addEventListener("click", newSession);

loadSessions();

// 左下角的版本号：取自 /api/status，取不到就不显示，不影响使用
fetch("/api/status")
  .then((r) => r.json())
  .then((st) => {
    if (!st.version) return;
    const el = $("#version-label");
    el.textContent = st.version;
    el.title = "Wen Agent " + st.version;
  })
  .catch(() => {});

// 侧栏定期刷新：QQ 远程会话、心跳与定时任务产生的新会话及标题变化自动出现。
// 只刷列表不动消息区，对话进行中（busy）跳过，避免打扰。
setInterval(() => {
  if (!busy) loadSessions();
}, 30000);
inputEl.focus();

// ---- 会话注记的实时通道 ----
//
// /api/chat 的事件流一轮就关，而后台工作（记忆提炼这类）在那之后才跑完。这里订阅
// 一条常驻流，任何会话上产生的注记都即时推过来，按当前会话筛选后追加到消息区。
// 注记同时已经落盘，所以断线期间漏掉的内容刷新页面就能补齐——不需要补发机制。
function subscribeNotices() {
  const es = new EventSource("/api/events");
  es.addEventListener("notice", (e) => {
    let n;
    try {
      n = JSON.parse(e.data);
    } catch {
      return;
    }
    if (!n.content || n.session_id !== currentSession) return;
    addSysBlock(n.content, "notice");
  });
  // EventSource 自带重连；这里只在彻底关闭时兜一次底
  es.addEventListener("error", () => {
    if (es.readyState === EventSource.CLOSED) setTimeout(subscribeNotices, 5000);
  });
}

subscribeNotices();
