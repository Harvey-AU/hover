/**
 * components/hover-tabs.js — filter tab bar
 *
 * A row of labelled tab buttons. Only one tab is active at a time.
 * Emits a `hover-tabs:change` CustomEvent when the active tab changes.
 *
 * Usage (declarative):
 *   <hover-tabs active="all"></hover-tabs>
 *   <!-- then set tabs property imperatively, or use data-tabs attribute -->
 *
 * Usage (imperative):
 *   import { createTabs } from "/app/components/hover-tabs.js";
 *   const tabs = createTabs([
 *     { key: "",          label: "All" },
 *     { key: "pending",   label: "Pending" },
 *     { key: "running",   label: "Running" },
 *     { key: "completed", label: "Completed" },
 *     { key: "failed",    label: "Failed" },
 *   ], { active: "" });
 *   container.appendChild(tabs);
 *
 * Attributes:
 *   active — key of the currently active tab (default: first tab's key)
 *
 * Properties:
 *   tabs   — Array<{ key: string, label: string }>  (triggers re-render on set)
 *
 * Events:
 *   hover-tabs:change — detail: { key: string, label: string }
 */

// ── Imperative helper ──────────────────────────────────────────────────────────

/**
 * Create a hover-tabs element imperatively.
 * @param {Array<{ key: string, label: string }>} tabs
 * @param {{ active?: string }} [options]
 * @returns {HoverTabs}
 */
export function createTabs(tabs, options = {}) {
  const el = /** @type {HoverTabs} */ (document.createElement("hover-tabs"));
  if (options.active !== undefined) el.setAttribute("active", options.active);
  // Set tabs after element creation so _render fires with data
  el.tabs = tabs;
  return el;
}

// ── Web Component ──────────────────────────────────────────────────────────────

class HoverTabs extends HTMLElement {
  static get observedAttributes() {
    return ["active"];
  }

  constructor() {
    super();
    /** @type {Array<{ key: string, label: string }>} */
    this._tabs = [];
  }

  // ── Public API ──────────────────────────────────────────────────────────────

  /** @param {Array<{ key: string, label: string }>} value */
  set tabs(value) {
    this._tabs = Array.isArray(value) ? value : [];
    this._render();
  }

  get tabs() {
    return this._tabs;
  }

  /** @returns {string} */
  get active() {
    return this.getAttribute("active") ?? this._tabs[0]?.key ?? "";
  }

  /** @param {string} key */
  set active(key) {
    this.setAttribute("active", key);
  }

  // ── Lifecycle ───────────────────────────────────────────────────────────────

  connectedCallback() {
    this._render();
    this.addEventListener("click", this._handleClick);
  }

  disconnectedCallback() {
    this.removeEventListener("click", this._handleClick);
  }

  attributeChangedCallback(name) {
    if (name === "active" && this._tabs.length) this._render();
  }

  // ── Private ─────────────────────────────────────────────────────────────────

  _handleClick = (e) => {
    const btn = e.target.closest("[data-key]");
    if (!btn || btn.getAttribute("aria-disabled") === "true") return;
    const key = btn.dataset.key;
    if (key === this.active) return;
    this.setAttribute("active", key);
    const tab = this._tabs.find((t) => t.key === key);
    this.dispatchEvent(
      new CustomEvent("hover-tabs:change", {
        bubbles: true,
        detail: { key, label: tab?.label ?? key },
      })
    );
  };

  _render() {
    if (!this._tabs.length) return;
    const active = this.active;

    this.className = "hover-tabs";
    this.setAttribute("role", "tablist");

    // Diff: only rebuild if tab count or active key changed
    const existing = this.querySelectorAll("[data-key]");
    if (existing.length === this._tabs.length) {
      // Fast path — just update active state
      existing.forEach((btn) => {
        const isActive = btn.dataset.key === active;
        btn.classList.toggle("hover-tabs__tab--active", isActive);
        btn.setAttribute("aria-selected", String(isActive));
        btn.setAttribute("tabindex", isActive ? "0" : "-1");
      });
      return;
    }

    // Full rebuild
    this.innerHTML = "";
    this._tabs.forEach((tab) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className =
        "hover-tabs__tab" +
        (tab.key === active ? " hover-tabs__tab--active" : "");
      btn.dataset.key = tab.key;
      btn.textContent = tab.label;
      btn.setAttribute("role", "tab");
      btn.setAttribute("aria-selected", String(tab.key === active));
      btn.setAttribute("tabindex", tab.key === active ? "0" : "-1");
      this.appendChild(btn);
    });
  }
}

customElements.define("hover-tabs", HoverTabs);

export default { createTabs };
