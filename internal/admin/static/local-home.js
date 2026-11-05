(() => {
  "use strict";

  const $ = (selector) => document.querySelector(selector);
  const state = { csrf: "", home: null, busy: false };
  const loginView = $("#login-view");
  const homeView = $("#home-view");
  const message = $("#page-message");
  const loginSubmit = $("#login-submit");
  const initializeButton = $("#initialize-button");
  const accessButton = $("#access-button");
  const logoutButton = $("#logout-button");

  function showMessage(text, kind = "") {
    message.textContent = text || "";
    message.className = `page-message${kind ? ` is-${kind}` : ""}`;
  }

  function setBusy(busy) {
    state.busy = busy;
    loginSubmit.disabled = busy;
    initializeButton.disabled = busy;
    accessButton.disabled = busy;
    logoutButton.disabled = busy;
  }

  function responseMessage(status) {
    if (status === 401 || status === 403) return "登录状态已失效，请重新验证。";
    if (status === 409) return "当前状态已更新，请确认最新信息后再操作。";
    return "暂时无法完成操作，请确认本机服务后再试。";
  }

  async function request(url, options = {}) {
    const headers = new Headers(options.headers || {});
    headers.set("Accept", "application/json");
    if (options.body) headers.set("Content-Type", "application/json");
    if (state.csrf && options.method && options.method !== "GET") headers.set("X-CSRF-Token", state.csrf);
    let response;
    try {
      response = await fetch(url, { ...options, headers, credentials: "same-origin" });
    } catch (_) {
      const error = new Error("network");
      error.status = 0;
      throw error;
    }
    let body = null;
    try { body = await response.json(); } catch (_) { /* Do not expose non-JSON bodies to the page. */ }
    if (!response.ok) {
      const error = new Error(responseMessage(response.status));
      error.status = response.status;
      throw error;
    }
    return body || {};
  }

  function authenticated(session) {
    return Boolean(session && (session.authenticated || session.logged_in || session.csrf));
  }

  function showLogin() {
    state.home = null;
    homeView.hidden = true;
    loginView.hidden = false;
    $("#login-password").value = "";
  }

  function showHome(home) {
    state.home = home;
    loginView.hidden = true;
    homeView.hidden = false;
    const initialized = Boolean(home.initialized);
    $("#status-kicker").textContent = initialized ? "专属空间" : "准备中";
    $("#status-title").textContent = initialized ? "家已准备好" : "为大肥鱼准备家";
    $("#stage-note").textContent = initialized
      ? "她的日记、作品与工具将留在这里。软件内聊天即将接入。"
      : "准备好后，她可以在这里整理自己的作品与工具。软件内聊天即将接入。";
    initializeButton.hidden = initialized;
    const enabled = Boolean(home.home_access);
    accessButton.hidden = !initialized;
    accessButton.textContent = enabled ? "暂停工作空间授权" : "恢复工作空间授权";
    accessButton.className = `button ${enabled ? "secondary" : "primary"}`;
    const chip = $("#access-chip");
    chip.textContent = !initialized ? "尚未准备" : enabled ? "工作空间已授权" : "工作空间已暂停";
    chip.classList.toggle("is-live", enabled);
    const details = $("#home-details");
    details.hidden = !initialized;
    $("#home-directory").textContent = home.directory || "专属目录正在准备";
    for (const [selector, id, fallback] of [["#fish-id", home.fish_id, "大肥鱼"], ["#owner-id", home.owner_id, "本机主人"]]) {
      const field = $(selector);
      field.textContent = id ? `${String(id).slice(0, 11)}…${String(id).slice(-6)}` : fallback;
      field.title = id || fallback;
    }
  }

  async function loadHome() {
    const home = await request("/api/local/home");
    showHome(home);
  }

  async function loadSession() {
    try {
      const session = await request("/api/session");
      state.csrf = String(session.csrf || session.csrf_token || "");
      if (!authenticated(session)) {
        showLogin();
        return;
      }
      await loadHome();
    } catch (error) {
      if (error.status === 401 || error.status === 403) {
        state.csrf = "";
        showLogin();
        return;
      }
      showLogin();
      showMessage("暂时无法连接本机服务，请稍后再试。", "error");
    }
  }

  async function saveHome(action) {
    if (state.busy || !state.home) return;
    setBusy(true);
    showMessage("");
    try {
      const payload = { action };
      if (action !== "initialize") payload.revision = Number(state.home.revision || 0);
      await request("/api/local/home", { method: "POST", body: JSON.stringify(payload) });
      await loadHome();
      showMessage(action === "initialize" ? "大肥鱼的家已经准备好。" : "权限状态已更新。", "success");
    } catch (error) {
      if (error.status === 409) {
        try { await loadHome(); } catch (_) { /* Keep the existing, safe screen if refresh also fails. */ }
        showMessage("状态已经更新，已刷新当前信息。请确认后再操作。", "warn");
      } else if (error.status === 401 || error.status === 403) {
        state.csrf = "";
        showLogin();
        showMessage("登录状态已失效，请重新验证。", "warn");
      } else {
        showMessage(responseMessage(error.status), "error");
      }
    } finally {
      setBusy(false);
    }
  }

  $("#login-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (state.busy) return;
    const password = $("#login-password").value;
    setBusy(true);
    showMessage("");
    try {
      const session = await request("/api/login", { method: "POST", body: JSON.stringify({ password }) });
      state.csrf = String(session.csrf || session.csrf_token || "");
      await loadHome();
      showMessage("验证完成。", "success");
    } catch (error) {
      showMessage(error.status === 401 || error.status === 403 ? "密码不正确，请重新输入。" : responseMessage(error.status), "error");
    } finally {
      setBusy(false);
    }
  });

  initializeButton.addEventListener("click", () => saveHome("initialize"));
  accessButton.addEventListener("click", () => saveHome(state.home && state.home.home_access ? "pause" : "authorize"));
  logoutButton.addEventListener("click", async () => {
    if (state.busy) return;
    setBusy(true);
    try {
      await request("/api/logout", { method: "POST" });
      state.csrf = "";
      showLogin();
      showMessage("已退出登录。", "success");
    } catch (error) {
      showMessage("退出登录未完成，请重试。", "error");
    } finally {
      setBusy(false);
    }
  });

  loadSession();
})();
