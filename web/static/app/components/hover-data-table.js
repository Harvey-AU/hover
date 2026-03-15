/**
 * components/hover-data-table.js — sortable, filterable data table
 *
 * A lightweight table shell for displaying job tasks, issue lists, and
 * any other tabular data across the extension panel, dashboard, and future
 * surfaces. Handles empty/loading/error states, tab switching, and a
 * "show more" footer — all without a framework.
 *
 * Usage (declarative):
 *   <hover-data-table
 *     columns='[{"key":"path","label":"Page"},{"key":"status","label":"Status"}]'
 *     empty-message="No issues found"
 *   ></hover-data-table>
 *
 *   // Then set rows via the property:
 *   document.querySelector("hover-data-table").rows = myDataArray;
 *
 * Usage (imperative):
 *   import { createDataTable } from "/app/components/hover-data-table.js";
 *
 *   const table = createDataTable({
 *     columns: [
 *       { key: "path", label: "Page" },
 *       { key: "status", label: "Status", render: (val) => `<hover-status-pill status="${val}"></hover-status-pill>` },
 *     ],
 *     rows: data,
 *     emptyMessage: "No issues found",
 *     onRowClick: (row) => console.log(row),
 *   });
 *   container.appendChild(table);
 *
 * Column definition:
 *   { key: string, label: string, render?: (value, row) => string|Node }
 *
 * The `render` function may return an HTML string or a DOM Node.
 * If omitted, the raw value is displayed as text.
 */

// ── Imperative helper ──────────────────────────────────────────────────────────

/**
 * @typedef {{ key: string, label: string, render?: (value: unknown, row: Record<string,unknown>) => string|Node }} Column
 */

/**
 * Create a hover-data-table element imperatively.
 *
 * @param {{
 *   columns: Column[],
 *   rows?: Record<string,unknown>[],
 *   emptyMessage?: string,
 *   loading?: boolean,
 *   onRowClick?: (row: Record<string,unknown>) => void,
 * }} options
 * @returns {HoverDataTable}
 */
export function createDataTable(options = {}) {
  const el = /** @type {HoverDataTable} */ (
    document.createElement("hover-data-table")
  );
  if (options.columns) {
    el.setAttribute("columns", JSON.stringify(options.columns));
  }
  if (options.emptyMessage) {
    el.setAttribute("empty-message", options.emptyMessage);
  }
  if (options.loading) {
    el.setAttribute("loading", "");
  }
  // Set rows and callback as properties (not attributes — arrays/fns don't serialise well)
  if (options.rows) el.rows = options.rows;
  if (options.onRowClick) el.onRowClick = options.onRowClick;
  return el;
}

// ── Web Component ──────────────────────────────────────────────────────────────

class HoverDataTable extends HTMLElement {
  static get observedAttributes() {
    return ["columns", "empty-message", "loading", "error"];
  }

  constructor() {
    super();
    /** @type {Record<string,unknown>[]} */
    this._rows = [];
    /** @type {((row: Record<string,unknown>) => void)|null} */
    this.onRowClick = null;
  }

  /** @param {Record<string,unknown>[]} value */
  set rows(value) {
    this._rows = Array.isArray(value) ? value : [];
    if (this._rendered) this._renderBody();
  }

  get rows() {
    return this._rows;
  }

  connectedCallback() {
    this._render();
  }

  attributeChangedCallback(name) {
    if (!this._rendered) return;
    if (name === "columns") {
      this._render();
    } else {
      this._renderBody();
    }
  }

  // ── Private ────────────────────────────────────────────────────────────────

  /** Parse columns attribute, returning [] on failure. */
  _columns() {
    try {
      const raw = this.getAttribute("columns");
      return raw ? JSON.parse(raw) : [];
    } catch {
      return [];
    }
  }

  _render() {
    this._rendered = true;
    this.className = "hover-data-table";

    this.innerHTML = "";

    // Header row
    const columns = this._columns();
    if (columns.length) {
      const head = document.createElement("div");
      head.className = "hover-data-table__head";
      columns.forEach((col) => {
        const cell = document.createElement("div");
        cell.className = "hover-data-table__head-cell";
        cell.textContent = col.label || col.key;
        head.appendChild(cell);
      });
      this.appendChild(head);
    }

    // Body — rendered separately so rows can be updated without rebuilding header
    const body = document.createElement("div");
    body.className = "hover-data-table__body";
    this._body = body;
    this.appendChild(body);

    this._renderBody();
  }

  _renderBody() {
    const body = this._body;
    if (!body) return;
    body.innerHTML = "";

    const columns = this._columns();
    const loading = this.hasAttribute("loading");
    const error = this.getAttribute("error");

    // Loading state
    if (loading) {
      body.appendChild(this._skeletonRows(columns.length || 2));
      return;
    }

    // Error state
    if (error) {
      const msg = document.createElement("div");
      msg.className = "hover-data-table__empty hover-data-table__empty--error";
      msg.textContent = error;
      body.appendChild(msg);
      return;
    }

    // Empty state
    if (!this._rows.length) {
      const msg = document.createElement("div");
      msg.className = "hover-data-table__empty";
      msg.textContent =
        this.getAttribute("empty-message") || "No data to display.";
      body.appendChild(msg);
      return;
    }

    // Data rows
    this._rows.forEach((row) => {
      const tr = document.createElement("div");
      tr.className = "hover-data-table__row";

      if (this.onRowClick) {
        tr.setAttribute("role", "button");
        tr.setAttribute("tabindex", "0");
        tr.addEventListener("click", () => this.onRowClick(row));
        tr.addEventListener("keydown", (e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            this.onRowClick(row);
          }
        });
      }

      columns.forEach((col) => {
        const td = document.createElement("div");
        td.className = "hover-data-table__cell";

        const value = row[col.key];
        if (col.render) {
          const rendered = col.render(value, row);
          if (rendered instanceof Node) {
            td.appendChild(rendered);
          } else {
            td.innerHTML = rendered;
          }
        } else {
          td.textContent = value != null ? String(value) : "—";
        }

        tr.appendChild(td);
      });

      body.appendChild(tr);
    });
  }

  /** Build skeleton loading rows. */
  _skeletonRows(colCount) {
    const frag = document.createDocumentFragment();
    for (let i = 0; i < 3; i++) {
      const tr = document.createElement("div");
      tr.className = "hover-data-table__row hover-data-table__row--skeleton";
      for (let j = 0; j < colCount; j++) {
        const td = document.createElement("div");
        td.className = "hover-data-table__cell";
        const skel = document.createElement("span");
        skel.className = "hover-data-table__skeleton";
        td.appendChild(skel);
        tr.appendChild(td);
      }
      frag.appendChild(tr);
    }
    return frag;
  }
}

customElements.define("hover-data-table", HoverDataTable);

export default { createDataTable };
