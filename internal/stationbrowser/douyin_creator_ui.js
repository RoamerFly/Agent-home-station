(input) => {
  const visible = e => e && e.getClientRects().length > 0 && getComputedStyle(e).visibility !== "hidden" && getComputedStyle(e).display !== "none";
  const locationOK = location.origin === input.origin && location.pathname.startsWith("/creator-micro/");
  if (!locationOK) return { invalid: true };
  const visibleLogin = [...document.querySelectorAll('input[type=password],form[action*="login"],iframe[src*="captcha"],iframe[src*="verify"]')].some(visible);
  if (visibleLogin) return { login: true };
  if (input.action === "query") {
    if (!location.pathname.startsWith("/creator-micro/content/manage")) return { invalid: true };
    const matches = [...document.querySelectorAll("a[href]")].filter(visible).filter(e => { try {
      const url = new URL(e.href, location.href);
      return url.origin === "https://www.douyin.com" && url.pathname === "/video/" + input.remoteID && !url.username && !url.password;
    } catch { return false; }});
    // A work still under moderation is accepted, not proven published. An
    // unrelated page-wide success toast is never evidence for this work.
    const published = matches.some(e => {
      const card = e.closest('article,[data-e2e="work-item"],[data-e2e="video-card"]');
      return !!card && /(?:^|\s)(?:已发布|发布成功)(?:\s|$)/u.test(card.innerText);
    });
    return { listed: matches.length > 0, published };
  }
  const files = [...document.querySelectorAll('input[type=file]')].filter(e => /video|mp4|webm/i.test(e.accept));
  const titles = [...document.querySelectorAll('input[placeholder*="标题"],input[aria-label*="标题"]')].filter(visible);
  const editors = [...document.querySelectorAll('[contenteditable=true][data-placeholder*="描述"],[contenteditable=true][data-placeholder*="简介"],[contenteditable=true][role=textbox],textarea[placeholder*="描述"],textarea[placeholder*="简介"]')].filter(visible);
  const publishButtons = [...document.querySelectorAll('button,[role=button]')].filter(e => visible(e) && e.textContent.trim() === "发布");
  const readText = e => e && (e instanceof HTMLInputElement || e instanceof HTMLTextAreaElement) ? e.value : (e?.innerText || e?.textContent || "");
  const setText = (e, text) => {
    if (e instanceof HTMLInputElement || e instanceof HTMLTextAreaElement) {
      const owner = e instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(owner, "value").set.call(e, text);
    } else { e.textContent = text; }
    e.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertText", data: text }));
    e.dispatchEvent(new Event("change", { bubbles: true }));
  };
  const caption = [input.title, input.body].filter(Boolean).join("\n");
  if (input.action === "pin-file") {
    if (files.length !== 1 || files[0].files.length !== 1) return { invalid: true };
    const file = files[0].files[0];
    globalThis.__ahsCreatorFile = { node: files[0], name: file.name, size: file.size };
    return { selected: true };
  }
  if (input.action === "fill") {
    if (titles.length > 1 || editors.length !== 1 || (titles.length === 1 && titles[0].maxLength > 0 && input.title.length > titles[0].maxLength)) return { invalid: true };
    if (titles.length === 1) setText(titles[0], input.title);
    setText(editors[0], titles.length === 1 ? input.body : caption);
  }
  const wanted = { public: /^(公开|所有人可见)$/, private: /^(私密|仅自己可见|仅自己)$/, friends: /^(好友可见|互关朋友可见)$/ }[input.visibility];
  const choices = [...document.querySelectorAll('label,[role=radio]')].filter(e => visible(e) && wanted?.test(e.textContent.replace(/\s+/g, "").trim()));
  const selected = e => e.getAttribute("aria-checked") === "true" || e.querySelector('input[type=radio]')?.checked === true || (e instanceof HTMLInputElement && e.checked);
  if (input.action === "fill" && choices.length === 1 && !selected(choices[0])) choices[0].click();
  const visibilityOK = choices.length === 1 && selected(choices[0]);
  const filled = editors.length === 1 && readText(editors[0]).trim() === (titles.length === 1 ? input.body : caption).trim() &&
    (titles.length === 0 || readText(titles[0]) === input.title);
  const button = publishButtons.length === 1 ? publishButtons[0] : null;
  const enabled = !!button && !button.disabled && button.getAttribute("aria-disabled") !== "true";
  const pin = globalThis.__ahsCreatorFile;
  const fileStillBound = !!pin && pin.size > 0 && pin.node.isConnected && pin.node.files.length === 1 &&
    pin.node.files[0].name === pin.name && pin.node.files[0].size === pin.size;
  if (input.action === "submit") {
    if (!filled || !visibilityOK || !enabled || !fileStillBound) return { invalid: true };
    button.click();
    return { clicked: true };
  }
  return { fileAvailable: files.length === 1, editorAvailable: editors.length === 1 && titles.length <= 1,
    filled, visibilityOK, enabled, fileStillBound };
}
