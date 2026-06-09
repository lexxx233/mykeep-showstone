() => {
  // Clear markers from the previous snapshot, then re-index from scratch.
  document.querySelectorAll('[data-showstone-idx]').forEach(e => e.removeAttribute('data-showstone-idx'));

  const visible = (el) => {
    const r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return false;
    const s = window.getComputedStyle(el);
    return s.display !== 'none' && s.visibility !== 'hidden' && s.opacity !== '0';
  };

  const SEL = 'a[href], button, input:not([type=hidden]), select, textarea,' +
    '[role=button], [role=link], [role=tab], [role=menuitem], [role=checkbox],' +
    '[role=radio], [role=combobox], [role=switch], [contenteditable=true], [onclick], [tabindex]';

  const roleFor = (el, tag) => {
    const explicit = el.getAttribute('role');
    if (explicit) return explicit;
    const map = { a: 'link', button: 'button', select: 'combobox', textarea: 'textbox' };
    if (tag === 'input') return (el.getAttribute('type') || 'text');
    return map[tag] || tag;
  };

  const seen = new Set();
  const elements = [];
  let idx = 0;
  document.querySelectorAll(SEL).forEach(el => {
    if (seen.has(el) || !visible(el)) return;
    seen.add(el);
    el.setAttribute('data-showstone-idx', String(idx));
    const tag = el.tagName.toLowerCase();
    const itype = tag === 'input' ? (el.getAttribute('type') || 'text') : '';
    // Never expose secret field VALUES (passwords, card numbers) to the agent.
    const hay = ((el.getAttribute('name') || '') + ' ' + (el.getAttribute('placeholder') || '') +
      ' ' + (el.getAttribute('aria-label') || '')).toLowerCase();
    const secret = itype.toLowerCase() === 'password' ||
      /password|card|cvv|cvc|credit|iban|ssn|social security|account number/.test(hay);
    const name = (el.getAttribute('aria-label') || (el.innerText || '').trim() || (secret ? '' : el.value) ||
      el.getAttribute('placeholder') || el.getAttribute('title') || el.getAttribute('alt') || '')
      .replace(/\s+/g, ' ').trim().slice(0, 200);
    const r = el.getBoundingClientRect();
    const isCheck = el.type === 'checkbox' || el.type === 'radio';
    elements.push({
      index: idx, role: roleFor(el, tag), tag: tag, name: name,
      value: secret ? '' : (el.value || '').slice(0, 200),
      placeholder: el.getAttribute('placeholder') || '',
      href: el.getAttribute('href') || '',
      input_type: tag === 'input' ? (el.getAttribute('type') || 'text') : '',
      checked: isCheck ? !!el.checked : null,
      disabled: !!el.disabled,
      bbox: [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)]
    });
    idx++;
  });

  const main = document.querySelector('main, article, [role=main]') || document.body;
  const text = ((main && main.innerText) || '').replace(/\n{3,}/g, '\n\n').trim();

  return JSON.stringify({ url: location.href, title: document.title, text: text, elements: elements });
}
