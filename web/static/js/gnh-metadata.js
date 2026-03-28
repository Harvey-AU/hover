/**
 * Hover - Metrics Metadata System
 *
 * Provides metadata (descriptions, help text, links) for all dashboard metrics.
 * Fetches from /v1/metadata/metrics and caches locally for performance.
 */

class MetricsMetadata {
  constructor() {
    this.metadata = null;
    this.loading = false;
    this.loadPromise = null;
    this.activeTooltip = null;
    this.activeTrigger = null;
    this.activeIcon = null;
    this._outsideClickHandler = null;
    this._hoverCleanup = null;
    this._hoverTimeout = null;
  }

  /**
   * Load metadata from the API (cached)
   */
  async load() {
    // Return cached data if already loaded
    if (this.metadata) {
      return this.metadata;
    }

    // Return existing promise if already loading
    if (this.loadPromise) {
      return this.loadPromise;
    }

    // Start loading
    this.loading = true;
    this.loadPromise = this._fetchMetadata();

    try {
      this.metadata = await this.loadPromise;
      return this.metadata;
    } finally {
      this.loading = false;
      this.loadPromise = null;
    }
  }

  /**
   * Fetch metadata from API
   */
  async _fetchMetadata() {
    try {
      if (
        window.dataBinder &&
        typeof window.dataBinder.fetchData === "function"
      ) {
        const response = await window.dataBinder.fetchData(
          "/v1/metadata/metrics"
        );
        if (response) {
          return response;
        }
        console.warn("Metadata response is empty:", response);
        return {};
      }

      const headers = await this._buildAuthHeaders();
      const response = await fetch("/v1/metadata/metrics", { headers });

      if (!response.ok) {
        console.warn(
          "Metadata request failed",
          response.status,
          response.statusText
        );
        return {};
      }

      const payload = await response.json();
      if (payload?.data) {
        return payload.data;
      }
      return payload || {};
    } catch (error) {
      console.error("Failed to load metrics metadata:", error);
      return {};
    }
  }

  async _buildAuthHeaders() {
    if (window.supabase?.auth) {
      try {
        const { data, error } = await window.supabase.auth.getSession();
        if (!error && data?.session?.access_token) {
          return { Authorization: `Bearer ${data.session.access_token}` };
        }
      } catch (authError) {
        console.warn(
          "Unable to resolve Supabase session for metadata",
          authError
        );
      }
    }

    return {};
  }

  /**
   * Get info HTML for a specific metric
   */
  getInfo(metricKey) {
    if (!this.metadata || !this.metadata[metricKey]) {
      return null;
    }
    return this.metadata[metricKey].info_html;
  }

  /**
   * Get full metadata for a specific metric
   */
  getMetric(metricKey) {
    if (!this.metadata) {
      return null;
    }
    return this.metadata[metricKey];
  }

  /**
   * Check if metadata is loaded
   */
  isLoaded() {
    return this.metadata !== null;
  }

  /**
   * Initialize info icons on the page
   * Scans for elements with gnh-help or data-gnh-info attributes and adds info icons with tooltips
   */
  initializeInfoIcons() {
    if (!this.isLoaded()) {
      console.warn("Metadata not loaded yet. Call load() first.");
      return;
    }

    // Find all elements with info attributes (both old and new formats)
    const elements = document.querySelectorAll("[data-gnh-info], [gnh-help]");

    elements.forEach((element) => {
      // Support both old (data-gnh-info) and new (gnh-help) formats
      const metricKey =
        element.getAttribute("gnh-help") ||
        element.getAttribute("data-gnh-info");
      const info = this.getInfo(metricKey);

      if (!info) {
        console.warn(`No metadata found for metric: ${metricKey}`);
        return;
      }

      // Check if info icon already exists
      if (element.querySelector(".gnh-info-icon")) {
        return;
      }

      // Create info icon
      const infoIcon = document.createElement("span");
      infoIcon.className = "gnh-info-icon";
      infoIcon.innerHTML = `
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" aria-hidden="true" focusable="false">
          <path d="M16 3C8.832 3 3 8.832 3 16s5.832 13 13 13 13-5.832 13-13S23.168 3 16 3zm0 2c6.086 0 11 4.914 11 11s-4.914 11-11 11S5 22.086 5 16 9.914 5 16 5zm-1 5v2h2v-2zm0 4v8h2v-8z" />
        </svg>
      `.trim();
      infoIcon.setAttribute("data-gnh-tooltip", info);
      infoIcon.setAttribute("aria-label", "More information");

      // Show rich tooltip on hover
      infoIcon.addEventListener("mouseenter", () => {
        this._showTooltip(infoIcon, { trigger: "hover" });
      });

      // Add click handler for mobile
      infoIcon.addEventListener("click", (e) => {
        e.stopPropagation();
        this._showTooltip(infoIcon);
      });

      // Append to element
      element.appendChild(infoIcon);
    });
  }

