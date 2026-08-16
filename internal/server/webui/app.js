"use strict";

const $ = (sel) => document.querySelector(sel);
const messagesEl = $("#messages");
const sessionListEl = $("#session-list");
const inputEl = $("#input");
const sendBtn = $("#btn-send");

let currentSession = null; // 当前 session id
let busy = false;          // 正在等待回复

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
  loadSessions();
}

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

function addSysBlock(text) {
  hideHint();
  const div = document.createElement("div");
  div.className = "sys-block";
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

function renderHistory(messages) {
  clearMessages();
  if (!messages || messages.length === 0) return;
  hideHint();
  const toolBlocks = {}; // tool_call id -> {el, args}
  for (const m of messages) {
    if (m.kind === "summary") {
      addSummaryBlock(m.content);
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

  busy = true;
  sendBtn.disabled = true;
  inputEl.value = "";
  autoGrow();
  addBubble("user", text);

  let assistantBubble = null; // 惰性创建，收到第一个 delta 才建
  let assistantRaw = "";      // 当前气泡的原始 Markdown 文本
  let thinkingBlock = null;   // 当前轮的思考块
  let compactBlock = null;    // 自动压缩的动态展示块
  let autoCompacted = false;  // 本轮发生过自动压缩，结束后需重载历史
  const toolBlocks = {};
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
      body: JSON.stringify({ session_id: currentSession, message: text }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || `HTTP ${res.status}`);
    }

    for await (const ev of sseEvents(res.body)) {
      if (ev.type === "thinking") {
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
    addError("请求失败：" + e.message);
  } finally {
    finishBubble();
    finishThinking();
    busy = false;
    sendBtn.disabled = false;
    if (autoCompacted) {
      await selectSession(currentSession); // 重载压缩后的历史
      addSysBlock("✅ 会话已自动压缩");
    }
    loadSessions(); // 刷新标题
    inputEl.focus();
  }
}

// ---------- 命令菜单（输入 / 时弹出，随输入筛选） ----------

const COMMANDS = [
  { cmd: "/status", desc: "显示 Agent 状态：模型、思考深度、上下文用量" },
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
      "📊 Agent 状态",
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
    } else {
      lines.push("当前会话：无");
    }
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
  try {
    renderSettingsPlugins(await fetch("/api/plugins").then((r) => r.json()));
  } catch (e) {
    settingsPluginsEl.textContent = "加载插件列表失败：" + e.message;
  }
}

function closeSettings() {
  settingsView.classList.add("hidden");
}

function renderSettingsPlugins(list) {
  settingsPluginsEl.textContent = "";
  for (const p of list) {
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
    head.append(name, label);
    card.appendChild(head);

    const tags = document.createElement("div");
    tags.className = "plugin-card-tags";
    for (const t of p.tool_names || []) {
      const tag = document.createElement("span");
      tag.className = "tag";
      tag.textContent = "工具 " + t;
      tags.appendChild(tag);
    }
    if (p.has_prompt) {
      const tag = document.createElement("span");
      tag.className = "tag";
      tag.textContent = "注入提示词";
      tags.appendChild(tag);
    }
    if (tags.childElementCount > 0) card.appendChild(tags);

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

    settingsPluginsEl.appendChild(card);
  }
}

$("#btn-settings").addEventListener("click", openSettings);
$("#btn-settings-back").addEventListener("click", closeSettings);
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !settingsView.classList.contains("hidden")) closeSettings();
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
}

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
  sendMessage();
});

$("#btn-new").addEventListener("click", newSession);

loadSessions();
inputEl.focus();
