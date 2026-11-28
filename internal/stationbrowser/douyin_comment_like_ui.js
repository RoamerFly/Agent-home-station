(input) => {
  // Only the observed Douyin comment card and an exact local test fixture are
  // recognized. Any changed or ambiguous markup must fail closed.
  const visible = el => !!el && el.getClientRects().length > 0 &&
    getComputedStyle(el).display !== 'none' && getComputedStyle(el).visibility !== 'hidden';
  let wanted;
  try { wanted = new URL(input.target); } catch { return {invalid: true}; }
  if (location.origin !== wanted.origin || location.pathname !== wanted.pathname ||
      location.search !== wanted.search || location.hash !== wanted.hash) return {invalid: true};
  const page = {ready: document.readyState === 'complete', url: location.href};
  if (!page.ready) return page;
  if ([...document.querySelectorAll('input[type=password],form[action*="login"],iframe[src*="captcha"],iframe[src*="verify"]')].some(visible)) {
    return {...page, login: true};
  }
  if (input.action !== 'query' && input.action !== 'submit') return {invalid: true};
  const nativeSelector = 'div.dIlRe7ZV';
  const itemSelector = '[data-e2e="comment-item"]';
  const owner = node => node.closest(itemSelector) || node.closest(nativeSelector);
  const own = (node, card) => owner(node) === card;
  const cards = [...document.querySelectorAll(itemSelector + ',' + nativeSelector)]
    .filter(card => card.matches(itemSelector) || !card.closest(itemSelector))
    .filter(visible).filter(card => {
    const explicit = card.getAttribute('data-comment-id');
    const tooltips = [...card.querySelectorAll('[id^="tooltip_"]')].filter(node => own(node, card));
    const tooltip = tooltips.length === 1 ? tooltips[0].id.slice('tooltip_'.length) : '';
    return (explicit === input.commentID || tooltip === input.commentID);
  });
  if (cards.length !== 1) return {...page, code: 'comment_unavailable'};
  const card = cards[0];
  const explicit = card.getAttribute('data-comment-id');
  const tooltips = [...card.querySelectorAll('[id^="tooltip_"]')].filter(node => own(node, card));
  if (tooltips.length > 1 || (explicit && explicit !== input.commentID) ||
      (tooltips.length === 1 && tooltips[0].id !== 'tooltip_' + input.commentID)) {
    return {...page, code: 'comment_identity_ambiguous'};
  }
  const authors = [...card.querySelectorAll('[data-sec-uid],.comment-item-info-wrap a[href]')].filter(node => own(node, card) && visible(node));
  const identities = authors.map(node => {
    const explicitID = node.getAttribute('data-sec-uid');
    if (explicitID) return explicitID;
    try {
      const url = new URL(node.getAttribute('href'), location.origin);
      const match = url.origin === location.origin && /^\/user\/([A-Za-z0-9._-]{1,256})\/?$/.exec(url.pathname);
      return match ? match[1] : '';
    } catch { return ''; }
  }).filter(Boolean);
  if (identities.length !== 1) return {...page, code: 'comment_author_unavailable'};
  if (identities[0] !== input.authorID) return {...page, code: 'comment_author_changed'};
  const bodies = [...card.querySelectorAll('[data-e2e="comment-content"],.FduGc_lz')].filter(node => own(node, card) && visible(node));
  if (bodies.length !== 1) return {...page, code: 'comment_text_unavailable'};
  if ((bodies[0].innerText || bodies[0].textContent || '').trim() !== input.text.trim()) {
    return {...page, code: 'comment_changed'};
  }
  // The native control is the first statistics paragraph in the exact card.
  // Both visual states were observed on the same comment; every other markup
  // state remains unknown and must not be clicked.
  const stats = [...card.querySelectorAll('.comment-item-stats-container')].filter(node => own(node, card) && visible(node));
  if (stats.length > 1) return {...page, code: 'comment_like_control_unavailable'};
  if (stats.length === 1) {
    const groups = [...stats[0].querySelectorAll(':scope > div.a2ls69qS')].filter(visible);
    if (groups.length !== 1) return {...page, code: 'comment_like_control_unavailable'};
    const paragraphs = [...groups[0].querySelectorAll(':scope > p')].filter(visible);
    const first = paragraphs[0];
    if (!first || !first.classList.contains('VpA2NKl1')) return {...page, code: 'comment_like_control_unavailable'};
    const paths = [...first.querySelectorAll('svg path')];
    const counts = [...first.querySelectorAll('span')].filter(visible);
    if (paths.length !== 1 || counts.length !== 1 || !(counts[0].textContent || '').trim() ||
        paths[0].getAttribute('opacity') !== '.9') return {...page, code: 'comment_like_state_unavailable'};
    const state = () => {
      const fill = paths[0].getAttribute('fill')?.toUpperCase();
      if (first.classList.contains('tOgCrAK_') && fill === '#FE2C55') return true;
      if (!first.classList.contains('tOgCrAK_') && fill === '#FFF') return false;
      return null;
    };
    const before = state();
    if (before === null) return {...page, code: 'comment_like_state_unavailable'};
    if (before) return {...page, liked: true};
    if (input.action === 'query') return {...page, liked: false};
    first.click();
    return {...page, clicked: true, liked: state() === true};
  }
  const controls = [...card.querySelectorAll('[data-e2e="comment-like-button"]')].filter(node => own(node, card) && visible(node));
  if (controls.length !== 1) return {...page, code: 'comment_like_control_unavailable'};
  const control = controls[0];
  if (!control.matches('button,[role="button"]') || control.disabled || control.getAttribute('aria-disabled') === 'true') {
    return {...page, code: 'comment_like_control_unavailable'};
  }
  const state = () => {
    const pressed = control.getAttribute('aria-pressed');
    const liked = control.getAttribute('data-liked');
    if (pressed === null && liked === null) return null;
    if ((pressed !== null && pressed !== 'true' && pressed !== 'false') ||
        (liked !== null && liked !== 'true' && liked !== 'false') ||
        (pressed !== null && liked !== null && pressed !== liked)) return null;
    return (pressed ?? liked) === 'true';
  };
  const before = state();
  if (before === null) return {...page, code: 'comment_like_state_unavailable'};
  if (before) return {...page, liked: true};
  if (input.action === 'query') return {...page, liked: false};
  control.click();
  // A single click is permitted. The caller may only query after this point.
  return {...page, clicked: true, liked: state() === true};
}
