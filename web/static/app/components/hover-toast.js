/**
 * components/hover-toast.js — feedback toast notification
 *
 * Usage (imperative, from any ES module):
 *   import { showToast } from "/app/components/hover-toast.js";
 *
 *   showToast("Job started");
 *   showToast("Something went wrong", { variant: "error" });
 *   showToast("Check this out", { variant: "warning", duration: 6000 });
 *
 * Usage (declarative, from HTML — requires the custom element to be defined):
 *   <hover-toast message="Hello" variant="success"></hover-toast>
 *
 * Variants: "success" | "error" | "warning" | "info"
 * Default duration: 4000 ms (pass 0 for persistent until dismissed)
 *
 * The component manages its own container singleton so toasts stack
 * vertically without any page-level scaffolding required.
 */

const VARIANTS = new Set(["success", "error", "warning", "info"]);
const DEFAULT_DURATION_MS = 4000;
const ANIMATION_DURATION_MS = 250;

// ── Container singleton ────────────────────────────────────────────────────────

let container = null;

function getContainer() {
  if (container && document.body.contains(container)) {
    return container;
  }
  container = document.createElement("div");
  container.className = "hover-toast-container";
  container.setAttribute("aria-live", "polite");
  container.setAttribute("aria-atomic", "false");
  container.setAttribute("role", "status");
  document.body.appendChild(container);
  return container;
}

// ── Imperative API ─────────────────────────────────────────────────────────────

/**
 * Show a toast notification.
 *
 * @param {string} message
 * @param {{ variant?: "success"|"error"|"warning"|"info", duration?: number }} [options]
 * @returns {HoverToast} the created element (can be dismissed early via el.dismiss())
 */
export function showToast(message, options = {}) {
  const el = /** @type {HoverToast} */ (document.createElement("hover-toast"));
  el.setAttribute("message", message);
  el.setAttribute(
    "variant",
    VARIANTS.has(options.variant) ? options.variant : "info"
  );
  if (options.duration !== undefined) {
    el.setAttribute("duration", String(options.duration));
  }
  getContainer().appendChild(el);
  return el;
}

// ── Web Component ──────────────────────────────────────────────────────────────

class HoverToast extends HTMLElement {
  static get observedAttributes() {
    return ["message", "variant"];
  }

  connectedCallback() {
    this._render();
    this._scheduleAutoDismiss();
  }

  attributeChangedCallback() {
    if (this._rendered) {
      this._updateContent();
    }
  }

  /** Programmatically dismiss this toast. */
  dismiss() {
    this._dismiss();
  }

  // ── Private ────────────────────────────────────────────────────────────────

  _render() {
    this._rendered = true;
    const variant = this._variant();
    const message = this.getAttribute("message") || "";

    this.classList.add("hover-toast", `hover-toast--${variant}`);
    this.setAttribute("role", "alert");

    const icon = document.createElement("span");
    icon.className = "hover-toast__icon";
    icon.setAttribute("aria-hidden", "true");
    icon.textContent = this._icon(variant);

    const text = document.createElement("span");
    text.className = "hover-toast__message";
    text.textContent = message;

    const close = document.createElement("button");
    close.className = "hover-toast__close";
    close.setAttribute("aria-label", "Dismiss notification");
    close.setAttribute("type", "button");
    close.textContent = "×";
    close.addEventListener("click", () => this._dismiss());

    this.appendChild(icon);
    this.appendChild(text);
    this.appendChild(close);

    // Trigger enter animation on next frame
    requestAnimationFrame(() => {
      this.classList.add("hover-toast--visible");
    });
  }

  _updateContent() {
    const msg = this.querySelector(".hover-toast__message");
    if (msg) msg.textContent = this.getAttribute("message") || "";
  }

  _variant() {
    const v = this.getAttribute("variant");
    return VARIANTS.has(v) ? v : "info";
  }

  _icon(variant) {
    const icons = {
      success: "✓",
      error: "✕",
      warning: "⚠",
      info: "ℹ",
    };
    return icons[variant] ?? "ℹ";
  }

  _scheduleAutoDismiss() {
    const durationAttr = this.getAttribute("duration");
    const duration =
      durationAttr !== null ? parseInt(durationAttr, 10) : DEFAULT_DURATION_MS;

    if (duration > 0) {
      this._dismissTimer = setTimeout(() => this._dismiss(), duration);
    }
  }

  _dismiss() {
    if (this._dismissing) return;
    this._dismissing = true;

    if (this._dismissTimer) {
      clearTimeout(this._dismissTimer);
      this._dismissTimer = null;
    }

    this.classList.remove("hover-toast--visible");
    this.classList.add("hover-toast--leaving");

    setTimeout(() => {
      if (this.parentNode) {
        this.parentNode.removeChild(this);
      }
      // Clean up empty container
      if (container && container.childElementCount === 0) {
        container.remove();
        container = null;
      }
    }, ANIMATION_DURATION_MS);
  }
}

customElements.define("hover-toast", HoverToast);

export default { showToast };
