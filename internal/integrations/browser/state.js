(() => {
  // This table lives in a CDP isolated world, separate from page scripts.
  const nodes = globalThis.__lokiNodes = [];
  const elements = [];
  let visited = 0;
  const truncate = (text, limit) => Array.from(text).slice(0, limit).join('');
  function childText(node, depth) {
    if (node.nodeType === Node.TEXT_NODE) return truncate(node.textContent, 200);
    if (depth < 0) return '';
    let text = '';
    for (const child of node.childNodes) {
      text += childText(child, depth - 1);
      if (text.length >= 200) break;
    }
    return truncate(text, 200);
  }
  function walk(root) {
    for (const element of root.children) {
      if (++visited > 100000 || nodes.length >= 1000) return;
      const view = element.ownerDocument.defaultView;
      const style = view.getComputedStyle(element);
      if (style.display === 'none' || style.visibility === 'hidden') continue;
      const rect = element.getBoundingClientRect();
      const interactive = element.matches('a[href],button,input:not([type="hidden"]),textarea,select,summary,[contenteditable="true"],[role="button"],[role="link"],[role="checkbox"],[role="textbox"],[role="combobox"],[role="menuitem"],[tabindex]');
      if (interactive && rect.width > 0 && rect.height > 0 && !element.disabled) {
        const index = nodes.length;
        nodes.push(element);
        const item = { index, tag: element.tagName.toLowerCase(), text: childText(element, 2) };
        for (const name of ['aria-label','href','name','placeholder','role','type','value']) {
          const value = name === 'value' && 'value' in element ? element.value : element.getAttribute(name);
          if (value) item[name.replace('-', '_')] = truncate(String(value), 500);
        }
        elements.push(item);
      }
      if (element.shadowRoot) walk(element.shadowRoot);
      if (element.tagName === 'IFRAME') {
        try { if (element.contentDocument) walk(element.contentDocument); } catch {}
      }
      walk(element);
    }
  }
  walk(document);
  const root = document.documentElement;
  const width = Math.max(root.scrollWidth, innerWidth);
  const height = Math.max(root.scrollHeight, innerHeight);
  return {
    url: location.href, title: document.title, interactive_elements: elements,
    pixels_above: Math.round(scrollY), pixels_below: Math.max(0, Math.round(height - innerHeight - scrollY)),
    viewport: {width: innerWidth, height: innerHeight, scroll_x: scrollX, scroll_y: scrollY, page_width: width, page_height: height}
  };
})()
