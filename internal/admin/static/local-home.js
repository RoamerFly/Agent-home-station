(() => {
  "use strict";
  const $ = (selector) => document.querySelector(selector);
  const state = { csrf: "", home: null, model: null, busy: false, chat: { items: [], nextCursor: "", hasMore: false, busy: false, loading: false, pending: new Map(), failed: new Map(), poll: 0 } };
  const loginView = $("#login-view"), homeView = $("#home-view"), localWorkspace = $("#local-workspace"), message = $("#page-message");
  const loginSubmit = $("#login-submit"), initializeButton = $("#initialize-button"), accessButton = $("#access-button"), logoutButton = $("#logout-button");
  const settingsAccessButton = $("#settings-access-button"), settingsLogoutButton = $("#settings-logout-button"), chatStream = $("#chat-stream");

  function showMessage(text, kind = "") { message.textContent = text || ""; message.className = `page-message${kind ? ` is-${kind}` : ""}`; }
  function safeServerMessage(body, fallback) { const value = body && (body.error || body.message || body.detail); return typeof value === "string" && value.trim() ? value.trim().slice(0, 280) : fallback; }
  function responseMessage(status) {
    if (status === 401 || status === 403) return "登录状态已失效，请重新验证。";
    if (status === 409) return "当前请求正在处理，或已被新的状态替代。";
    if (status === 503) return "还没有可用的聊天模型，请先完成模型设置。";
    if (status === 502) return "模型暂时没有返回结果。可以稍后手动重试这条消息。";
    return "暂时无法完成操作，请确认本机服务后再试。";
  }
  async function request(url, options = {}) {
    const headers = new Headers(options.headers || {});
    headers.set("Accept", "application/json");
    if (options.body) headers.set("Content-Type", "application/json");
    if (state.csrf && options.method && options.method !== "GET") headers.set("X-CSRF-Token", state.csrf);
    let response;
    try { response = await fetch(url, { ...options, headers, credentials: "same-origin" }); }
    catch (_) { const error = new Error("无法连接本机服务。"); error.status = 0; throw error; }
    let body = null;
    try { body = await response.json(); } catch (_) { /* Do not render non-JSON error bodies. */ }
    if (!response.ok) { const error = new Error(safeServerMessage(body, responseMessage(response.status))); error.status = response.status; error.body = body; throw error; }
    return body || {};
  }
  function authenticated(session) { return Boolean(session && (session.authenticated || session.logged_in || session.csrf)); }
  function setBusy(busy) {
    state.busy = busy;
    for (const node of [loginSubmit, initializeButton, accessButton, logoutButton, settingsAccessButton, settingsLogoutButton]) node.disabled = busy;
  }
  function chatConfigured() { return Boolean(state.model && state.model.key_configured); }
  function setActiveView(view) {
    const chat = view !== "settings";
    $("#chat-view").hidden = !chat; $("#settings-view").hidden = chat;
    $("#chat-tab").classList.toggle("is-active", chat); $("#settings-tab").classList.toggle("is-active", !chat);
    $("#chat-tab").setAttribute("aria-selected", String(chat)); $("#settings-tab").setAttribute("aria-selected", String(!chat));
  }
  function showLogin() {
    state.home = null; state.model = null; window.clearTimeout(state.chat.poll);
    document.body.classList.remove("is-home-ready");
    localWorkspace.hidden = true; homeView.hidden = true; loginView.hidden = false; $("#login-password").value = "";
  }
  function shortId(id, fallback) { const value = String(id || ""); return value ? `${value.slice(0, 11)}…${value.slice(-6)}` : fallback; }
  function showHome(home) {
    state.home = home; loginView.hidden = true; homeView.hidden = false;
    const initialized = Boolean(home.initialized), enabled = Boolean(home.home_access), chatAvailable = home.chat_available !== false;
    $("#status-kicker").textContent = initialized ? "本地相处" : "准备中";
    $("#status-title").textContent = initialized ? "大肥鱼在家" : "为大肥鱼准备家";
    $("#stage-note").textContent = !initialized
      ? "准备家目录后，就能和她聊天，并让她整理自己的文件与工具。"
      : chatAvailable
        ? enabled
          ? "她可以整理家目录中的文件、项目与工具；程序执行需要已配置的 Windows 运行器。"
          : "聊天始终可用；工作空间已暂停，文件操作与新程序启动已停止。"
        : "本地聊天请从独立 Agent-home-station 服务进入。";
    initializeButton.hidden = initialized; $("#setup-actions").hidden = initialized && chatAvailable; localWorkspace.hidden = !initialized || !chatAvailable;
    document.body.classList.toggle("is-home-ready", initialized && chatAvailable);
    for (const button of [accessButton, settingsAccessButton]) {
      button.hidden = !initialized && button === accessButton;
      button.textContent = enabled ? "暂停工作空间授权" : "恢复工作空间授权";
      button.className = `button ${enabled ? "secondary" : "primary"}${button === settingsAccessButton ? " compact" : ""}`;
    }
    const chip = $("#access-chip");
    chip.textContent = !initialized ? "尚未准备" : enabled ? "工作空间已授权" : "工作空间已暂停";
    chip.classList.toggle("is-live", enabled);
    $("#home-directory").textContent = home.directory || "专属目录正在准备";
    $("#fish-id").textContent = shortId(home.fish_id, "大肥鱼"); $("#fish-id").title = home.fish_id || "大肥鱼";
    $("#owner-id").textContent = shortId(home.owner_id, "本机主人"); $("#owner-id").title = home.owner_id || "本机主人";
  }
  async function loadHome() {
    const home = await request("/api/local/home");
    showHome(home);
    if (home.initialized && home.chat_available !== false) await Promise.all([loadModel(state.model === null), loadChat(true)]);
  }
  function setModelForm(model) {
    const source = model || {};
    $("#model-revision").value = String(source.revision || 0);
    $("#model-provider").value = source.provider || "deepseek";
    $("#model-base-url").value = source.base_url || "https://api.deepseek.com";
    $("#model-name").value = source.model || "deepseek-flash";
    $("#model-max-tokens").value = String(source.max_tokens || 4096);
    $("#model-thinking").checked = source.thinking_enabled !== false;
    $("#model-reasoning").value = source.reasoning_effort || "low";
    $("#model-persona").value = source.persona || "";
    $("#model-api-key").value = ""; $("#model-api-key").placeholder = source.key_configured ? "已保存；留空不改" : "填写后保存";
    $("#model-clear-key").checked = false;
  }
  async function loadModel(syncForm = false) {
    try {
      state.model = await request("/api/local/model");
      if (syncForm) setModelForm(state.model);
      $("#model-setup-note").hidden = chatConfigured(); $("#open-settings-button").hidden = chatConfigured();
    } catch (error) {
      state.model = null; $("#model-setup-note").hidden = false; $("#open-settings-button").hidden = false;
      if (error.status !== 404) showMessage(error.message, "warn");
    }
  }
  function messageTime(value) {
    if (!value) return "";
    const date = new Date(value); if (Number.isNaN(date.getTime())) return String(value).slice(0, 16);
    return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" }).format(date);
  }
  function itemText(item) { return typeof item.text === "string" ? item.text : (typeof item.content === "string" ? item.content : ""); }
  function renderFailedDraft(requestID, failure) {
    const bubble = document.createElement("article"), meta = document.createElement("div"), body = document.createElement("p"), line = document.createElement("div"), detail = document.createElement("span"), retry = document.createElement("button");
    bubble.className = "chat-bubble is-user";
    meta.className = "bubble-meta"; meta.textContent = "你";
    body.className = "bubble-text"; body.textContent = failure.text;
    line.className = "bubble-failure"; detail.textContent = failure.message;
    retry.type = "button"; retry.className = "text-action"; retry.textContent = "重试";
    retry.addEventListener("click", () => sendMessage(failure.text, requestID));
    line.append(detail, retry); bubble.append(meta, body, line);
    chatStream.append(bubble);
  }
  function renderPendingDraft(failure) {
    const bubble = document.createElement("article"), meta = document.createElement("div"), body = document.createElement("p");
    bubble.className = "chat-bubble is-user is-pending";
    meta.className = "bubble-meta"; meta.textContent = "你 · 正在提交";
    body.className = "bubble-text"; body.textContent = failure.text;
    bubble.append(meta, body); chatStream.append(bubble);
  }
  function renderChat() {
    const previousScroll = chatStream.scrollTop;
    chatStream.replaceChildren();
    if (!state.chat.items.length) {
      const empty = document.createElement("div"), title = document.createElement("strong"), hint = document.createElement("span");
      empty.className = "chat-empty"; title.textContent = "从一句话开始"; hint.textContent = "大肥鱼会在这里记下这段对话。";
      empty.append(title, hint);
      chatStream.append(empty);
    }
    const latestAssistant = state.chat.items.reduce((latest, item, index) => item.role === "assistant" ? index : latest, -1);
    let recoveryID = "";
    if (!state.chat.busy && !state.chat.pending.size) {
      for (let index = state.chat.items.length - 1; index > latestAssistant; index -= 1) {
        const item = state.chat.items[index];
        if (item.role !== "assistant" && item.request_id) { recoveryID = item.request_id; break; }
      }
    }
    for (const item of state.chat.items) {
      const assistant = item.role === "assistant", bubble = document.createElement("article");
      bubble.className = `chat-bubble is-${assistant ? "assistant" : "user"}`;
      if (!assistant && state.chat.pending.has(item.request_id)) bubble.classList.add("is-pending");
      const meta = document.createElement("div"); meta.className = "bubble-meta"; meta.textContent = assistant ? "大肥鱼" : "你";
      const time = messageTime(item.time || item.created_at);
      if (time) { const timeNode = document.createElement("time"); timeNode.textContent = time; meta.append(timeNode); }
      const body = document.createElement("p"); body.className = "bubble-text"; body.textContent = itemText(item); bubble.append(meta, body);
      if (!assistant && (state.chat.failed.has(item.request_id) || item.request_id === recoveryID)) {
        const failure = state.chat.failed.get(item.request_id) || { message: "未收到回复。" }, line = document.createElement("div"), detail = document.createElement("span"), retry = document.createElement("button");
        line.className = "bubble-failure"; detail.textContent = failure.message; retry.type = "button"; retry.className = "text-action"; retry.textContent = "重试";
        retry.addEventListener("click", () => sendMessage(itemText(item), item.request_id)); line.append(detail, retry); bubble.append(line);
      }
      chatStream.append(bubble);
    }
    const persistedRequestIDs = new Set(state.chat.items.filter((item) => item.role !== "assistant").map((item) => item.request_id));
    for (const [requestID, pending] of state.chat.pending) {
      if (!persistedRequestIDs.has(requestID)) renderPendingDraft(pending);
    }
    for (const [requestID, failure] of state.chat.failed) {
      if (!persistedRequestIDs.has(requestID)) renderFailedDraft(requestID, failure);
    }
    $("#load-older-button").hidden = !state.chat.hasMore || state.chat.loading;
    $("#chat-progress").textContent = state.chat.pending.size ? "正在等待回复…" : "";
    chatStream.setAttribute("aria-busy", String(state.chat.loading || state.chat.pending.size > 0));
    chatStream.scrollTop = previousScroll;
  }
  function mergeItems(incoming, replace) {
    const mapped = replace ? new Map() : new Map(state.chat.items.map((item) => [item.id || `${item.role}/${item.time}/${item.text}`, item]));
    for (const item of incoming) mapped.set(item.id || `${item.role}/${item.time}/${item.text}`, item);
    state.chat.items = Array.from(mapped.values()).sort((a, b) => String(a.time || a.created_at || "").localeCompare(String(b.time || b.created_at || "")));
  }
  async function loadChat(replace = false) {
    if (!state.home || !state.home.initialized || state.chat.loading) return;
    state.chat.loading = true; renderChat();
    try {
      const query = new URLSearchParams({ limit: "40" }); if (!replace && state.chat.nextCursor) query.set("cursor", state.chat.nextCursor);
      const data = await request(`/api/local/chat?${query.toString()}`);
      mergeItems(Array.isArray(data.items) ? data.items : [], replace); state.chat.nextCursor = data.next_cursor || ""; state.chat.hasMore = Boolean(data.has_more);
      state.chat.busy = Boolean(data.busy);
      const settled = new Set(state.chat.items.filter((item) => item.role === "assistant" && item.request_id).map((item) => item.request_id));
      for (const requestID of settled) { state.chat.pending.delete(requestID); state.chat.failed.delete(requestID); }
    } catch (error) { if (error.status !== 404) showMessage(error.message, "warn"); }
    finally { state.chat.loading = false; renderChat(); }
  }
  function schedulePoll() {
    window.clearTimeout(state.chat.poll); if (!state.chat.pending.size) return;
    state.chat.poll = window.setTimeout(async () => { await loadChat(true); if (state.chat.pending.size) schedulePoll(); }, 1600);
  }
  function newRequestID() { return window.crypto && typeof window.crypto.randomUUID === "function" ? window.crypto.randomUUID() : `local-${Date.now()}-${Math.random().toString(16).slice(2)}`; }
  async function sendMessage(raw, requestID = newRequestID()) {
    const text = String(raw || ""); if (!text.trim() || state.chat.pending.has(requestID)) return;
    if (!chatConfigured()) { setActiveView("settings"); showMessage("先保存可用的聊天模型，再发送消息。", "warn"); return; }
    state.chat.pending.set(requestID, { text }); state.chat.failed.delete(requestID); $("#chat-input").value = ""; renderChat();
    // Only an explicit send/retry brings its result into view. Background
    // refreshes and reading older pages preserve the user's scroll position.
    chatStream.scrollTop = chatStream.scrollHeight;
    schedulePoll();
    try {
      const result = await request("/api/local/chat", { method: "POST", body: JSON.stringify({ request_id: requestID, text }) });
      if (result.status === "completed") state.chat.pending.delete(requestID);
      if (result.status === "no_reply") {
        state.chat.pending.delete(requestID);
        showMessage("本轮没有生成回复。", "warn");
      }
      await loadChat(true);
    } catch (error) {
      state.chat.pending.delete(requestID); state.chat.failed.set(requestID, { text, message: error.message || responseMessage(error.status) });
      await loadChat(true); renderChat();
    } finally { chatStream.scrollTop = chatStream.scrollHeight; schedulePoll(); }
  }
  async function loadSession() {
    try {
      const session = await request("/api/session"); state.csrf = String(session.csrf || session.csrf_token || "");
      if (!authenticated(session)) { showLogin(); return; } await loadHome();
    } catch (error) { if (error.status === 401 || error.status === 403) { state.csrf = ""; showLogin(); return; } showLogin(); showMessage("暂时无法连接本机服务，请稍后再试。", "error"); }
  }
  async function saveHome(action) {
    if (state.busy || !state.home) return; setBusy(true); showMessage("");
    try {
      const payload = { action }; if (action !== "initialize") payload.revision = Number(state.home.revision || 0);
      await request("/api/local/home", { method: "POST", body: JSON.stringify(payload) }); await loadHome();
      showMessage(action === "initialize" ? "大肥鱼的家已经准备好。" : "工作空间授权已更新。", "success");
    } catch (error) {
      if (error.status === 409) { try { await loadHome(); } catch (_) {} showMessage("状态已经更新，已刷新当前信息。", "warn"); }
      else if (error.status === 401 || error.status === 403) { state.csrf = ""; showLogin(); showMessage("登录状态已失效，请重新验证。", "warn"); }
      else showMessage(error.message || responseMessage(error.status), "error");
    } finally { setBusy(false); }
  }
  async function saveModel(event) {
    event.preventDefault();
    const payload = { revision: Number($("#model-revision").value || 0), provider: $("#model-provider").value, base_url: $("#model-base-url").value.trim(), model: $("#model-name").value.trim(), max_tokens: Number($("#model-max-tokens").value), thinking_enabled: $("#model-thinking").checked, reasoning_effort: $("#model-reasoning").value, persona: $("#model-persona").value.trim(), api_key: $("#model-api-key").value, clear_key: $("#model-clear-key").checked };
    const button = $("#model-save"); button.disabled = true;
    try {
      state.model = await request("/api/local/model", { method: "PUT", body: JSON.stringify(payload) }); setModelForm(state.model);
      $("#model-setup-note").hidden = chatConfigured(); $("#open-settings-button").hidden = chatConfigured(); showMessage("聊天模型已保存。", "success"); setActiveView("chat");
    } catch (error) {
      if (error.status === 409 && /已更新/.test(String(error.body && error.body.error || error.message))) {
        try {
          const latest = await request("/api/local/model");
          state.model = latest;
          $("#model-revision").value = String(latest.revision || 0);
          $("#model-api-key").placeholder = latest.key_configured ? "已保存；留空不改" : "填写后保存";
        } catch (_) { /* Keep the user's draft when a refresh is unavailable. */ }
        showMessage("设置已在别处更新。已保留这次编辑，请确认后重新保存。", "warn");
      }
      else showMessage(error.message || responseMessage(error.status), "error");
    } finally { button.disabled = false; }
  }
  async function logout() {
    if (state.busy) return; setBusy(true);
    try { await request("/api/logout", { method: "POST" }); state.csrf = ""; showLogin(); showMessage("已退出登录。", "success"); }
    catch (_) { showMessage("退出登录未完成，请重试。", "error"); } finally { setBusy(false); }
  }
  $("#login-form").addEventListener("submit", async (event) => {
    event.preventDefault(); if (state.busy) return; setBusy(true); showMessage("");
    try { const session = await request("/api/login", { method: "POST", body: JSON.stringify({ password: $("#login-password").value }) }); state.csrf = String(session.csrf || session.csrf_token || ""); await loadHome(); showMessage("验证完成。", "success"); }
    catch (error) { showMessage(error.status === 401 || error.status === 403 ? "密码不正确，请重新输入。" : (error.message || responseMessage(error.status)), "error"); } finally { setBusy(false); }
  });
  initializeButton.addEventListener("click", () => saveHome("initialize"));
  accessButton.addEventListener("click", () => saveHome(state.home && state.home.home_access ? "pause" : "authorize"));
  settingsAccessButton.addEventListener("click", () => saveHome(state.home && state.home.home_access ? "pause" : "authorize"));
  logoutButton.addEventListener("click", logout); settingsLogoutButton.addEventListener("click", logout); $("#local-logout-button").addEventListener("click", logout);
  $("#chat-tab").addEventListener("click", () => setActiveView("chat")); $("#settings-tab").addEventListener("click", () => setActiveView("settings"));
  $("#open-settings-button").addEventListener("click", () => setActiveView("settings")); $("#back-to-chat-button").addEventListener("click", () => setActiveView("chat"));
  $("#model-form").addEventListener("submit", saveModel); $("#chat-form").addEventListener("submit", (event) => { event.preventDefault(); sendMessage($("#chat-input").value); });
  $("#load-older-button").addEventListener("click", () => loadChat(false));
  loadSession();
})();
