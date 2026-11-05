(() => {
  'use strict';
  function element(parent, tag, text, className) {
    const child = document.createElement(tag);
    if (text !== undefined) child.textContent = text;
    if (className) child.className = className;
    parent.append(child);
    return child;
  }

  function button(parent, text, onClick, className = 'secondary') {
    const child = element(parent, 'button', text, className);
    child.type = 'button';
    child.addEventListener('click', onClick);
    return child;
  }

  window.WechatControls = {element, button};
})();
