(() => {
  "use strict";
  const $ = (selector) => document.querySelector(selector);
  const state = { csrf: "", home: null, model: null, busy: false, chat: { items: [], nextCursor: "", hasMore: false, busy: false, loading: false, pending: new Map(), failed: new Map(), poll: 0 } };
  const tabs = ["chat", "apps", "project-tools", "settings"];
  let activeView = "chat";
  let apps = newAppView();
  let projectTools = newProjectToolsView();
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
    activeView = view;
    for (const name of tabs) {
      const tab = $(`#${name}-tab`), selected = name === view;
      $(`#${name}-view`).hidden = !selected;
      tab.classList.toggle("is-active", selected);
      tab.setAttribute("aria-selected", String(selected));
      tab.tabIndex = selected ? 0 : -1;
    }
    if (view === "apps") { loadApps(); scheduleAppLogin(apps); }
    else window.clearTimeout(apps.loginTimer);
    if (view === "project-tools") loadProjectTools();
  }
  function newAppView() {
    return { loading: false, installing: "", catalog: null, registered: null, connections: new Map(), accountStatuses: new Map(), setupBusy: false, provisionKey: "", logins: new Map(), selectedLogin: "", loginTimer: 0, workerDrafts: new Map(), historyDrafts: new Map(), attachmentDrafts: new Map(), mediaReplyDrafts: new Map(), groupMuteDrafts: new Map(), groupRuleDrafts: new Map(), workDrafts: new Map(), resumeDrafts: new Map(), editWorker: "" };
  }
  function newProjectToolsView() { return { loading: false, applying: "", tools: [] }; }
  function appElement(tag, text, className = "") {
    const node = document.createElement(tag);
    node.textContent = text;
    if (className) node.className = className;
    return node;
  }
  function appFeedback(text, warn = false) {
    const feedback = $("#apps-feedback");
    feedback.textContent = text;
    feedback.classList.toggle("is-warn", warn);
  }
  function projectToolsFeedback(text, warn = false) {
    const feedback = $("#project-tools-feedback");
    feedback.textContent = text || "";
    feedback.classList.toggle("is-warn", Boolean(warn));
  }
  function appSessionExpired(results) {
    if (!results.some((result) => result.status === "rejected" && result.reason?.status === 401)) return false;
    state.csrf = "";
    showLogin();
    showMessage("登录状态已失效，请重新验证。", "warn");
    return true;
  }
  function appRows(result, key) {
    if (result.status !== "fulfilled" || !Array.isArray(result.value[key])) return null;
    return result.value[key].filter((item) => item && typeof item.id === "string" && item.id);
  }
  function appSourceRow(record, listing) {
    const row = appElement("div", "", "app-layer");
    const detail = appElement("div", "");
    let title = "源码状态未知", note = "刷新后可重新检查。";
    if (listing) {
      const version = listing.installed_version || listing.version;
      if (listing.install_state === "installed") {
        title = "源码已安装";
        note = version ? `本地版本 ${version}，可在家目录的 Connected Apps 中编辑。` : "可在家目录的 Connected Apps 中编辑。";
      } else if (listing.install_state === "conflict") {
        title = "应用目录待整理";
        note = listing.installed_version ? `已有版本 ${listing.installed_version}，请先保留现有修改。` : "家目录已有同名内容，请先保留并整理该目录。";
      } else if (!listing.source_available) {
        title = "未提供源码";
        note = "当前发行包未提供可用源码，本地聊天可继续使用。";
      } else if (listing.install_state === "not_installed") {
        title = "可安装";
        note = state.home?.home_access ? `随包版本 ${listing.version}，安装后仍需单独接入账号。` : "请在模型设置中恢复工作空间授权。";
      } else if (listing.install_state === "unavailable") {
        title = "安装状态暂不可读";
        note = "请检查工作空间是否可用。";
      }
    } else if (apps.catalog) {
      title = "未提供源码";
      note = "当前发行包没有此应用的源码。";
    }
    detail.append(appElement("strong", title), appElement("p", note));
    row.append(appElement("h5", "应用源码"), detail);
    if (title === "可安装" && state.home?.home_access) {
      const busy = apps.installing === record.id;
      const button = appElement("button", busy ? "正在安装…" : "安装源码", "button compact primary");
      button.type = "button";
      button.disabled = Boolean(apps.installing) || apps.loading;
      button.addEventListener("click", () => installApp(listing));
      row.append(button);
    }
    return row;
  }
  function appSetupButton(text, action) {
    const button = appElement("button", text, "button compact secondary");
    button.type = "button";
    button.disabled = apps.setupBusy || apps.loading;
    button.addEventListener("click", action);
    return button;
  }
  const appLoginLabels = {
    starting: "正在生成二维码…", awaiting_scan: "请用微信扫描二维码",
    awaiting_approval: "请在微信上确认登录", succeeded: "微信登录已确认",
    expired: "二维码已过期", failed: "微信登录失败", cancelled: "微信登录已取消",
    rejected: "微信登录未获确认", needs_login: "需要重新扫码", challenge_required: "请在手机上核对登录验证",
    approval_required: "请在手机上核对登录验证", needs_review: "请在手机上核对登录验证", unknown: "微信登录状态暂不可读"
  };
  const pendingAppLogin = (op) => op && ["starting", "awaiting_scan", "awaiting_approval"].includes(op.state);
  const appLoginPath = (id) => "/api/apps/wechat/connections/" + encodeURIComponent(id) + "/login";
  const newAppSetupKey = () => crypto.randomUUID().replaceAll("-", "");
  const appSetupPost = (path, key) => request(path, { method: "POST", body: JSON.stringify({ request_id: key }) });

  function renderAppLogin(id, op) {
    const panel = appElement("div", "", "app-login");
    panel.append(appElement("p", appLoginLabels[op.state] || appLoginLabels.unknown));
    if (op.qr_available && op.state === "awaiting_scan") {
      const image = document.createElement("img");
      image.src = appLoginPath(id) + "/" + encodeURIComponent(op.id) + "/qr?generation=" + op.qr_generation;
      image.alt = "微信登录二维码";
      image.addEventListener("error", () => {
        image.remove();
        panel.append(appElement("p", "二维码暂不可读，可重新查看当前登录。"));
      }, { once: true });
      panel.append(image);
    }
    return panel;
  }
  function scheduleAppLogin(current, delay = 2200) {
    window.clearTimeout(current.loginTimer);
    const id = current.selectedLogin, entry = current.logins.get(id);
    if (current !== apps || activeView !== "apps" || !pendingAppLogin(entry?.op)) return;
    current.loginTimer = window.setTimeout(() => pollAppLogin(current, id, entry), delay);
  }
  async function pollAppLogin(current, id, entry) {
    const before = entry.op;
    try {
      const op = await request(appLoginPath(id) + "/" + encodeURIComponent(before.id));
      if (current !== apps || current.selectedLogin !== id || entry.op !== before || op.id !== before.id) return;
      entry.op = op;
      if (op.state !== before.state || op.qr_available !== before.qr_available || op.qr_generation !== before.qr_generation) renderApps();
      if (op.state === "succeeded") await loadApps();
    } catch (error) {
      if (current !== apps || current.selectedLogin !== id || appSessionExpired([{ status: "rejected", reason: error }])) return;
      if (error.status === 404) {
        entry.op = { ...before, state: "unknown", qr_available: false };
        renderApps();
      }
      appFeedback("暂时无法读取微信登录状态，可稍后重新查看。", true);
      scheduleAppLogin(current, 5000);
      return;
    }
    scheduleAppLogin(current);
  }
  async function runAppSetup(id = "") {
    const current = apps;
    if (current.setupBusy || current.loading) return;
    current.setupBusy = true;
    if (id) current.selectedLogin = id;
    appFeedback("");
    renderApps();
    try {
      if (!id) {
        current.provisionKey ||= newAppSetupKey();
        await appSetupPost("/api/apps/wechat/connections", current.provisionKey);
        current.provisionKey = "";
        if (current !== apps) return;
        appFeedback("账号已登记，可以扫码登录。");
        await loadApps();
      } else {
        let entry = current.logins.get(id);
        if (!entry || entry.op && !pendingAppLogin(entry.op)) {
          entry = { key: newAppSetupKey(), op: null };
          current.logins.set(id, entry);
        }
        if (entry.op) {
          await pollAppLogin(current, id, entry);
        } else {
          entry.op = await appSetupPost(appLoginPath(id), entry.key);
          scheduleAppLogin(current);
        }
      }
    } catch (error) {
      if (current === apps && !appSessionExpired([{ status: "rejected", reason: error }])) appFeedback(error.message, true);
    } finally {
      current.setupBusy = false;
      if (current === apps) renderApps();
    }
  }
  const appWorkerLabels = {
    disabled: "Agent 未启动", needs_setup: "Agent 待配置", waiting_login: "Agent 等待登录",
    running: "Agent 运行中", stopping: "Agent 正在停止", paused: "Agent 已暂停", error: "Agent 运行异常"
  };
  function legacyImportControls(id, availability, connection = {}) {
    if (!availability) return null;
    const panel = appElement("details", "", "identity-details legacy-import");
    panel.append(appElement("summary", "从旧版接入"));
    const body = appElement("div", "", "details-grid");
    const stateCopy = {
      available: "发现此账号的旧版隔离资料。先查看预演，确认后才会保存接入摘要。",
      conflict: "此账号已有新的工作记录，旧版资料不能覆盖接入。",
      applied: "旧版接入摘要已保存，可随时查看。",
      invalid: "旧版隔离资料无法通过身份或路径核验，不能接入。"
    };
    let plan = null, loading = false;
    const note = appElement("small", stateCopy[availability] || "旧版接入状态暂不可读。");
    const result = appElement("div", "");
    const memoryResult = appElement("div", "", "legacy-memory-result");
    const actions = appElement("div", "", "app-worker-actions");
    const legacyImportURL = "/api/apps/wechat/connections/" + encodeURIComponent(id) + "/legacy-import";
    const preview = appSetupButton(availability === "applied" ? "查看接入摘要" : "查看接入预演", async () => {
      if (loading) return;
      loading = true;
      actions.querySelector("[data-legacy-confirm]")?.remove();
      preview.disabled = true;
      result.replaceChildren(appElement("small", "正在读取旧版接入信息…"));
      try {
        plan = await request(legacyImportURL);
        renderPlan();
      } catch (error) {
        plan = null;
        actions.querySelector("[data-legacy-confirm]")?.remove();
        result.replaceChildren(appElement("small", error.message));
      } finally {
        loading = false;
        preview.disabled = false;
      }
    });
    function list(label, values) {
      const reviewCopy = {
        legacy_text_reply_disabled: "旧版文本回复未开启",
        custom_filehelper_prefix: "旧版文件传输助手使用了自定义前缀",
        all_friends: "旧版允许所有好友回复",
        group_rule: "旧版包含群聊规则"
      };
      const items = (Array.isArray(values) ? values : []).filter(Boolean).map((item) => reviewCopy[item] || item);
      return appElement("p", `${label}：${items.length ? items.join("、") : "无"}`);
    }
    function renderPlan(message = "") {
      const value = plan || {};
      const cutover = value.cutover && typeof value.cutover === "object" ? value.cutover : null;
      const cutoverCopy = {
        legacy_host: {
          stop_record_matched: "已记录正常停机",
          pending: "尚未取得停机记录",
          changed: "停机记录与当前快照不一致"
        },
        native_profile: {
          import_receipt_matched: "导入记录与当前连接匹配",
          pending: "尚未取得档案导入记录",
          mismatch: "导入记录与当前连接不匹配"
        },
        target_login: {
          online: "已登录且在线",
          authenticated_offline: "已登录，当前离线",
          signed_out: "尚未登录",
          pending: "待核对"
        }
      };
      const cutoverLines = cutover ? [
        appElement("p", `旧宿主：${cutoverCopy.legacy_host[cutover.legacy_host] || "待核对"}`),
        appElement("p", `原生档案：${cutoverCopy.native_profile[cutover.native_profile] || "待核对"}`),
        appElement("p", `目标登录：${cutoverCopy.target_login[cutover.target_login] || "待核对"}`),
        appElement("p", "身份确认：仍需主人确认实际微信身份")
      ] : [appElement("p", "切换前置检查：旧记录未保存，仍需主人逐项核对")];
      result.replaceChildren(
        appElement("strong", value.applied ? "已保存的接入摘要" : "接入预演"),
        appElement("p", `账号：${value.account_id || "未知"}`),
        list("建议回复范围", value.suggested_reply_threads),
        list("仍需核对", value.requires_manual_review),
        appElement("p", `可读回执：${Number(value.receipt_records || 0)}；待核对回执：${Number(value.unresolved_receipts || 0)}`),
        appElement("p", `原生资料集：${value.same_native_dataset ? "相同，可查询既有操作记录" : "不同或未知，不会覆盖当前资料"}`),
        appElement("p", `旧版状态：${value.legacy_enabled ? "已启用" : "未启用"}${value.legacy_paused ? "，已暂停" : ""}`),
        ...cutoverLines,
        message ? appElement("small", message) : document.createTextNode("")
      );
      const confirm = actions.querySelector("[data-legacy-confirm]");
      if (confirm) confirm.remove();
      if (availability !== "available" || value.applied || !value.digest) return;
      const apply = appSetupButton("确认接入旧版摘要", async () => {
        if (loading) return;
        loading = true;
        apply.disabled = true;
        try {
          plan = await request("/api/apps/wechat/connections/" + encodeURIComponent(id) + "/legacy-import", {
            method: "POST", body: JSON.stringify({ digest: value.digest })
          });
          note.textContent = "旧版接入摘要已保存，可随时查看。";
          preview.textContent = "查看接入摘要";
          renderPlan("旧版摘要已保存；没有启动 Agent，也没有修改回复范围。");
        } catch (error) {
          if (error.status === 409) {
            plan = null;
            apply.remove();
            result.replaceChildren(appElement("small", "旧版资料或当前状态已变化，请重新读取接入预演。"));
          } else result.replaceChildren(appElement("small", error.message));
        } finally {
          loading = false;
          apply.disabled = false;
        }
      });
      apply.dataset.legacyConfirm = "true";
      actions.append(apply);
    }
    function currentImportPrerequisites() {
      const policy = connection.configured_policy || {};
      const worker = appWorkerLabels[connection.worker] || "Agent 状态待核对";
      const history = Array.isArray(policy.history_threads) ? policy.history_threads.length : 0;
      return `当前 Agent：${worker}；已配置历史读取会话：${history} 个。`;
    }
    function renderMemorySelection(thread, panel, value) {
      const statusCopy = {
        ready: "可接续此会话",
        applied: "此会话已接续",
        not_ready: "暂不能接续此会话",
        conflict: "此会话接续存在冲突",
        empty: "此会话没有可接续的内容"
      };
      const status = value && typeof value.status === "string" ? value.status : "";
      const lines = [
        appElement("strong", statusCopy[status] || "接续状态待核对"),
        appElement("small", `来源事件：${Number(value?.event_count || 0)}；可接续记忆：${Number(value?.memory_count || 0)}`),
        appElement("small", currentImportPrerequisites())
      ];
      if (value?.reason) lines.push(appElement("small", `说明：${value.reason}`));
      if (status !== "ready") {
        panel.replaceChildren(...lines);
        return;
      }
      if (!value.digest) {
        lines.push(appElement("small", "本次预演缺少校验标识，请重新读取后再试。"));
        panel.replaceChildren(...lines);
        return;
      }
      const confirmID = `legacy-memory-confirm-${String(thread).replace(/[^a-zA-Z0-9_-]/g, "-")}`;
      const confirmation = document.createElement("input");
      confirmation.type = "checkbox";
      confirmation.id = confirmID;
      const label = document.createElement("label");
      label.htmlFor = confirmID;
      label.textContent = "我已确认旧资料和当前目标是同一个实际微信账号";
      const apply = appSetupButton("确认接续此会话", async () => {
        apply.disabled = true;
        try {
          const applied = await request(legacyImportURL + "/memory/" + encodeURIComponent(thread), {
            method: "POST", body: JSON.stringify({ digest: value.digest, same_identity: true })
          });
          renderMemorySelection(thread, panel, applied);
        } catch (error) {
          if (error.status === 409) {
            panel.replaceChildren(
              appElement("small", "旧资料或当前目标已变化，请重新预演此会话。"),
              appSetupButton("重新预演此会话", () => readMemorySelection(thread, panel))
            );
          } else {
            lines.push(appElement("small", error.message));
            panel.replaceChildren(...lines);
          }
        }
      });
      apply.disabled = true;
      confirmation.addEventListener("change", () => { apply.disabled = !confirmation.checked; });
      const confirmRow = appElement("div", "", "legacy-memory-confirm");
      confirmRow.append(confirmation, label, apply);
      lines.push(confirmRow);
      panel.replaceChildren(...lines);
    }
    async function readMemorySelection(thread, panel) {
      panel.replaceChildren(appElement("small", "正在预演接续此会话…"));
      try {
        renderMemorySelection(thread, panel, await request(legacyImportURL + "/memory/" + encodeURIComponent(thread)));
      } catch (error) {
        panel.replaceChildren(appElement("small", error.message));
      }
    }
    function renderMemoryReport(value) {
      const statusCopy = {
        ready: "旧会话记忆可核对",
        missing: "未找到可核对的旧会话记忆",
        snapshot_incomplete: "旧会话快照不完整，请使用正常关闭后的一致副本",
        unsupported: "当前旧资料格式不支持会话记忆核对",
        unavailable: "旧会话记忆暂不可读"
      };
      const conversations = Array.isArray(value?.conversations) ? value.conversations : [];
      const rows = conversations.map((conversation) => {
        const row = appElement("div", "", "legacy-memory-scope");
        const thread = typeof conversation.thread_id === "string" ? conversation.thread_id : "";
        row.append(appElement("p",
          `会话：${thread || "未知"}；来源候选：${Number(conversation.event_count || 0)}；已排除：${Number(conversation.withheld_events || 0)}；记忆：${Number(conversation.memory_count || 0)}；摘要：${conversation.has_summary ? "有" : "无"}；当前目标历史授权：${conversation.history_granted ? "已授权" : "未授权"}`));
        if (conversation.history_granted && thread) {
          const selection = appElement("div", "", "legacy-memory-selection");
          row.append(appSetupButton("预演接续此会话", () => readMemorySelection(thread, selection)), selection);
        } else {
          row.append(appElement("small", "接续此会话前，请先在当前连接中授权历史读取。"));
        }
        return row;
      });
      const next = typeof value?.next_after === "string" && value.next_after ? appSetupButton("读取下一页会话", () => readLegacyMemory(value.next_after)) : null;
      memoryResult.replaceChildren(
        appElement("strong", statusCopy[value?.status] || "旧会话记忆状态待核对"),
        value?.status === "ready" ? appElement("p", `匹配范围：${Number(value?.matched_scopes || 0)}；未映射范围：${Number(value?.unmapped_scopes || 0)}；外部范围：${Number(value?.foreign_scopes || 0)}`) : document.createTextNode(""),
        appElement("small", "历史授权仅表示当前目标连接的读取权限，不代表可读取旧版资料。"),
        ...rows,
        next || document.createTextNode("")
      );
    }
    let memoryLoading = false;
    async function readLegacyMemory(after = "") {
      if (memoryLoading) return;
      memoryLoading = true;
      memoryResult.replaceChildren(appElement("small", "正在核对旧会话记忆…"));
      try {
        const suffix = after ? "?after=" + encodeURIComponent(after) : "";
        renderMemoryReport(await request(legacyImportURL + "/memory" + suffix));
      } catch (error) {
        memoryResult.replaceChildren(appElement("small", error.message));
      } finally {
        memoryLoading = false;
      }
    }
    if (["available", "applied", "conflict"].includes(availability)) {
      actions.append(appSetupButton("核对旧会话记忆", () => readLegacyMemory()));
    }
    actions.append(preview);
    body.append(note, actions, result, memoryResult);
    panel.append(body);
    return panel;
  }
  function appWorkerControls(id, status) {
    const panel = appElement("div", "", "app-worker");
    const buttons = appElement("div", "", "app-worker-actions");
    const running = ["running", "waiting_login", "stopping"].includes(status.worker);
    const edit = appSetupButton(apps.editWorker === id ? "收起回复范围" : "设置回复范围", () => {
      apps.editWorker = apps.editWorker === id ? "" : id;
      renderApps();
    });
    buttons.append(appElement("small", appWorkerLabels[status.worker]), edit);
    const toggle = appSetupButton(running ? "暂停 Agent" : "启动 Agent",
      () => runWorkerAction(id, "POST", "lifecycle", { action: running ? "stop" : "start" }));
    toggle.disabled ||= status.worker === "stopping" || !running && !status.configured_policy?.conversation_enabled;
    buttons.append(toggle);
    const legacyImport = legacyImportControls(id, status.legacy_import, status);
    panel.append(buttons);
    if (legacyImport) panel.append(legacyImport);
    panel.append(contactDirectoryControls(id), groupDirectoryControls(id), deliveryRecords(id), stationToolControls(id, false), stationToolControls(id, true));
    if (apps.editWorker !== id) return panel;
    const field = document.createElement("textarea");
    field.rows = 3;
    field.placeholder = "filehelper，或微信用户 / 群 ID";
    field.setAttribute("aria-label", "回复会话 ID");
    field.value = apps.workerDrafts.get(id) ?? (status.configured_policy?.reply_threads || []).join("\n");
    field.addEventListener("input", () => apps.workerDrafts.set(id, field.value));
    const history = document.createElement("textarea");
    history.rows = 2;
    history.placeholder = "可留空，或填写已允许回复的会话 ID";
    history.setAttribute("aria-label", "历史与成员读取会话 ID");
    history.value = apps.historyDrafts.get(id) ?? (status.configured_policy?.history_threads || []).join("\n");
    history.addEventListener("input", () => apps.historyDrafts.set(id, history.value));
    const attachments = document.createElement("textarea");
    attachments.rows = 2;
    attachments.placeholder = "可留空，或填写上方已允许回复的会话 ID";
    attachments.setAttribute("aria-label", "附件读取会话 ID");
    attachments.value = apps.attachmentDrafts.get(id) ?? (status.configured_policy?.attachment_threads || []).join("\n");
    attachments.addEventListener("input", () => apps.attachmentDrafts.set(id, attachments.value));
    const mediaReplies = document.createElement("textarea");
    mediaReplies.rows = 2;
    mediaReplies.placeholder = "可留空，或填入已允许读取附件的会话 ID";
    mediaReplies.setAttribute("aria-label", "附件回复会话 ID");
    mediaReplies.value = apps.mediaReplyDrafts.get(id) ?? (status.configured_policy?.media_reply_threads || []).join("\n");
    mediaReplies.addEventListener("input", () => apps.mediaReplyDrafts.set(id, mediaReplies.value));
    const contactRead = document.createElement("input");
    contactRead.type = "checkbox";
    contactRead.checked = Boolean(status.configured_policy?.contact_read);
    contactRead.setAttribute("aria-label", "允许大肥鱼在本地聊天中查阅此通讯录");
    const contactReadLabel = appElement("label", "", "switch-row");
    const contactReadCopy = appElement("span");
    contactReadCopy.append(appElement("strong", "允许大肥鱼在本地聊天中查阅此通讯录"),
      appElement("small", "主人可在应用页直接查看。大肥鱼查询时，所读资料会提供给当前聊天模型。"));
    contactReadLabel.append(contactRead, contactReadCopy);
    const groupMutes = document.createElement("textarea");
    groupMutes.rows = 2;
    groupMutes.placeholder = "每行一个群 ID；留空会撤销授权";
    groupMutes.setAttribute("aria-label", "Agent 免打扰群 ID");
    groupMutes.value = apps.groupMuteDrafts.get(id) ?? (status.configured_policy?.group_mute_threads || []).join("\n");
    groupMutes.addEventListener("input", () => apps.groupMuteDrafts.set(id, groupMutes.value));
    const groupRules = appElement("div", "", "group-rule-settings");
    const configuredRules = new Map((status.configured_policy?.group_rules || [])
      .filter((rule) => rule && typeof rule.thread_id === "string")
      .map((rule) => [rule.thread_id, { note: String(rule.note || ""), summarize_shares: Boolean(rule.summarize_shares) }]));
    const readGroupRule = (threadID) => apps.groupRuleDrafts.get(id)?.get(threadID) || configuredRules.get(threadID) || { note: "", summarize_shares: false };
    const updateGroupRule = (threadID, patch) => {
      let drafts = apps.groupRuleDrafts.get(id);
      if (!drafts) {
        drafts = new Map(configuredRules);
        apps.groupRuleDrafts.set(id, drafts);
      }
      drafts.set(threadID, { ...readGroupRule(threadID), ...patch });
    };
    const replyGroups = () => field.value.split(/[\s，,]+/u).filter((threadID) => threadID.endsWith("@chatroom"));
    const renderGroupRules = () => {
      const groups = replyGroups();
      groupRules.replaceChildren(appElement("strong", "群聊偏好"));
      if (!groups.length) {
        groupRules.append(appElement("small", "在回复范围中加入群聊后，可为该群写备注或开启分享总结。"));
        return;
      }
      for (const threadID of groups) {
        const value = readGroupRule(threadID);
        const row = appElement("div", "", "group-rule-row");
        const note = document.createElement("input");
        note.value = value.note;
        note.placeholder = "本群偏好（可选）";
        note.setAttribute("aria-label", threadID + " 群聊备注");
        note.addEventListener("input", () => updateGroupRule(threadID, { note: note.value }));
        const summarize = document.createElement("input");
        summarize.type = "checkbox";
        summarize.checked = value.summarize_shares;
        summarize.disabled = !attachmentThreadIDs().includes(threadID);
        summarize.setAttribute("aria-label", threadID + " 自动总结视频号分享");
        summarize.addEventListener("change", () => updateGroupRule(threadID, { summarize_shares: summarize.checked }));
        const summarizeLabel = appElement("label", "", "switch-row");
        summarizeLabel.append(summarize, appElement("span", "收到视频号分享时自动总结"));
        row.append(appElement("small", threadID), note, summarizeLabel);
        if (summarize.disabled) row.append(appElement("small", "需先在“附件读取会话”中授权本群读取视频号；不会自动开启。"));
        groupRules.append(row);
      }
    };
    const attachmentThreadIDs = () => attachments.value.split(/[\s，,]+/u).filter(Boolean);
    field.addEventListener("input", renderGroupRules);
    attachments.addEventListener("input", renderGroupRules);
    renderGroupRules();
    const save = appSetupButton("保存回复范围", () => {
      const reply_threads = field.value.split(/[\s，,]+/u).filter(Boolean);
      const history_threads = history.value.split(/[\s，,]+/u).filter(Boolean);
      const attachment_threads = attachmentThreadIDs();
      const revokedAttachmentThreads = new Set((status.configured_policy?.attachment_threads || []).filter((threadID) => !attachment_threads.includes(threadID)));
      const media_reply_threads = mediaReplies.value.split(/[\s，,]+/u).filter((threadID) => threadID && !revokedAttachmentThreads.has(threadID));
      const group_mute_threads = groupMutes.value.split(/[\s，,]+/u).filter(Boolean);
      const group_rules = reply_threads.filter((threadID) => threadID.endsWith("@chatroom")).map((thread_id) => {
        const rule = readGroupRule(thread_id);
        return { thread_id, note: rule.note.trim(), summarize_shares: Boolean(rule.summarize_shares) && attachment_threads.includes(thread_id) };
      });
      runWorkerAction(id, "PUT", "policy", { reply_threads, history_threads, attachment_threads, media_reply_threads, contact_read: contactRead.checked, group_mute_threads, group_rules });
    });
    panel.append(appElement("p", "每行一个会话 ID。群聊默认在被提及时回复。"), field,
      appElement("p", "历史消息与群成员须单独授权；大肥鱼仅能读取当前所在会话。"), history,
      appElement("p", "附件读取须在回复范围内单独开启，原件保存在宿主私有目录。"), attachments,
      groupRules,
      appElement("p", "附件回复须单独授权，只能把附件发回原会话。撤销附件读取时同时撤销附件回复。"), mediaReplies,
      contactReadLabel,
      appElement("p", "仅允许大肥鱼在本地聊天中设置这些群的免打扰或取消免打扰；微信群聊与私聊中的 Agent 不会获得此项权限。"), groupMutes,
      save, workGrantControls(id));
    return panel;
  }
  function contactDirectoryControls(id) {
    const panel = appElement("details", "", "identity-details contact-directory");
    panel.append(appElement("summary", "通讯录"));
    const form = appElement("form", "", "details-grid");
    const search = document.createElement("input");
    search.placeholder = "昵称或微信 ID";
    search.setAttribute("aria-label", "搜索联系人");
    const limit = document.createElement("input");
    limit.type = "number";
    limit.min = "1";
    limit.step = "1";
    limit.value = "50";
    limit.required = true;
    limit.setAttribute("aria-label", "联系人每页数量");
    const searchLabel = appElement("label", "搜索联系人");
    searchLabel.append(search);
    const limitLabel = appElement("label", "每页数量");
    limitLabel.append(limit);
    form.append(searchLabel, limitLabel);
    const actions = appElement("div", "", "app-worker-actions");
    const query = appSetupButton("查询联系人");
    query.type = "submit";
    const next = appSetupButton("下一页", () => load(false));
    next.disabled = true;
    actions.append(query, next);
    form.append(actions);
    const note = appElement("small", "本页仅显示当前微信连接的联系人目录。");
    const records = appElement("div", "", "work-records");
    panel.append(form, note, records);
    let cursor = "", hasNext = false, busy = false, appliedSearch = "", appliedLimit = 50;
    async function load(reset) {
      if (busy) return;
      const searchValue = reset ? search.value.trim() : appliedSearch;
      const limitValue = reset ? limit.valueAsNumber : appliedLimit;
      if (!Number.isSafeInteger(limitValue) || limitValue < 1) {
        appFeedback("每页数量须为正整数。", true);
        return;
      }
      const requestCursor = reset ? "" : cursor;
      busy = true;
      query.disabled = next.disabled = true;
      try {
        const params = new URLSearchParams({ search: searchValue, limit: String(limitValue) });
        if (requestCursor) params.set("cursor", requestCursor);
        const result = await request("/api/apps/wechat/connections/" + encodeURIComponent(id) + "/contacts?" + params.toString());
        if (reset) { appliedSearch = searchValue; appliedLimit = limitValue; }
        records.replaceChildren();
        const items = result.items || [];
        for (const item of items) {
          records.append(appElement("div", (item.display_name || "未命名联系人") + " · " + item.id, "work-record"));
        }
        if (!records.children.length) records.append(appElement("small", "没有匹配的联系人。"));
        cursor = result.next_cursor || "";
        hasNext = Boolean(result.has_more && cursor);
        next.disabled = !hasNext;
        const effectiveLimit = Number.isInteger(result.effective_limit) && result.effective_limit > 0 ? result.effective_limit : limitValue;
        note.textContent = (result.coverage || "已读取当前可见联系人。") + " · 本页 " + items.length + " 条，每页最多 " + effectiveLimit + " 条" + (result.limit_reason ? " · " + result.limit_reason : "");
      } catch (error) {
        appFeedback(error.message, true);
      } finally {
        busy = false;
        query.disabled = false;
        next.disabled = !hasNext;
      }
    }
    form.addEventListener("submit", (event) => { event.preventDefault(); load(true); });
    return panel;
  }
  function groupDirectoryControls(id) {
    const panel = appElement("details", "", "identity-details group-directory");
    panel.append(appElement("summary", "群聊"));
    const form = appElement("form", "", "details-grid");
    const search = document.createElement("input");
    search.placeholder = "群名或群 ID";
    search.setAttribute("aria-label", "搜索群聊");
    const limit = document.createElement("input");
    limit.type = "number";
    limit.min = "1";
    limit.step = "1";
    limit.value = "50";
    limit.required = true;
    limit.setAttribute("aria-label", "群聊每页数量");
    const searchLabel = appElement("label", "搜索群聊");
    searchLabel.append(search);
    const limitLabel = appElement("label", "每页数量");
    limitLabel.append(limit);
    form.append(searchLabel, limitLabel);
    const actions = appElement("div", "", "app-worker-actions");
    const query = appSetupButton("查询群聊");
    query.type = "submit";
    const next = appSetupButton("下一页", () => load(false));
    next.disabled = true;
    actions.append(query, next);
    form.append(actions);
    const note = appElement("small", "本页仅显示当前微信连接可管理的群聊。");
    const records = appElement("div", "", "work-records");
    const control = appElement("div", "", "details-grid");
    control.hidden = true;
    panel.append(form, note, records, control);
    let cursor = "", hasNext = false, busy = false, appliedSearch = "", appliedLimit = 50;
    let selected = null, notification = null, profile = null, selectedAction = null, renameDraft = null, myNameDraft = null, controlBusy = false, needsRefresh = false, renameNeedsRefresh = false, myNameNeedsRefresh = false;
    const base = "/api/apps/wechat/connections/" + encodeURIComponent(id);
    function receiptLabel(operation) {
      if (!operation || !operation.state) return "尚无操作记录";
      if (["succeeded", "confirmed", "completed"].includes(operation.state)) return "已确认完成";
      if (["submitted_pending", "pending", "running"].includes(operation.state)) return "已提交，等待微信确认";
      if (["submission_unknown", "unknown"].includes(operation.state)) return "提交结果待核对";
      if (["cancelled", "canceled"].includes(operation.state)) return "操作已取消";
      if (["failed", "rejected"].includes(operation.state)) return "操作未完成";
      return "状态待核对";
    }
    function isOutstanding(operation) {
      return Boolean(operation && ["submitted_pending", "pending", "running", "submission_unknown", "unknown"].includes(operation.state));
    }
    function renderControl() {
      control.replaceChildren();
      if (!selected) { control.hidden = true; return; }
      control.hidden = false;
      control.append(appElement("strong", selected.display_name || "未命名群聊"), appElement("small", selected.id));
      const mutedKnown = typeof notification?.muted === "boolean";
      const coverage = notification?.coverage === "cached" ? "（来自本机缓存）" : "";
      control.append(appElement("small", mutedKnown
        ? (notification.muted ? "当前为免打扰" : "当前为正常提醒") + coverage
        : "当前免打扰状态未取得，可刷新后再决定。"));
      const operation = notification?.operation || null;
      if (operation) control.append(appElement("small", receiptLabel(operation) + (operation.operation_id ? " · 操作 " + operation.operation_id : "")));
      const refresh = appSetupButton(controlBusy ? "正在刷新…" : "刷新状态", () => loadNotification());
      refresh.disabled ||= controlBusy;
      control.append(refresh);
      if (needsRefresh) {
        control.append(appElement("small", "免打扰操作暂不可确认，请先刷新状态；不会自动重复提交。"));
      } else if (notification?.can_change !== true) {
        control.append(appElement("small", "当前连接不能修改这个群的免打扰；仍可刷新查看已有操作回执。"));
      } else if (isOutstanding(operation)) {
        control.append(appElement("small", "这项免打扰操作仍在核对中；刷新只查询状态，不会重复提交。"));
      } else {
        const choices = appElement("div", "", "app-worker-actions");
        const mute = appSetupButton("设为免打扰", () => { selectedAction = true; renderControl(); });
        const unmute = appSetupButton("取消免打扰", () => { selectedAction = false; renderControl(); });
        mute.disabled ||= controlBusy;
        unmute.disabled ||= controlBusy;
        choices.append(mute, unmute);
        control.append(choices);
        if (typeof selectedAction === "boolean") {
          const confirmation = appElement("div", "", "app-worker-actions");
          confirmation.append(appElement("small", `将“${selected.display_name || selected.id}”${selectedAction ? "设为免打扰" : "取消免打扰"}。`));
          const confirm = appSetupButton("确认提交", () => submitAction());
          const cancel = appSetupButton("取消", () => { selectedAction = null; renderControl(); });
          confirm.disabled ||= controlBusy;
          cancel.disabled ||= controlBusy;
          confirmation.append(confirm, cancel);
          control.append(confirmation);
        }
      }
      const renameOperation = profile?.operation || null;
      const myNameOperation = profile?.my_name_operation || null;
      if (!profile || !(profile.can_rename === true || renameOperation || profile.can_set_my_name === true || myNameOperation)) return;
      const profileSection = appElement("div", "", "details-grid");
      const refreshProfile = appSetupButton(controlBusy ? "正在刷新…" : "刷新群资料", () => loadProfile());
      refreshProfile.disabled ||= controlBusy;
      profileSection.append(refreshProfile);
      function appendTextAction(options) {
        const section = appElement("div", "", "details-grid");
        section.append(appElement("strong", options.title), appElement("small", options.currentLabel));
        if (options.operation) section.append(appElement("small", receiptLabel(options.operation) + (options.operation.operation_id ? " · 操作 " + options.operation.operation_id : "")));
        if (options.needsRefresh()) {
          section.append(appElement("small", options.retryMessage));
        } else if (isOutstanding(options.operation)) {
          section.append(appElement("small", options.pendingMessage));
        } else if (options.canChange === true) {
          const start = appSetupButton(options.startLabel, () => { options.setDraft(options.currentValue); renderControl(); });
          start.disabled ||= controlBusy;
          section.append(start);
          if (options.draft() !== null) {
            const input = document.createElement("input");
            input.value = options.draft();
            input.setAttribute("aria-label", options.inputLabel);
            const confirmation = appElement("div", "", "app-worker-actions");
            const confirmationCopy = appElement("small", options.confirmation(input.value));
            input.addEventListener("input", () => {
              options.setDraft(input.value);
              confirmationCopy.textContent = options.confirmation(input.value);
            });
            confirmation.append(input, confirmationCopy);
            const confirm = appSetupButton(options.confirmLabel, () => options.submit());
            const cancel = appSetupButton(options.cancelLabel, () => { options.setDraft(null); renderControl(); });
            confirm.disabled ||= controlBusy;
            cancel.disabled ||= controlBusy;
            confirmation.append(confirm, cancel);
            section.append(confirmation);
          }
        } else {
          section.append(appElement("small", options.unavailableMessage));
        }
        profileSection.append(section);
      }
      const name = typeof profile.name === "string" ? profile.name : "";
      appendTextAction({
        title: "群名称", currentValue: name,
        currentLabel: (name || "群名称尚未取得") + (name && profile.coverage === "cached" ? "（来自本机缓存）" : ""),
        operation: renameOperation, canChange: profile.can_rename, draft: () => renameDraft, setDraft: (value) => { renameDraft = value; },
        needsRefresh: () => renameNeedsRefresh, retryMessage: "改名结果暂不可确认，请先刷新群资料；不会自动重复提交。",
        pendingMessage: "这次改名仍在核对中；刷新只查询原操作，不会重复提交。",
        startLabel: "修改群名称", inputLabel: "新的群名称", confirmLabel: "确认改名", cancelLabel: "取消改名",
        confirmation: (value) => `将“${selected.display_name || selected.id}”改为“${value || "新名称"}”。`,
        unavailableMessage: "当前连接不能修改这个群名称；仍可刷新查看已有操作回执。", submit: () => submitRename()
      });
      const hasMyName = Object.prototype.hasOwnProperty.call(profile, "my_display_name");
      const myName = typeof profile.my_display_name === "string" ? profile.my_display_name : "";
      appendTextAction({
        title: "我的群昵称", currentValue: myName,
        currentLabel: hasMyName ? ((myName || "未设置") + (profile.coverage === "cached" ? "（来自本机缓存）" : "")) : "群昵称尚未取得",
        operation: myNameOperation, canChange: profile.can_set_my_name, draft: () => myNameDraft, setDraft: (value) => { myNameDraft = value; },
        needsRefresh: () => myNameNeedsRefresh, retryMessage: "群昵称修改结果暂不可确认，请先刷新群资料；不会自动重复提交。",
        pendingMessage: "这次群昵称修改仍在核对中；刷新只查询原操作，不会重复提交。",
        startLabel: "修改我的群昵称", inputLabel: "新的群昵称", confirmLabel: "确认修改昵称", cancelLabel: "取消修改昵称",
        confirmation: (value) => value === "" ? `将“${selected.display_name || selected.id}”中的我的群昵称清空。` : `将“${selected.display_name || selected.id}”中的我的群昵称改为“${value}”。`,
        unavailableMessage: "当前连接不能修改我的群昵称；仍可刷新查看已有操作回执。", submit: () => submitMyName()
      });
      control.append(profileSection);
    }
    async function loadNotification() {
      if (!selected || controlBusy) return;
      controlBusy = true;
      renderControl();
      try {
        notification = await request(base + "/groups/" + encodeURIComponent(selected.id) + "/notification");
        selectedAction = null;
        needsRefresh = false;
      } catch (error) {
        appFeedback(error.message, true);
      } finally {
        controlBusy = false;
        renderControl();
      }
    }
    async function loadProfile() {
      if (!selected || controlBusy) return;
      controlBusy = true;
      renderControl();
      try {
        profile = await request(base + "/groups/" + encodeURIComponent(selected.id) + "/profile");
        renameDraft = null;
        renameNeedsRefresh = false;
        myNameDraft = null;
        myNameNeedsRefresh = false;
      } catch (error) {
        appFeedback(error.message, true);
      } finally {
        controlBusy = false;
        renderControl();
      }
    }
    async function selectGroup(item) {
      if (controlBusy) return;
      selected = item;
      notification = null;
      profile = null;
      selectedAction = null;
      renameDraft = null;
      myNameDraft = null;
      needsRefresh = false;
      renameNeedsRefresh = false;
      myNameNeedsRefresh = false;
      renderControl();
      await loadNotification();
      await loadProfile();
    }
    async function submitAction() {
      if (!selected || typeof selectedAction !== "boolean" || controlBusy) return;
      controlBusy = true;
      renderControl();
      try {
        const receipt = await request(base + "/group-actions", { method: "POST", body: JSON.stringify({
          group_id: selected.id, muted: selectedAction, request_key: crypto.randomUUID()
        }) });
        notification = { ...(notification || {}), operation: receipt };
        selectedAction = null;
      } catch (error) {
        selectedAction = null;
        needsRefresh = true;
        appFeedback(error.message, true);
      } finally {
        controlBusy = false;
        renderControl();
      }
    }
    async function submitRename() {
      if (!selected || controlBusy) return;
      const name = renameDraft || "";
      if (!name.trim() || new TextEncoder().encode(name).length > 512 || /\p{Cc}/u.test(name)) {
        appFeedback("新群名称须非空、最多 512 UTF-8 字节，且不能含控制字符。", true);
        return;
      }
      controlBusy = true;
      renderControl();
      try {
        const receipt = await request(base + "/group-actions", { method: "POST", body: JSON.stringify({
          action: "rename", group_id: selected.id, name, request_key: crypto.randomUUID()
        }) });
        profile = { ...(profile || {}), operation: receipt };
        renameDraft = null;
      } catch (error) {
        renameDraft = null;
        renameNeedsRefresh = true;
        appFeedback(error.message, true);
      } finally {
        controlBusy = false;
        renderControl();
      }
    }
    async function submitMyName() {
      if (!selected || controlBusy) return;
      const name = myNameDraft === null ? "" : myNameDraft;
      if (new TextEncoder().encode(name).length > 512 || /\p{Cc}/u.test(name)) {
        appFeedback("群昵称最多 512 UTF-8 字节，且不能含控制字符。", true);
        return;
      }
      controlBusy = true;
      renderControl();
      try {
        const receipt = await request(base + "/group-actions", { method: "POST", body: JSON.stringify({
          action: "my_name", group_id: selected.id, name, request_key: crypto.randomUUID()
        }) });
        profile = { ...(profile || {}), my_name_operation: receipt };
        myNameDraft = null;
      } catch (error) {
        myNameDraft = null;
        myNameNeedsRefresh = true;
        appFeedback(error.message, true);
      } finally {
        controlBusy = false;
        renderControl();
      }
    }
    async function load(reset) {
      if (busy) return;
      const searchValue = reset ? search.value.trim() : appliedSearch;
      const limitValue = reset ? limit.valueAsNumber : appliedLimit;
      if (!Number.isSafeInteger(limitValue) || limitValue < 1) {
        appFeedback("每页数量须为正整数。", true);
        return;
      }
      const requestCursor = reset ? "" : cursor;
      busy = true;
      query.disabled = next.disabled = true;
      try {
        const params = new URLSearchParams({ search: searchValue, limit: String(limitValue) });
        if (requestCursor) params.set("cursor", requestCursor);
        const result = await request(base + "/groups?" + params.toString());
        if (reset) { appliedSearch = searchValue; appliedLimit = limitValue; }
        records.replaceChildren();
        const items = result.items || [];
        for (const item of items) {
          const row = appSetupButton((item.display_name || "未命名群聊") + " · " + item.id, () => selectGroup(item));
          row.classList.add("group-directory-row");
          records.append(row);
        }
        if (!records.children.length) records.append(appElement("small", "没有匹配的群聊。"));
        cursor = result.next_cursor || "";
        hasNext = Boolean(result.has_more && cursor);
        next.disabled = !hasNext;
        const effectiveLimit = Number.isInteger(result.effective_limit) && result.effective_limit > 0 ? result.effective_limit : limitValue;
        note.textContent = (result.coverage || "已读取当前可见群聊。") + " · 本页 " + items.length + " 条，每页最多 " + effectiveLimit + " 条" + (result.limit_reason ? " · " + result.limit_reason : "");
      } catch (error) {
        appFeedback(error.message, true);
      } finally {
        busy = false;
        query.disabled = false;
        next.disabled = !hasNext;
      }
    }
    form.addEventListener("submit", (event) => { event.preventDefault(); load(true); });
    return panel;
  }
  function stationToolControls(id, media) {
    const name = media ? "媒体理解" : "视频号解析";
    const tool = media ? "media-understanding" : "share-reader";
    const fieldsSpec = media ? [
      ["vision_endpoint", "视觉接口地址"], ["vision_model", "视觉模型"], ["vision_key", "视觉接口密钥", true],
      ["asr_endpoint", "语音识别接口地址"], ["asr_model", "语音识别模型"], ["asr_key", "语音识别接口密钥", true]
    ] : [
      ["yuanbao_cookie", "元宝解析凭据", true], ["assistant_cookie", "视频号助手凭据", true], ["object_bridge_url", "本机对象桥接地址"]
    ];
    const panel = appElement("details", "", "identity-details");
    panel.append(appElement("summary", name));
    const content = appElement("div", "", "details-grid");
    panel.append(content);
    const path = "/api/apps/wechat/connections/" + encodeURIComponent(id) + "/" + tool;
    let busy = false;
    async function load(body) {
      if (busy) return;
      busy = true;
      content.querySelectorAll("button").forEach((button) => { button.disabled = true; });
      try {
        const value = await request(path, body ? { method: "PUT", body: JSON.stringify(body) } : undefined);
        render(value);
      } catch (error) {
        content.querySelectorAll("button").forEach((button) => { button.disabled = false; });
        let errorRow = content.querySelector(".share-reader-error");
        if (!errorRow) {
          errorRow = appElement("small", "", "share-reader-error");
          content.append(errorRow);
        }
        errorRow.textContent = error.message;
      } finally { busy = false; }
    }
    function render(value) {
      const status = value.installed ? (value.enabled ? "已启用，按需运行" : "已停用") : "尚未安装";
      content.replaceChildren(appElement("small", value.runtime_ready ? status : `${status} · 缺少 Windows 隔离工具运行环境`));
      if (!value.installed) {
        const explanation = media ? "仅处理当前连接已授权的视频。实际帧或音轨通过宿主代理发送到配置的模型接口。" : "授权后，只为当前微信连接解析原卡片。";
        const approval = media ? { grant_media: true } : { grant_network: true };
        content.append(appElement("small", explanation),
          appSetupButton(media ? "安装并授权媒体处理和模型调用" : "安装并授予受控联网权限", () => load({ action: "install", ...approval })));
        return;
      }
      const action = value.enabled ? "disable" : "enable";
      content.append(appSetupButton((value.enabled ? "停用" : "启用") + (media ? "媒体理解" : "解析"), () => load({ action, revision: value.revision })));
      if (value.enabled) return;
      if (media) content.append(appElement("small", "视觉与音轨转写分别配置。接口地址须为完整 HTTPS 地址。"));
      const configuration = value.configuration || {};
      const fields = new Map();
      const clear = new Map();
      for (const [key, label, secret] of fieldsSpec) {
        const wrap = appElement("label", label, "details-grid");
        const field = document.createElement("input");
        field.type = secret ? "password" : "text";
        field.autocomplete = secret ? "new-password" : "off";
        field.value = secret ? "" : configuration.values?.[key] || "";
        field.placeholder = secret && configuration.configured_secrets?.includes(key) ? "已配置，留空保留" : "可留空";
        fields.set(key, field);
        wrap.append(field);
        content.append(wrap);
        if (secret && configuration.configured_secrets?.includes(key)) {
          const clearLabel = appElement("label", "清除已保存的" + label);
          const checkbox = document.createElement("input");
          checkbox.type = "checkbox";
          clear.set(key, checkbox);
          clearLabel.prepend(checkbox);
          content.append(clearLabel);
        }
      }
      content.append(appSetupButton(media ? "保存媒体设置" : "保存解析设置", () => {
        const values = {};
        if (media) {
          for (const key of ["vision_backend", "thinking", "reasoning_effort", "max_tokens"]) {
            if (configuration.values?.[key] !== undefined) values[key] = configuration.values[key];
          }
        }
        const secrets = {};
        const clear_secrets = [];
        for (const [key, , secret] of fieldsSpec) {
          const field = fields.get(key);
          if (secret) {
            if (clear.get(key)?.checked) clear_secrets.push(key);
            else if (field.value) secrets[key] = field.value;
          } else if (field.value.trim()) values[key] = field.value.trim();
        }
        load({ action: "configure", revision: value.revision, configuration: {
          revision: configuration.revision || 0, values, secrets, clear_secrets
        } });
      }));
    }
    panel.addEventListener("toggle", () => { if (panel.open) load(); });
    return panel;
  }
  function workGrantControls(id) {
    const panel = appElement("details", "", "identity-details work-grant-panel");
    panel.append(appElement("summary", "授权作品交付"));
    const form = appElement("form", "", "details-grid");
    const draft = apps.workDrafts.get(id) || { artifact_id: "", thread_id: "", requester_id: "", request_key: crypto.randomUUID() };
    apps.workDrafts.set(id, draft);
    for (const [key, label, placeholder] of [["artifact_id", "作品编号", "登记作品后得到的 work- 编号"], ["thread_id", "接收会话", "已允许附件回复的会话 ID"], ["requester_id", "请求者", "该会话中请求作品的微信 ID"]]) {
      const wrap = appElement("label", label, "details-grid");
      const input = document.createElement("input");
      input.value = draft[key]; input.required = true; input.placeholder = placeholder;
      input.addEventListener("input", () => { draft[key] = input.value; });
      wrap.append(input); form.append(wrap);
    }
    const actions = appElement("div", "", "app-worker-actions");
    const grant = appElement("button", "允许交付", "button compact secondary");
    grant.type = "submit";
    const revoke = appSetupButton("撤销此授权", () => save(false));
    actions.append(grant, revoke);
    form.append(actions, appElement("small", "授权后，大肥鱼可应此人的请求发送该作品。撤销时会暂停此连接。"));
    async function save(enabled) {
      if (!form.reportValidity()) return;
      grant.disabled = revoke.disabled = true;
      try {
        await request("/api/apps/wechat/connections/" + encodeURIComponent(id) + "/work-grants", { method: "POST", body: JSON.stringify({ ...draft, enabled }) });
        appFeedback(enabled ? "作品交付已授权，可在指定会话向大肥鱼索取。" : "作品交付已撤销，连接已暂停。");
        if (!enabled) { draft.request_key = crypto.randomUUID(); await loadApps(); }
      } catch (error) { appFeedback(error.message, true); }
      finally { grant.disabled = revoke.disabled = false; }
    }
    form.addEventListener("submit", (event) => { event.preventDefault(); save(true); });
    panel.append(form);
    const records = appElement("div", "", "work-records");
    let after = "", loaded = false;
    const list = appSetupButton("查看已有授权", async () => {
      list.disabled = true;
      try {
        const result = await request("/api/apps/wechat/connections/" + encodeURIComponent(id) + "/work-grants?limit=20&after=" + encodeURIComponent(after));
        if (!loaded) records.replaceChildren();
        for (const item of result.items || []) {
          const row = appElement("div", "", "work-record");
          const status = !item.enabled ? "已撤销" : item.usable === false ? "来源或范围已变化" : "已授权";
          const text = appElement("small", (item.filename ? item.filename + " · " : "") + item.thread_id + " · " + item.requester_id + " · " + status);
          const remove = appSetupButton("撤销", async () => {
            remove.disabled = true;
            try {
              await request("/api/apps/wechat/connections/" + encodeURIComponent(id) + "/work-grants/" + encodeURIComponent(item.delivery_id) + "/revoke", { method: "POST", body: "{}" });
              text.textContent = item.thread_id + " · 已撤销，连接已暂停";
            } catch (error) { appFeedback(error.message, true); remove.disabled = false; }
          });
          remove.disabled = !item.enabled;
          row.append(text, remove); records.append(row);
        }
        if (!records.children.length) records.append(appElement("small", "还没有作品交付授权。"));
        after = result.next_after || ""; loaded = true;
        list.textContent = after ? "继续查看" : "已列出全部";
        list.disabled = !after;
      } catch (error) { appFeedback(error.message, true); list.disabled = false; }
    });
    panel.append(list, records);
    return panel;
  }
  function deliveryRecords(id) {
    const panel = appElement("details", "", "identity-details delivery-records");
    panel.append(appElement("summary", "投递记录"));
    const records = appElement("div", "", "work-records");
    const note = appElement("small", "核对只查询原任务。停止作用于本回合的剩余投递，已接受的消息保留。");
    const base = "/api/apps/wechat/connections/" + encodeURIComponent(id) + "/receipts";
    let after = "", snapshot = "", loading = false;
    function describe(item) {
      const labels = { accepted: "微信已接受", not_submitted: "待发送", submitted_pending: "等待微信回执", submission_unknown: "发送结果待核对", failed: "发送失败", cancelled: "已取消" };
      const kinds = { text: "文字", voice: "语音", image: "图片", video: "视频", file: "文件" };
      let status = labels[item.state] || "状态待核对";
      const accepted = item.accepted_parts?.length || 0;
      const pending = item.unconfirmed_parts?.filter((part) => part.state !== "cancelled").length || 0;
      const cancelled = item.unconfirmed_parts?.filter((part) => part.state === "cancelled").length || 0;
      if ((item.cancel_requested || item.state === "cancelled") && item.state !== "accepted") {
        status = item.state === "cancelled" && !pending && !item.parts_incomplete ? "已取消" : "正在停止，等待回执";
      }
      const parts = [];
      if (accepted) parts.push(`已接受 ${accepted} 段`);
      if (pending) parts.push(`待核对 ${pending} 段`);
      if (cancelled) parts.push(`已取消 ${cancelled} 段`);
      if (item.parts_incomplete) parts.push("分段记录尚不完整");
      return (kinds[item.kind] || "消息") + (item.resumed_from ? "续发" : "") + " · " + item.conversation_id + " · " + status + (parts.length ? "（" + parts.join("，") + "）" : "") + (item.continuation?.message ? " · " + item.continuation.message : "");
    }
    function append(item) {
      const row = appElement("div", "", "work-record");
      const text = appElement("small", describe(item));
      const recall = appElement("div", "", "delivery-recall");
      const recallState = { serverID: "", cursor: "", messageID: "", operation: null, submitting: false, lookingUp: false, checked: false };
      function acceptedServers(receipt) {
        const seen = new Set();
        const add = (serverID, label) => {
          const value = String(serverID || "").trim();
          if (!value || seen.has(value)) return;
          seen.add(value);
          servers.push({ value, label });
        };
        const servers = [];
        for (const part of receipt.accepted_parts || []) add(part.server_id, `已接受第 ${(Number(part.index) || 0) + 1} 段`);
        if (receipt.state === "accepted") add(receipt.server_id, "已接受消息");
        return servers;
      }
      function operationText(operation) {
        if (!operation) return "";
        const labels = { succeeded: "撤回已完成", failed: "撤回失败", rejected: "撤回被拒绝", cancelled: "撤回已取消", submitted_pending: "撤回请求已提交，等待微信核对", waiting_receipt: "撤回请求已提交，等待微信核对", tracking: "正在核对撤回结果", unknown: "撤回结果待核对", submission_unknown: "撤回结果待核对" };
        return labels[operation.state] || "撤回状态待核对";
      }
      function operationFinal(operation) {
        return ["succeeded", "failed", "rejected", "cancelled"].includes(operation?.state);
      }
      function operationNeedsLookup(operation) {
        return ["submitted_pending", "waiting_receipt", "tracking", "unknown", "submission_unknown"].includes(operation?.state);
      }
      function resultOperation(result) {
        return result?.operation || (result?.operation_id ? result : null);
      }
      async function lookupRecall(cursor = "") {
        const serverID = recallState.serverID;
        const query = new URLSearchParams({ server_id: serverID });
        if (cursor) query.set("cursor", cursor);
        recallState.lookingUp = true;
        renderRecall();
        try {
          const result = await request(base + "/" + encodeURIComponent(item.idempotency_key) + "/recall?" + query.toString());
          // A selector change must never attach an old part's lookup result to a new part.
          if (serverID !== recallState.serverID) return result;
          recallState.checked = true;
          recallState.cursor = result.next_cursor || "";
          recallState.messageID = result.message_id || "";
          const operation = resultOperation(result);
          if (operation) recallState.operation = operation;
          return result;
        } finally {
          if (serverID === recallState.serverID) {
            recallState.lookingUp = false;
            renderRecall();
          }
        }
      }
      function renderRecall() {
        recall.replaceChildren();
        const servers = acceptedServers(item);
        if (!servers.length) return;
        if (!servers.some((server) => server.value === recallState.serverID)) {
          recallState.serverID = servers[0].value;
          recallState.cursor = ""; recallState.messageID = ""; recallState.operation = null; recallState.submitting = false; recallState.lookingUp = false; recallState.checked = false;
        }
        const selected = servers.find((server) => server.value === recallState.serverID);
        if (servers.length > 1) {
          const select = document.createElement("select");
          select.setAttribute("aria-label", "选择要撤回的已确认消息");
          for (const server of servers) {
            const option = document.createElement("option"); option.value = server.value; option.textContent = server.label; option.selected = server.value === recallState.serverID;
            select.append(option);
          }
          select.disabled = recallState.submitting || recallState.lookingUp;
          select.addEventListener("change", () => {
            recallState.serverID = select.value; recallState.cursor = ""; recallState.messageID = ""; recallState.operation = null; recallState.submitting = false; recallState.lookingUp = false; recallState.checked = false;
            renderRecall();
          });
          recall.append(select);
        } else {
          recall.append(appElement("small", selected.label));
        }
        const blocked = operationNeedsLookup(recallState.operation) || recallState.submitting;
        if (recallState.operation) recall.append(appElement("small", operationText(recallState.operation)));
        if (recallState.lookingUp) {
          recall.append(appElement("small", "正在核对撤回记录"));
        } else if (operationFinal(recallState.operation)) {
          // A terminal receipt is authoritative. Never offer a second POST that could
          // turn a failed, rejected, or cancelled recall into an accidental retry.
        } else if (recallState.messageID && !blocked && !recallState.submitting) {
          const confirm = appSetupButton("确认撤回", async () => {
            const serverID = recallState.serverID, messageID = recallState.messageID;
            recallState.submitting = true; renderRecall();
            try {
              const posted = await request(base + "/" + encodeURIComponent(item.idempotency_key) + "/recall", { method: "POST", body: JSON.stringify({ server_id: serverID, message_id: messageID }) });
              if (serverID !== recallState.serverID) return;
              recallState.operation = resultOperation(posted) || { state: "submission_unknown" };
            } catch (error) {
              if (serverID !== recallState.serverID) return;
              recallState.operation = { state: "submission_unknown" };
              appFeedback(error.message, true);
            }
            try {
              await lookupRecall();
            } catch (error) {
              if (serverID === recallState.serverID) appFeedback(error.message, true);
            } finally {
              if (serverID === recallState.serverID) {
                recallState.submitting = false;
                renderRecall();
              }
            }
          });
          recall.append(confirm);
        } else if (recallState.cursor && !blocked) {
          const moreRecall = appSetupButton("继续查找", async () => {
            moreRecall.disabled = true; moreRecall.textContent = "正在查找…";
            try { await lookupRecall(recallState.cursor); } catch (error) { appFeedback(error.message, true); renderRecall(); }
          });
          recall.append(moreRecall);
        } else if (!recallState.checked || blocked) {
          const locate = appSetupButton(blocked ? "核对撤回状态" : "定位可撤回消息", async () => {
            locate.disabled = true; locate.textContent = "正在核对…";
            try { await lookupRecall(); } catch (error) { appFeedback(error.message, true); renderRecall(); }
          });
          recall.append(locate);
        } else {
          recall.append(appElement("small", "没有找到可撤回的本人消息"));
        }
      }
      const stop = appSetupButton("停止本回合投递", async () => {
        stop.disabled = true; stop.textContent = "正在停止…";
        try {
          await request(base + "/" + encodeURIComponent(item.idempotency_key) + "/cancel", { method: "POST", body: "{}" });
          item.cancel_requested = true;
          if (item.continuation) item.continuation = { ...item.continuation, state: "cancelled", message: "续办已停止" };
          text.textContent = describe(item);
          updateActions();
          stop.textContent = "停止意图已保存";
        } catch (error) { appFeedback(error.message, true); stop.disabled = false; stop.textContent = "停止本回合投递"; }
      });
      const resume = appSetupButton("续发未发送片段", async () => {
        resume.disabled = true; resume.textContent = "正在保存…";
        const draftID = id + "|" + item.idempotency_key;
        if (!apps.resumeDrafts.has(draftID)) apps.resumeDrafts.set(draftID, crypto.randomUUID());
        try {
          const result = await request(base + "/" + encodeURIComponent(item.idempotency_key) + "/resume", { method: "POST", body: JSON.stringify({ request_key: apps.resumeDrafts.get(draftID) }) });
          item.continuation = result.continuation;
          text.textContent = describe(item);
          updateActions();
        } catch (error) { appFeedback(error.message, true); updateActions(); }
      });
      function updateActions() {
        const active = ["requested", "waiting_receipt", "tracking"].includes(item.continuation?.state);
        stop.disabled = Boolean(!active && (item.cancel_requested || item.state === "cancelled"));
        stop.textContent = active ? "停止续办" : "停止本回合投递";
        resume.hidden = item.kind !== "voice" || !(item.cancel_requested || item.state === "cancelled");
        resume.disabled = Boolean(item.continuation || item.state === "accepted" || item.state === "cancelled" && !item.operation_id);
        resume.textContent = item.continuation ? "续办请求已保存" : "续发未发送片段";
      }
      const check = appSetupButton("核对回执", async () => {
        check.disabled = true; check.textContent = "正在核对…";
        try {
          const result = await request(base + "/" + encodeURIComponent(item.idempotency_key));
          item = result;
          text.textContent = describe(result);
          updateActions();
          renderRecall();
          if (result.lookup === "unavailable") text.textContent += " · 暂时无法连接，保留已知状态";
          const nextKey = result.continuation?.delivery_key;
          if (nextKey && nextKey !== result.idempotency_key) {
            try {
              const next = await request(base + "/" + encodeURIComponent(nextKey));
              if (next.resumed_from === result.idempotency_key && next.conversation_id === result.conversation_id) {
                text.textContent += " · 续发结果：" + describe(next);
              }
            } catch (_) { text.textContent += " · 续发回执暂不可读"; }
          }
        } catch (error) { appFeedback(error.message, true); }
        finally { check.disabled = false; check.textContent = "核对回执"; }
      });
      updateActions();
      renderRecall();
      row.append(text, check, stop, resume, recall); records.append(row);
    }
    const more = appSetupButton("查看投递记录", async () => {
      if (loading) return;
      loading = true; more.disabled = true; more.textContent = "正在读取…";
      try {
        const result = await request(base + "?limit=20&after=" + encodeURIComponent(after));
        if (after && snapshot !== (result.snapshot || "")) {
          after = ""; snapshot = "";
          note.textContent = "投递记录已更新，请重新查看。";
          more.textContent = "重新查看";
          return;
        }
        if (!after) records.replaceChildren();
        for (const item of result.items || []) append(item);
        if (!records.children.length) records.append(appElement("small", "此连接还没有当前投递记录。"));
        snapshot = result.snapshot || ""; after = result.next_after || "";
        note.textContent = "仅含当前连接队列保存的记录；旧版导入回执仍可按原编号查询。";
        more.textContent = after ? "继续查看" : "重新查看";
      } catch (error) { appFeedback(error.message, true); more.textContent = "重新查看"; }
      finally { loading = false; more.disabled = false; }
    });
    panel.append(note, records, more);
    return panel;
  }
  $("#artifact-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.currentTarget.querySelector("button"), path = $("#artifact-path").value;
    button.disabled = true;
    try {
      const artifact = await request("/api/local/artifacts", { method: "POST", body: JSON.stringify({ path, request_key: crypto.randomUUID() }) });
      $("#artifact-result").textContent = artifact.filename + " 已登记，作品编号 " + artifact.artifact_id;
    } catch (error) { $("#artifact-result").textContent = error.message; }
    finally { button.disabled = false; }
  });
  async function runWorkerAction(id, method, action, body) {
    const current = apps;
    if (current.setupBusy || current.loading) return;
    current.setupBusy = true;
    appFeedback("");
    renderApps();
    try {
      const path = "/api/apps/wechat/connections/" + encodeURIComponent(id) + "/" + action;
      const result = await request(path, { method, body: JSON.stringify(body) });
      if (current !== apps) return;
      if (action === "policy") {
        current.workerDrafts.delete(id);
        current.historyDrafts.delete(id);
        current.attachmentDrafts.delete(id);
        current.mediaReplyDrafts.delete(id);
        current.groupMuteDrafts.delete(id);
        current.groupRuleDrafts.delete(id);
        appFeedback(result.connection?.submissions_paused ? "回复范围已保存，Agent 已暂停。" : "回复范围已保存。");
      } else {
        appFeedback(body.action === "stop" ? "已暂停此账号的 Agent。" : "已启动此账号的 Agent，正在确认状态。");
      }
      await loadApps();
    } catch (error) {
      if (current === apps && !appSessionExpired([{ status: "rejected", reason: error }])) {
        appFeedback(error.message, true);
        if (error.status === 409) await loadApps();
      }
    } finally {
      current.setupBusy = false;
      if (current === apps) renderApps();
    }
  }
  function appConnectionRow(record) {
    const row = appElement("div", "", "app-layer");
    const detail = appElement("div", "");
    const connection = apps.connections.get(record.id);
    const canSetup = record.id === "wechat" && apps.registered?.some((app) => app.id === record.id && app.account_setup);
    if (!apps.registered || connection === null) {
      detail.append(appElement("strong", "账号状态暂不可读"), appElement("p", "可刷新后重查。"));
    } else if (!connection?.length) {
      const nativeReady = apps.registered.some((app) => app.id === record.id && app.registration === "station-native");
      detail.append(appElement("strong", "账号未接入"),
        appElement("p", nativeReady ? "原生服务已就绪，暂无已登记账号。" : "账号接入后会显示在这里。"));
    } else {
      detail.append(appElement("strong", `已有 ${connection.length} 个账号连接`));
      const list = appElement("ul", "", "app-accounts");
      for (const [index, account] of connection.entries()) {
        const item = appElement("li", "");
        item.append(appElement("span", account.label || `账号 ${index + 1}`));
        if (account.primary) item.append(appElement("small", "主连接"));
        const status = apps.accountStatuses.get(account.id);
        if (apps.accountStatuses.has(account.id)) {
          const login = status?.login === "authenticated" ? "已登录" : status?.login === "signed_out" ? "未登录" : "登录状态暂不可读";
          item.append(appElement("small", login));
        }
        const entry = apps.logins.get(account.id);
        if (canSetup && status?.login === "signed_out" && status.runtime !== "starting" && entry?.op?.state !== "succeeded") {
          const label = pendingAppLogin(entry?.op) ? "查看扫码" : "扫码登录";
          item.append(appSetupButton(label, () => runAppSetup(account.id)));
        }
        if (apps.selectedLogin === account.id && entry?.op && (status?.login !== "authenticated" || entry.op.state === "succeeded")) {
          item.append(renderAppLogin(account.id, entry.op));
        }
        if (canSetup && status && appWorkerLabels[status.worker]) item.append(appWorkerControls(account.id, status));
        list.append(item);
      }
      detail.append(list);
    }
    row.append(appElement("h5", "账号连接"), detail);
    if (canSetup && connection !== null) row.append(appSetupButton("添加账号", () => runAppSetup()));
    return row;
  }
  function renderApps() {
    const list = $("#apps-list");
    list.setAttribute("aria-busy", String(apps.loading || Boolean(apps.installing)));
    $("#apps-refresh").disabled = apps.loading || Boolean(apps.installing);
    $("#apps-refresh").textContent = apps.loading ? "刷新中…" : "刷新";
    const catalog = new Map((apps.catalog || []).map((row) => [row.id, row]));
    const records = new Map((apps.registered || []).map((row) => [row.id, row]));
    for (const row of catalog.values()) records.set(row.id, row);
    if (!records.size) {
      const text = apps.loading ? "正在读取应用…" : apps.catalog && apps.registered ? "还没有可管理的应用。" : "暂时无法读取应用，可刷新后重查。";
      list.replaceChildren(appElement("p", text, "apps-empty"));
      return;
    }
    list.replaceChildren(...Array.from(records.values(), (record) => {
      const card = appElement("article", "", "app-record");
      const head = appElement("header", "", "app-record-head");
      const label = record.name || record.id;
      const mark = appElement("span", Array.from(label)[0], "app-mark");
      mark.setAttribute("aria-hidden", "true");
      head.append(mark, appElement("h4", label));
      card.append(head, appSourceRow(record, catalog.get(record.id)), appConnectionRow(record));
      return card;
    }));
  }
  async function loadApps() {
    const current = apps;
    if (current.loading || !state.home?.initialized) return;
    current.loading = true;
    renderApps();
    try {
      const result = await Promise.allSettled([request("/api/local/apps/catalog"), request("/api/apps")]);
      if (current !== apps || appSessionExpired(result)) return;
      const registered = appRows(result[1], "items");
      const connections = await Promise.allSettled((registered || []).map((row) => request(`/api/apps/${encodeURIComponent(row.id)}/connections`)));
      if (current !== apps || appSessionExpired(connections)) return;
      const connectionMap = new Map((registered || []).map((row, index) => [row.id, appRows(connections[index], "items")]));
      const nativeAccounts = connectionMap.get("wechat") && registered.some((app) => app.id === "wechat" && app.registration === "station-native")
        ? connectionMap.get("wechat") : [];
      const statuses = await Promise.allSettled(nativeAccounts.map((account) => request(`/api/apps/wechat/connections/${encodeURIComponent(account.id)}`)));
      if (current !== apps || appSessionExpired(statuses)) return;
      current.catalog = appRows(result[0], "applications");
      current.registered = registered;
      current.connections = connectionMap;
      current.accountStatuses = new Map(nativeAccounts.map((account, index) => {
        const entry = statuses[index];
        const details = entry.status === "fulfilled" ? entry.value.connection : null;
        return [account.id, details?.id === account.id ? details : null];
      }));
    } finally {
      if (current === apps) {
        current.loading = false;
        renderApps();
      }
    }
  }
  async function installApp(listing) {
    const current = apps;
    if (current.loading || current.installing || !state.home?.home_access) return;
    current.installing = listing.id;
    appFeedback("");
    renderApps();
    try {
      const result = await request(`/api/local/apps/${encodeURIComponent(listing.id)}/install`, {
        method: "POST", body: JSON.stringify({ digest: listing.digest, revision: state.home.revision })
      });
      if (current !== apps) return;
      appFeedback(result.result === "existing" ? "源码已在家目录中。" : "源码已安装到家目录。");
      await loadApps();
    } catch (error) {
      if (current !== apps) return;
      if (error.status === 401 || error.status === 403) {
        state.csrf = "";
        showLogin();
        showMessage("登录状态已失效，请重新验证。", "warn");
      } else {
        appFeedback(error.message, true);
        if (error.status === 409) {
          const home = await request("/api/local/home").catch(() => null);
          if (home && current === apps) showHome(home);
          if (current === apps) await loadApps();
        }
      }
    } finally {
      if (current === apps) {
        current.installing = "";
        renderApps();
      }
    }
  }
  function projectToolStatus(tool) {
    if (!tool || !tool.installed && !tool.catalog_available) return ["未随当前发行包提供", "请安装包含此工具的 Windows 发行包。"];
    if (!tool.runtime_ready) return ["缺少本地运行环境", "工具尚不能启动；先补齐本机运行环境或依赖包。"];
    if (!tool.installed) return ["尚未安装", "安装后才会把工具放入大肥鱼的项目环境。"];
    if (!tool.enabled) return ["已安装，未启用", "需要时启用；停用不会删除已生成的本地成品。"];
    if (tool.state === "running") return ["已启动", "本地运行器空闲一段时间后会自动关闭。"];
    if (tool.state === "failed") return ["运行异常", "下次有本地任务时会尝试重新启动。"];
    return ["已启用，待命", "会在本地聊天实际需要时启动，不会取得网络或应用账号权限。"];
  }
  function renderProjectTools() {
    const list = $("#project-tools-list"), current = projectTools;
    list.setAttribute("aria-busy", String(current.loading || Boolean(current.applying)));
    $("#project-tools-refresh").disabled = current.loading || Boolean(current.applying);
    $("#project-tools-refresh").textContent = current.loading ? "刷新中…" : "刷新";
    if (current.loading && !current.tools.length) {
      list.replaceChildren(appElement("p", "正在读取项目工具…", "project-tools-empty"));
      return;
    }
    if (!current.tools.length) {
      list.replaceChildren(appElement("p", "当前没有可管理的项目工具。", "project-tools-empty"));
      return;
    }
    list.replaceChildren(...current.tools.map((tool) => {
      const card = appElement("article", "", "project-tool-record");
      const [status, note] = projectToolStatus(tool);
      const head = appElement("header", "", "project-tool-head");
      const mark = appElement("div", "工", "project-tool-mark"), title = appElement("div", "");
      mark.setAttribute("aria-hidden", "true");
      title.append(appElement("h4", tool.name || tool.id || "项目工具"), appElement("p", status));
      head.append(mark, title);
      const detail = appElement("div", "", "project-tool-detail");
      detail.append(appElement("p", note));
      if (tool.id === "document-workshop") {
        const permissions = appElement("div", "", "project-tool-permissions");
        permissions.append(appElement("strong", "本次授权"), appElement("span", "本机素材读写限于授权的文档任务，成品保存在本机，可从聊天下载。联网和应用账号权限另行管理。"));
        detail.append(permissions);
      }
      const controls = appElement("div", "", "project-tool-actions");
      const busy = current.applying === tool.id;
      if (!tool.installed) {
        const approval = document.createElement("label"), checkbox = document.createElement("input"), copy = appElement("span", "确认本机素材读写");
        approval.className = "project-tool-approval"; checkbox.type = "checkbox"; checkbox.disabled = busy || !tool.catalog_available || !tool.runtime_ready;
        approval.append(checkbox, copy);
        const install = appElement("button", busy ? "正在安装…" : "安装", "button compact primary");
        install.type = "button"; install.disabled = true;
        checkbox.addEventListener("change", () => { install.disabled = checkbox.disabled || !checkbox.checked; });
        install.addEventListener("click", () => updateProjectTool(tool, "install", checkbox.checked));
        controls.append(approval, install);
      } else {
        const action = tool.enabled ? "disable" : "enable";
        const button = appElement("button", busy ? "正在更新…" : (tool.enabled ? "停用" : "启用"), tool.enabled ? "button compact secondary" : "button compact primary");
        button.type = "button"; button.disabled = busy || (action === "enable" && !tool.runtime_ready);
        button.addEventListener("click", () => updateProjectTool(tool, action, false));
        controls.append(button);
      }
      detail.append(controls); card.append(head, detail); return card;
    }));
  }
  async function loadProjectTools() {
    const current = projectTools;
    if (current.loading || !state.home?.initialized) return;
    current.loading = true; renderProjectTools();
    try {
      const payload = await request("/api/local/project-tools");
      if (current !== projectTools) return;
      current.tools = Array.isArray(payload.tools) ? payload.tools.filter((tool) => tool && typeof tool.id === "string") : [];
    } catch (error) {
      if (current === projectTools) projectToolsFeedback(error.message, true);
    } finally {
      if (current === projectTools) { current.loading = false; renderProjectTools(); }
    }
  }
  async function updateProjectTool(tool, action, approveArtifacts) {
    const current = projectTools;
    if (current.loading || current.applying || !tool || tool.id !== "document-workshop") return;
    current.applying = tool.id; projectToolsFeedback(""); renderProjectTools();
    try {
      const result = await request("/api/local/project-tools/document-workshop", { method: "PUT", body: JSON.stringify({ action, revision: Number(tool.revision || 0), approve_artifacts: Boolean(approveArtifacts) }) });
      if (current !== projectTools) return;
      const updated = result && result.tool;
      if (updated && typeof updated.id === "string") current.tools = current.tools.map((item) => item.id === updated.id ? updated : item);
      projectToolsFeedback(action === "install" ? "文档工坊已安装。" : (action === "enable" ? "文档工坊已启用。" : "文档工坊已停用。"));
    } catch (error) {
      if (current === projectTools) projectToolsFeedback(error.message, true);
    } finally {
      if (current === projectTools) { current.applying = ""; renderProjectTools(); }
    }
  }
  function showLogin() {
    state.home = null; state.model = null; window.clearTimeout(state.chat.poll);
    window.clearTimeout(apps.loginTimer);
    apps = newAppView();
    projectTools = newProjectToolsView();
    $("#apps-list").replaceChildren();
    appFeedback("");
    $("#project-tools-list").replaceChildren();
    projectToolsFeedback("");
    setActiveView("chat");
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
    if (home.initialized && home.chat_available !== false) {
      await Promise.all([loadModel(state.model === null), loadChat(true), activeView === "apps" ? loadApps() : undefined, activeView === "project-tools" ? loadProjectTools() : undefined]);
    }
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
  function appendArtifactDownloads(bubble, text) {
    const identifiers = new Set(String(text || "").match(/\bwork-[a-f0-9]{64}\b/g) || []);
    if (!identifiers.size) return;
    const downloads = appElement("div", "", "artifact-downloads");
    for (const identifier of identifiers) {
      const link = document.createElement("a");
      link.className = "artifact-download";
      link.textContent = "下载已登记成品";
      link.href = "/api/local/artifacts/" + encodeURIComponent(identifier) + "/download";
      link.setAttribute("download", "");
      downloads.append(link);
    }
    bubble.append(downloads);
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
      const text = itemText(item), body = document.createElement("p"); body.className = "bubble-text"; body.textContent = text; bubble.append(meta, body);
      if (assistant) appendArtifactDownloads(bubble, text);
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
  for (const [index, name] of tabs.entries()) {
    const tab = $(`#${name}-tab`);
    tab.addEventListener("click", () => { showMessage(""); setActiveView(name); });
    tab.addEventListener("keydown", (event) => {
      const positions = { ArrowLeft: (index + tabs.length - 1) % tabs.length, ArrowRight: (index + 1) % tabs.length, Home: 0, End: tabs.length - 1 };
      if (!Object.hasOwn(positions, event.key)) return;
      event.preventDefault();
      const selected = tabs[positions[event.key]];
      showMessage("");
      setActiveView(selected);
      $(`#${selected}-tab`).focus();
    });
  }
  $("#apps-refresh").addEventListener("click", () => { appFeedback(""); loadApps(); });
  $("#project-tools-refresh").addEventListener("click", () => { projectToolsFeedback(""); loadProjectTools(); });
  $("#open-settings-button").addEventListener("click", () => setActiveView("settings")); $("#back-to-chat-button").addEventListener("click", () => setActiveView("chat"));
  $("#model-form").addEventListener("submit", saveModel); $("#chat-form").addEventListener("submit", (event) => { event.preventDefault(); sendMessage($("#chat-input").value); });
  $("#load-older-button").addEventListener("click", () => loadChat(false));
  loadSession();
})();
