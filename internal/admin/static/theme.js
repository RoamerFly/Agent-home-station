(() => {
  'use strict';
  const key = 'wechat_bot_admin_theme';
  const root = document.documentElement;
  const read = () => { try { return localStorage.getItem(key); } catch (_) { return null; } };
  const write = (value) => { try { localStorage.setItem(key, value); } catch (_) {} };
  const apply = (value) => {
    const theme = value === 'dark' ? 'dark' : 'light';
    root.dataset.theme = theme;
    root.style.colorScheme = theme;
    document.querySelectorAll('[data-theme-value]').forEach((button) => {
      const selected = button.dataset.themeValue === theme;
      button.setAttribute('aria-pressed', String(selected));
    });
  };
  root.dataset.theme = read() === 'dark' ? 'dark' : 'light';
  root.style.colorScheme = root.dataset.theme;
  document.addEventListener('DOMContentLoaded', () => {
    apply(read());
    document.querySelectorAll('[data-theme-value]').forEach((button) => {
      button.addEventListener('click', () => {
        const theme = button.dataset.themeValue === 'light' ? 'light' : 'dark';
        write(theme);
        apply(theme);
      });
    });
  });
})();
