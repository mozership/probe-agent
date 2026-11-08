// The part of the agent that runs in the page. It is evaluated in every
// document the tab opens, and installs window.__probeAgent once. It tells
// the model what the page has as lines that each end with a ref, such as
//
//   textbox "Email" type=email required [ref_3]
//
// and finds the element of a ref again for the actions. Elements keep their
// ref while they are in the document, and the page is never written to, so
// that the application sees nothing of the agent.
(() => {
  if (window.__probeAgent) return;

  // A link that opens a window opens in the tab instead, since the agent
  // drives one tab.
  document.addEventListener('click', (e) => {
    const a = e.target instanceof Element && e.target.closest('a[target]');
    if (a && a.target !== '_self') a.target = '_self';
  }, true);
  window.open = (url) => { if (url) location.href = url; return window; };

  const refs = new Map();
  const ids = new WeakMap();
  let next = 1;

  const refOf = (el) => {
    let r = ids.get(el);
    if (!r) {
      r = 'ref_' + next++;
      ids.set(el, r);
      refs.set(r, new WeakRef(el));
    }
    return r;
  };

  const byRef = (ref) => {
    const w = refs.get(ref);
    const el = w && w.deref();
    if (!el || !el.isConnected) {
      throw new Error(ref + ' is not on the page any more; read the page again');
    }
    return el;
  };

  const skipped = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'HEAD', 'META', 'LINK', 'svg', 'SVG', 'PATH']);

  const shown = (el) => {
    const s = getComputedStyle(el);
    if (s.visibility === 'hidden' || s.visibility === 'collapse' || s.opacity === '0') return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };

  const inViewport = (el) => {
    const r = el.getBoundingClientRect();
    return r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth;
  };

  const inputRoles = {
    button: 'button', submit: 'button', reset: 'button', image: 'button', file: 'button',
    checkbox: 'checkbox', radio: 'radio', range: 'slider', number: 'spinbutton', search: 'searchbox',
  };

  const tagRoles = {
    BUTTON: 'button', SUMMARY: 'button', TEXTAREA: 'textbox', OPTION: 'option',
    H1: 'heading', H2: 'heading', H3: 'heading', H4: 'heading', H5: 'heading', H6: 'heading',
    NAV: 'navigation', MAIN: 'main', FORM: 'form', DIALOG: 'dialog', UL: 'list', OL: 'list', LI: 'listitem',
    TABLE: 'table', TR: 'row', HEADER: 'banner', FOOTER: 'contentinfo', ASIDE: 'complementary',
  };

  const roleOf = (el) => {
    const explicit = el.getAttribute('role');
    if (explicit) return explicit.trim().split(/\s+/)[0];
    switch (el.tagName) {
      case 'A': return el.hasAttribute('href') ? 'link' : '';
      case 'SELECT': return el.multiple ? 'listbox' : 'combobox';
      case 'IMG': return el.getAttribute('alt') ? 'img' : '';
      case 'INPUT': {
        const t = (el.getAttribute('type') || 'text').toLowerCase();
        if (t === 'hidden') return '';
        return inputRoles[t] || 'textbox';
      }
    }
    if (tagRoles[el.tagName]) return tagRoles[el.tagName];
    if (el.isContentEditable && el.getAttribute('contenteditable') !== null) return 'textbox';
    return '';
  };

  const interactiveRoles = new Set([
    'link', 'button', 'checkbox', 'radio', 'switch', 'textbox', 'searchbox', 'combobox', 'listbox',
    'slider', 'spinbutton', 'tab', 'menuitem', 'menuitemcheckbox', 'menuitemradio', 'option', 'treeitem',
  ]);

  // Roles that hold other elements, shown as a line of their own with what
  // they hold below it.
  const containerRoles = new Set([
    'navigation', 'main', 'form', 'dialog', 'alertdialog', 'list', 'listitem', 'table', 'row',
    'banner', 'contentinfo', 'complementary', 'region', 'alert', 'status', 'group', 'radiogroup',
    'tablist', 'menu', 'menubar', 'tree', 'grid',
  ]);

  const fieldRoles = new Set(['textbox', 'searchbox', 'combobox', 'listbox', 'spinbutton', 'slider']);

  const interactive = (el, role) =>
    interactiveRoles.has(role) || el.hasAttribute('onclick') ||
    (el.hasAttribute('tabindex') && el.tabIndex >= 0 && role !== '' && !containerRoles.has(role));

  const squash = (s) => (s || '').replace(/\s+/g, ' ').trim();
  const clip = (s, n) => { s = squash(s); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
  const quote = (s) => JSON.stringify(s);

  // each calls f on the elements of root in the order of the page, those of
  // an open shadow root right after its host. A closed shadow root is out of
  // reach of a script, so its elements are not seen.
  const each = (root, f) => {
    for (const el of root.querySelectorAll('*')) {
      f(el);
      if (el.shadowRoot) each(el.shadowRoot, f);
    }
  };

  // hasShadow tells whether el, or an element under it, has an open shadow
  // root or is a slot: innerText leaves out the text of a shadow root, and
  // that of the nodes slotted into a slot.
  const hasShadow = (el) => {
    if (el.shadowRoot || el.localName === 'slot') return true;
    for (const n of el.querySelectorAll('*')) if (n.shadowRoot || n.localName === 'slot') return true;
    return false;
  };

  const box = (el) => Boolean(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
  const inline = (el) => /^(inline|contents|ruby)/.test(getComputedStyle(el).display);
  // localName, as the title of an SVG is "title" and not "TITLE".
  const noText = new Set(['script', 'style', 'template', 'noscript', 'head', 'title', 'desc']);

  // leaf gives the visible text of an element with no shadow root under it.
  // An SVG has no innerText, and shows only the text of its text elements.
  const leaf = (el) => {
    if (el instanceof HTMLElement) return el.innerText;
    if (el instanceof SVGElement) {
      const texts = el.localName === 'text' ? [el] : Array.from(el.querySelectorAll('text'));
      return texts.map((t) => t.textContent).join(' ');
    }
    return el.textContent || '';
  };

  // rendered gives the text of a node of the flat tree, with a line break
  // around each block, as innerText puts them: an element's innerText when
  // no shadow root is under it, and otherwise the text of the children of
  // its shadow root, or of its own, and of the nodes slotted into it.
  const rendered = (node) => {
    if (node.nodeType === Node.TEXT_NODE) {
      // A text node at the top of a shadow root takes its style from the host.
      const parent = node.parentElement || (node.parentNode && node.parentNode.host);
      if (!parent || !box(parent)) return '';
      const style = getComputedStyle(parent);
      if (style.visibility !== 'visible') return '';
      return style.whiteSpace.startsWith('pre') ? node.nodeValue : node.nodeValue.replace(/\s+/g, ' ');
    }
    if (node.nodeType !== Node.ELEMENT_NODE || noText.has(node.localName)) return '';
    if (node.localName === 'br') return '\n';
    if (node.localName === 'slot') return node.assignedNodes({ flatten: true }).map(rendered).join('');
    if (!box(node)) return '';
    let t;
    if (!hasShadow(node)) {
      t = leaf(node);
    } else {
      const children = node.shadowRoot ? node.shadowRoot.childNodes : node.childNodes;
      t = Array.from(children, rendered).join('');
    }
    return inline(node) ? t : '\n' + t + '\n';
  };

  // textOf gives the visible text of el, as innerText does, and the text of
  // the open shadow roots in it, which innerText leaves out.
  const textOf = (el) => {
    if (!box(el)) return '';
    if (!hasShadow(el)) return leaf(el);
    // The lines of the parts are put together the way innerText would:
    // trimmed, and the empty ones dropped.
    return rendered(el).split('\n').map((l) => l.replace(/[ \t]+/g, ' ').trim()).filter((l) => l !== '').join('\n');
  };

  const nameOf = (el, role) => {
    const label = el.getAttribute('aria-label');
    if (label) return label;
    const by = el.getAttribute('aria-labelledby');
    if (by) {
      // The ids are looked up where el is, which is a shadow root for an
      // element in one. A label may be hidden, and still names el.
      const root = el.getRootNode();
      const t = by.split(/\s+/).map((id) => {
        const l = root.getElementById ? root.getElementById(id) : document.getElementById(id);
        return l ? textOf(l) || l.textContent : '';
      }).join(' ');
      if (squash(t)) return t;
    }
    if (el.labels && el.labels.length) {
      const t = Array.from(el.labels).map((l) => textOf(l)).join(' ');
      if (squash(t)) return t;
    }
    if (el.tagName === 'INPUT' && ['button', 'submit', 'reset'].includes(el.type)) return el.value || el.type;
    if (el.tagName === 'IMG' || (el.tagName === 'INPUT' && el.type === 'image')) return el.alt || '';
    if (fieldRoles.has(role)) {
      return el.getAttribute('placeholder') || el.getAttribute('title') || el.getAttribute('name') || '';
    }
    return squash(textOf(el) || el.textContent) || el.getAttribute('title') || '';
  };

  const describe = (el, role) => {
    let line = role + ' ' + quote(clip(nameOf(el, role), 100));
    const props = [];
    const level = el.getAttribute('aria-level') || (/^H[1-6]$/.test(el.tagName) ? el.tagName[1] : '');
    if (role === 'heading' && level) props.push('level=' + level);
    if (el.tagName === 'INPUT' && !['text', 'checkbox', 'radio', 'submit', 'button'].includes(el.type)) {
      props.push('type=' + el.type);
    }
    if (el.tagName === 'SELECT') {
      const chosen = Array.from(el.selectedOptions).map((o) => o.label).join(', ');
      if (chosen) props.push('value=' + quote(clip(chosen, 80)));
      props.push('options=' + quote(Array.from(el.options).slice(0, 30).map((o) => clip(o.label, 40)).join(' | ')));
    } else if (fieldRoles.has(role)) {
      const v = 'value' in el ? el.value : el.innerText;
      if (v) props.push('value=' + quote(el.type === 'password' ? '••••' : clip(v, 80)));
    }
    if (['checkbox', 'radio', 'switch'].includes(role)) {
      props.push(el.checked || el.getAttribute('aria-checked') === 'true' ? 'checked' : 'unchecked');
    }
    if (el.disabled || el.getAttribute('aria-disabled') === 'true') props.push('disabled');
    if (el.required || el.getAttribute('aria-required') === 'true') props.push('required');
    if (el.getAttribute('aria-invalid') === 'true') props.push('invalid');
    if (role === 'link') {
      const href = el.getAttribute('href');
      if (href) props.push('href=' + quote(clip(href, 80)));
    }
    if (props.length) line += ' ' + props.join(' ');
    return line;
  };

  const maxChars = 50000;

  // read returns the lines of the page: every element shown in the
  // viewport, or with filter "all" also those outside it, or with filter
  // "interactive" only those one can act on. depth bounds how far
  // containers nest, and ref reads only that element and what it holds.
  const read = (filter, depth, ref) => {
    const all = filter === 'all';
    const onlyInteractive = filter === 'interactive';
    const out = [];
    let size = 0;
    let truncated = false;
    const push = (d, s) => {
      if (truncated) return;
      const line = '  '.repeat(d) + s;
      if (size + line.length + 1 > maxChars) { truncated = true; return; }
      out.push(line);
      size += line.length + 1;
    };
    const visible = (el) => shown(el) && (all || inViewport(el));

    const walk = (node, d) => {
      for (const child of node.childNodes) {
        if (truncated) return;
        if (child.nodeType === Node.TEXT_NODE) {
          const t = squash(child.textContent);
          if (t && !onlyInteractive && child.parentElement && visible(child.parentElement)) {
            push(d, 'text ' + quote(clip(t, 300)));
          }
          continue;
        }
        if (child.nodeType !== Node.ELEMENT_NODE) continue;
        visit(child, d);
      }
    };

    const visit = (el, d) => {
      if (skipped.has(el.tagName) || el.getAttribute('aria-hidden') === 'true') return;
      if (getComputedStyle(el).display === 'none') return;
      const role = roleOf(el);
      if (interactive(el, role) || ((role === 'heading' || role === 'img') && !onlyInteractive)) {
        if (visible(el)) push(d, describe(el, role) + ' [' + refOf(el) + ']');
        return;
      }
      if (containerRoles.has(role) && !onlyInteractive && shown(el)) {
        if (d >= depth) return;
        const before = out.length;
        const label = el.getAttribute('aria-label');
        push(d, role + (label ? ' ' + quote(clip(label, 80)) : '') + ' [' + refOf(el) + ']');
        walk(el, d + 1);
        if (el.shadowRoot) walk(el.shadowRoot, d + 1);
        if (out.length === before + 1 && !['alert', 'status'].includes(role)) {
          size -= out.pop().length + 1;
        }
        return;
      }
      walk(el, d);
      if (el.shadowRoot) walk(el.shadowRoot, d);
    };

    if (ref) {
      visit(byRef(ref), 0);
    } else if (document.body) {
      walk(document.body, 0);
    }
    if (truncated) out.push('[The page is longer than ' + maxChars + ' characters and was cut here; read less of it with depth or ref.]');
    return out.join('\n');
  };

  // point scrolls the element of ref into view and returns the middle of
  // it, where a click goes.
  const point = (ref) => {
    const el = byRef(ref);
    el.scrollIntoView({ block: 'center', inline: 'center' });
    const r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) throw new Error(ref + ' is not shown on the page');
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  };

  const scrollTo = (ref) => {
    byRef(ref).scrollIntoView({ block: 'center', inline: 'center' });
  };

  const focus = (ref) => {
    byRef(ref).focus();
  };

  // formInput sets the value of a form field as a user's input does, firing
  // input and change, and says what it did.
  const formInput = (ref, value) => {
    const el = byRef(ref);
    const fire = () => {
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
    };
    if (el.disabled) throw new Error(ref + ' is disabled');
    if (el.tagName === 'SELECT') {
      const want = String(value);
      const opts = Array.from(el.options);
      const opt = opts.find((o) => o.value === want) || opts.find((o) => squash(o.label) === squash(want));
      if (!opt) throw new Error('no option ' + quote(want) + ' in ' + ref + '; the options are ' + quote(opts.map((o) => o.label)));
      if (opt.disabled) throw new Error('the option ' + quote(opt.label) + ' is disabled');
      if (el.multiple) opt.selected = true; else el.value = opt.value;
      fire();
      return 'Selected ' + quote(opt.label) + ' in ' + ref + '.';
    }
    if (el.type === 'checkbox' || el.type === 'radio') {
      const want = value === true || value === 'true' || value === 'on' || value === 1 || value === '1';
      if (el.checked !== want) el.click();
      return (el.checked ? 'Checked ' : 'Unchecked ') + ref + '.';
    }
    if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') {
      const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, String(value));
      fire();
      return 'Set the value of ' + ref + '.';
    }
    if (el.isContentEditable) {
      el.textContent = String(value);
      el.dispatchEvent(new Event('input', { bubbles: true }));
      return 'Set the text of ' + ref + '.';
    }
    throw new Error(ref + ' is not a form field');
  };

  // first returns the first element that matches css, in the document or in
  // an open shadow root, or null.
  const first = (css) => {
    let found = null;
    each(document, (el) => { if (!found && el.matches(css)) found = el; });
    return found;
  };

  // text returns the text the page shows, its main content first, the text
  // of the open shadow roots included.
  const text = () => {
    const body = document.body ? textOf(document.body) : '';
    const main = first('main, [role=main]');
    if (!main) return body;
    const inMain = textOf(main);
    const rest = body.replace(inMain, '');
    return inMain + (squash(rest) ? '\n\n' + rest : '');
  };

  // The elements shown with role and name, in the order of the document,
  // shadow roots included.
  const withRoleAndName = (role, name) => {
    const out = [];
    each(document, (el) => {
      if (roleOf(el) === role && clip(nameOf(el, role), 200) === name && shown(el)) out.push(el);
    });
    return out;
  };

  // locatorOf describes el by its role, its name, and, when other elements
  // have both, which of how many it is. An element with no name among
  // others of its role has nothing but its order to tell it by, which rows
  // that reorder would give to another, so it is not described.
  const locatorOf = (el) => {
    const role = roleOf(el);
    if (!role) return null;
    const name = clip(nameOf(el, role), 200);
    const twins = withRoleAndName(role, name);
    if (twins.length <= 1) return { role, name };
    if (!name) return null;
    return { role, name, nth: Math.max(twins.indexOf(el), 0), of: twins.length };
  };

  // locator describes the element of ref, or the one one can act on at x,
  // y, so that a later run can find it again with locate. It is null for a
  // point with nothing to act on, and for an element it cannot describe.
  const locator = (ref, x, y) => {
    if (ref) return locatorOf(byRef(ref));
    let el = document.elementFromPoint(x, y);
    while (el && !interactive(el, roleOf(el))) el = el.parentElement;
    return el ? locatorOf(el) : null;
  };

  // locate finds the element a locator describes, and returns its ref and
  // how many elements have its role and name. The ref is '' unless that
  // tells the element: one element where the locator counted one, or as
  // many as it counted, of which it takes the one at its place.
  const locate = (loc) => {
    const found = withRoleAndName(loc.role, loc.name);
    const of = loc.of || 1;
    const el = found.length === of ? found[loc.nth || 0] : null;
    return { ref: el ? refOf(el) : '', count: found.length };
  };

  // alerts returns the texts of the alerts the page shows, which say what
  // just happened, those in open shadow roots included.
  const alerts = () => {
    const out = [];
    each(document, (el) => {
      if (!el.matches('[role=alert], [role=alertdialog]')) return;
      const t = squash(textOf(el));
      if (t && shown(el)) out.push(clip(t, 300));
    });
    return out;
  };

  window.__probeAgent = { read, point, scrollTo, focus, formInput, text, locator, locate, alerts };
})();
