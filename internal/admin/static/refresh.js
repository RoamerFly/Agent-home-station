/* A refresh belongs to its view. Registration follows panels moved by the console
   and is collected when a rendered panel is replaced. */
(() => {
  'use strict';
  const button = document.querySelector('#sidebar-refresh');
  const handlers = new WeakMap();
  function register(anchor, run) {
    anchor.dataset.refreshAnchor = '';
    if (!handlers.has(anchor)) handlers.set(anchor, new Set());
    handlers.get(anchor).add(run);
  }
  function bind(control, run) {
    control.dataset.refreshSource = '';
    register(control, run);
  }
  async function refresh() {
    if (button.disabled || !window.WechatAdmin?.isAuthenticated()) return;
    const calls = new Set();
    for (const anchor of document.querySelectorAll('[data-refresh-anchor]')) {
      let box = anchor.hasAttribute('data-refresh-source') ? anchor.parentElement : anchor;
      while (box && getComputedStyle(box).display === 'contents') box=box.parentElement;
      if (!box || !box.getClientRects().length || anchor.closest('[hidden], details:not([open])')) continue;
      for (const run of handlers.get(anchor)) calls.add(run);
    }
    if (!calls.size) calls.add(window.WechatAdmin.refreshStatus);
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    button.lastElementChild.textContent = '刷新中';
    try {
      const results = await Promise.allSettled(Array.from(calls, run => Promise.resolve().then(run)));
      const failure = results.find(result => result.status === 'rejected');
      if (failure) window.WechatAdmin.notice(failure.reason?.message || '刷新未完成', true);
    } finally {
      button.disabled = false;
      button.removeAttribute('aria-busy');
      button.lastElementChild.textContent = '刷新';
    }
  }
  button?.addEventListener('click', refresh);
  // A click during a background read joins the same work, including its rendering.
  function join(run) {
    let pending;
    return (...args) => {
      if (!pending) pending = Promise.resolve().then(() => run(...args)).finally(() => { pending = null });
      return pending;
    };
  }
  window.WechatRefresh = {bind, register, join};
})();