  /**
   * Remove the currently visible tooltip (if any)
   */
  _removeTooltip() {
    if (this._hoverCleanup) {
      this._hoverCleanup();
      this._hoverCleanup = null;
    }

    if (this._hoverTimeout) {
      clearTimeout(this._hoverTimeout);
      this._hoverTimeout = null;
    }

    if (this.activeTooltip && this.activeTooltip.parentElement) {
      this.activeTooltip.remove();
    }

    if (this._outsideClickHandler) {
      document.removeEventListener("click", this._outsideClickHandler);
      this._outsideClickHandler = null;
    }

    this.activeTooltip = null;
    this.activeTrigger = null;
    this.activeIcon = null;
  }

  /**
   * Show tooltip (hover and click interaction)
   * @param {HTMLElement} iconElement
   * @param {Object} options
   */
  _showTooltip(iconElement, options = {}) {
    const trigger = options.trigger || "click";

    // Remove any existing tooltip first
    this._removeTooltip();

    const tooltipContent = iconElement.getAttribute("data-gnh-tooltip");
    if (!tooltipContent) return;

    // Create tooltip popup
    const tooltip = document.createElement("div");
    tooltip.className = "gnh-tooltip-popup";
    tooltip.dataset.trigger = trigger;
    tooltip.innerHTML = tooltipContent;

    // Add close button
    const closeBtn = document.createElement("button");
    closeBtn.className = "gnh-tooltip-close";
    closeBtn.innerHTML = "×";
    closeBtn.setAttribute("aria-label", "Close");
    closeBtn.addEventListener("click", () => this._removeTooltip());
    tooltip.appendChild(closeBtn);

    // Position tooltip
    document.body.appendChild(tooltip);

    const iconRect = iconElement.getBoundingClientRect();
    const tooltipRect = tooltip.getBoundingClientRect();

    // Position below icon by default
    let top = iconRect.bottom + 8;
    let left = iconRect.left - tooltipRect.width / 2 + iconRect.width / 2;

    // Adjust if off-screen
    if (left + tooltipRect.width > window.innerWidth - 16) {
      left = window.innerWidth - tooltipRect.width - 16;
    }
    if (left < 16) {
      left = 16;
    }
    if (top + tooltipRect.height > window.innerHeight - 16) {
      // Position above instead
      top = iconRect.top - tooltipRect.height - 8;
    }

    tooltip.style.top = `${top}px`;
    tooltip.style.left = `${left}px`;

    // Track active tooltip state
    this.activeTooltip = tooltip;
    this.activeTrigger = trigger;
    this.activeIcon = iconElement;

    // Close on click outside
    this._outsideClickHandler = (e) => {
      if (!this.activeTooltip) return;
      if (
        !this.activeTooltip.contains(e.target) &&
        e.target !== this.activeIcon
      ) {
        this._removeTooltip();
      }
    };
    setTimeout(() => {
      document.addEventListener("click", this._outsideClickHandler);
    }, 0);

    // Additional handling for hover-triggered tooltips
    if (trigger === "hover") {
      const cancelRemoval = () => {
        if (this._hoverTimeout) {
          clearTimeout(this._hoverTimeout);
          this._hoverTimeout = null;
        }
      };

      const scheduleRemoval = () => {
        cancelRemoval();
        this._hoverTimeout = setTimeout(() => {
          const iconHovered = this.activeIcon?.matches(":hover");
          const tooltipHovered = this.activeTooltip?.matches(":hover");
          if (!iconHovered && !tooltipHovered) {
            this._removeTooltip();
          }
        }, 150);
      };

      iconElement.addEventListener("mouseleave", scheduleRemoval);
      tooltip.addEventListener("mouseleave", scheduleRemoval);
      iconElement.addEventListener("mouseenter", cancelRemoval);
      tooltip.addEventListener("mouseenter", cancelRemoval);

      // Store cleanup so we can remove listeners when tooltip closes
      this._hoverCleanup = () => {
        iconElement.removeEventListener("mouseleave", scheduleRemoval);
        tooltip.removeEventListener("mouseleave", scheduleRemoval);
        iconElement.removeEventListener("mouseenter", cancelRemoval);
        tooltip.removeEventListener("mouseenter", cancelRemoval);
        cancelRemoval();
      };
    }
  }

  /**
   * Refresh info icons (useful after dynamic content updates)
   */
  refresh() {
    if (this.isLoaded()) {
      this.initializeInfoIcons();
    }
  }
}

// Create global instance
window.metricsMetadata = new MetricsMetadata();

// Metadata will be loaded by dashboard after authentication
// No auto-initialization - auth is required for /v1/metadata/metrics endpoint
