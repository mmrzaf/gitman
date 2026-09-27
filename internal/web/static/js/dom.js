// Helpers the page's modules share.

// on calls handler(event, element) for every event of type on the page
// that starts inside an element matching selector.
export function on(type, selector, handler, options) {
  document.addEventListener(type, (event) => {
    const target = event.target instanceof Element ? event.target.closest(selector) : null;
    if (target) handler(event, target);
  }, options);
}

// el builds an element: el("button", { class: "btn", type: "button" }, "Copy").
// Attributes are set as attributes; children are nodes or text.
export function el(tag, attributes = {}, ...children) {
  const node = document.createElement(tag);
  for (const [name, value] of Object.entries(attributes)) {
    if (value !== false && value != null) node.setAttribute(name, value === true ? "" : value);
  }
  node.append(...children);
  return node;
}

// icon is an icon element from gitman.css.
export function icon(name) {
  return el("span", { class: `icon icon-${name}`, "aria-hidden": "true" });
}

const focusableSelector = [
  "a[href]", "button:not([disabled])", "input:not([disabled]):not([type=hidden])",
  "select:not([disabled])", "textarea:not([disabled])", "summary", "[tabindex]:not([tabindex='-1'])",
].join(",");

// focusable lists the elements in root that the keyboard can reach, in
// document order.
export function focusable(root) {
  return [...root.querySelectorAll(focusableSelector)].filter((node) => node.getClientRects().length > 0);
}

let announcer = null;

// announce says text to screen readers, politely, without showing it.
export function announce(text) {
  if (!announcer) {
    announcer = el("p", { class: "visually-hidden", "aria-live": "polite" });
    document.body.append(announcer);
  }
  // Emptied first, so the same words said twice are said twice.
  announcer.textContent = "";
  setTimeout(() => { announcer.textContent = text; }, 50);
}
