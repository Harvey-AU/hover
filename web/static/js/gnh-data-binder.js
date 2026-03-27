/**
 * Hover Data Binding Library
 *
 * Provides template + data binding system for Hover dashboard pages.
 * Automatically finds and populates elements with data-gnh-bind attributes.
 */

class GNHDataBinder {
  constructor(options = {}) {
    this.apiBaseUrl = options.apiBaseUrl || "";
    this.authManager = null;
    this.debug = options.debug || false;

    // Store bound elements for efficient updates
    this.boundElements = new Map();
    this.templates = new Map();

    this.log("GNHDataBinder initialized", options);
  }

  /**
   * Initialize the data binder
   */
  async init() {
    this.log("Initializing data binder...");

    // Initialize authentication if available
    if (window.supabase) {
      await this.initAuth();
    }

    // Scan and bind all elements
    this.scanAndBind();

    this.log("Data binder initialized successfully");
  }

  /**
   * Initialize Supabase authentication
   */
  async initAuth() {
    try {
      const {
        data: { session },
      } = await window.supabase.auth.getSession();
      this.authManager = {
        session,
        isAuthenticated: !!session,
        user: session?.user || null,
      };

      // Listen for auth changes
      window.supabase.auth.onAuthStateChange((event, session) => {
        this.authManager.session = session;
        this.authManager.isAuthenticated = !!session;
        this.authManager.user = session?.user || null;

        // Re-scan conditional auth elements
        this.updateAuthElements();
      });

      this.log("Auth initialized", {
        authenticated: this.authManager.isAuthenticated,
        hasSession: !!session,
        hasAccessToken: !!session?.access_token,
        tokenPreview: session?.access_token?.substring(0, 20) + "...",
      });
    } catch (error) {
      this.log("Auth initialization failed", error);
    }
  }

  /**
   * Scan the DOM and bind all data binding attributes
   * Supports both old (data-gnh-*) and new (gnh-*) attribute formats
   */
  scanAndBind() {
    this.log("Scanning DOM for data binding attributes...");

    // Find all elements with data binding attributes (both old and new formats)
    const bindElements = document.querySelectorAll(
      "[data-gnh-bind], [gnh-text]"
    );
    const styleElements = document.querySelectorAll(
      "[data-gnh-bind-style], [gnh-style]"
    );
    const attrElements = document.querySelectorAll(
      "[data-gnh-bind-attr], [gnh-class], [gnh-href], [gnh-attr\\:]"
    );
    const templateElements = document.querySelectorAll(
      "[data-gnh-template], [gnh-template]"
    );
    const authElements = document.querySelectorAll(
      "[data-gnh-auth], [gnh-auth]"
    );
    const formElements = document.querySelectorAll(
      "[data-gnh-form], [gnh-form]"
    );
    const showElements = document.querySelectorAll(
      "[data-gnh-show-if], [gnh-show], [gnh-hide], [gnh-if]"
    );

    this.log("Found elements", {
      bind: bindElements.length,
      style: styleElements.length,
      attr: attrElements.length,
      template: templateElements.length,
      auth: authElements.length,
      forms: formElements.length,
      conditional: showElements.length,
    });

    // Process data binding elements
    bindElements.forEach((el) => this.registerBindElement(el));
    styleElements.forEach((el) => this.registerStyleElement(el));
    attrElements.forEach((el) => this.registerAttrElement(el));

    // Process template elements
    templateElements.forEach((el) => this.registerTemplate(el));

    // Process auth elements
    authElements.forEach((el) => this.updateAuthElement(el));

    // Process conditional visibility elements
    showElements.forEach((el) => this.updateConditionalElement(el));

    // Process form elements
    formElements.forEach((el) => this.registerForm(el));
  }

  /**
   * Register an element for data binding
   * Supports both data-gnh-bind and gnh-text
   */
  registerBindElement(element) {
    // Check for both old and new attribute formats
    const bindPath =
      element.getAttribute("gnh-text") || element.getAttribute("data-gnh-bind");
    if (!bindPath) return;

    if (!this.boundElements.has(bindPath)) {
      this.boundElements.set(bindPath, []);
    }

    this.boundElements.get(bindPath).push({
      element,
      type: "text",
      path: bindPath,
    });

    this.log("Registered bind element", { path: bindPath, element });
  }

  /**
   * Register an element for style binding
   * Supports both data-gnh-bind-style and gnh-style:prop
   */
  registerStyleElement(element) {
    // Check for old format: data-gnh-bind-style="width:{progress}%"
    const oldStyleBinding = element.getAttribute("data-gnh-bind-style");

    if (oldStyleBinding) {
      // Parse style binding format: "width:{progress}%"
      const match = oldStyleBinding.match(/^([^:]+):(.+)$/);
      if (match) {
        const [, property, template] = match;
        this._registerStyleBinding(element, property, template);
      }
    }

    // Check for new format: gnh-style:width="{progress}%"
    Array.from(element.attributes).forEach((attr) => {
      if (attr.name.startsWith("gnh-style:")) {
        const property = attr.name.substring("gnh-style:".length);
        const template = attr.value;
        this._registerStyleBinding(element, property, template);
      }
    });
  }

  /**
   * Internal helper to register a style binding
   */
  _registerStyleBinding(element, property, template) {
    const pathMatches = template.match(/\{([^}]+)\}/g);

    if (pathMatches) {
      pathMatches.forEach((pathMatch) => {
        const path = pathMatch.slice(1, -1); // Remove { }

        if (!this.boundElements.has(path)) {
          this.boundElements.set(path, []);
        }

        this.boundElements.get(path).push({
          element,
          type: "style",
          property,
          template,
          path,
        });
      });
    }

    this.log("Registered style element", { property, template, element });
  }

  /**
   * Register an element for attribute binding
   * Supports data-gnh-bind-attr, gnh-class, gnh-href, gnh-attr:name
   */
  registerAttrElement(element) {
    // Check for old format: data-gnh-bind-attr="class:gnh-status-{status}"
    const oldAttrBinding = element.getAttribute("data-gnh-bind-attr");

    if (oldAttrBinding) {
      // Parse attribute binding format: "class:gnh-status-{status}"
      const match = oldAttrBinding.match(/^([^:]+):(.+)$/);
      if (match) {
        const [, attribute, template] = match;
        this._registerAttrBinding(element, attribute, template);
      }
    }

    // Check for new shorthand formats: gnh-class, gnh-href, etc.
    const shorthandAttrs = [
      "class",
      "href",
      "src",
      "alt",
      "title",
      "placeholder",
      "value",
    ];
    shorthandAttrs.forEach((attrName) => {
      const attrValue = element.getAttribute(`gnh-${attrName}`);
      if (attrValue) {
        this._registerAttrBinding(element, attrName, attrValue);
      }
    });

    // Check for new explicit format: gnh-attr:data-id="{id}"
    Array.from(element.attributes).forEach((attr) => {
      if (attr.name.startsWith("gnh-attr:")) {
        const attribute = attr.name.substring("gnh-attr:".length);
        const template = attr.value;
        this._registerAttrBinding(element, attribute, template);
      }
    });
  }

  /**
   * Internal helper to register an attribute binding
   */
  _registerAttrBinding(element, attribute, template) {
    const pathMatches = template.match(/\{([^}]+)\}/g);

    if (pathMatches) {
      pathMatches.forEach((pathMatch) => {
        const path = pathMatch.slice(1, -1); // Remove { }

        if (!this.boundElements.has(path)) {
          this.boundElements.set(path, []);
        }

        this.boundElements.get(path).push({
          element,
          type: "attribute",
          attribute,
          template,
          path,
        });
      });
    }

    this.log("Registered attribute element", { attribute, template, element });
  }

  /**
   * Register a template element for repeated content
   * Supports both data-gnh-template and gnh-template
   */
  registerTemplate(element) {
    // Check for both old and new attribute formats
    const templateName =
      element.getAttribute("gnh-template") ||
      element.getAttribute("data-gnh-template");
    if (!templateName) return;

    // Store the template
    this.templates.set(templateName, {
      element,
      originalHTML: element.outerHTML,
      parent: element.parentElement,
    });

    // Hide the template element
    element.style.display = "none";

    this.log("Registered template", { name: templateName, element });
  }

  /**
   * Update authentication-conditional elements
   * Supports both data-gnh-auth and gnh-auth
   */
  updateAuthElements() {
    const authElements = document.querySelectorAll(
      "[data-gnh-auth], [gnh-auth]"
    );
    authElements.forEach((el) => this.updateAuthElement(el));
  }

  /**
   * Update a single auth element
   * Supports both data-gnh-auth and gnh-auth
   */
  updateAuthElement(element) {
    // Check for both old and new attribute formats
    const authCondition =
      element.getAttribute("gnh-auth") || element.getAttribute("data-gnh-auth");
    let shouldShow = false;

    switch (authCondition) {
      case "required":
        shouldShow = this.authManager?.isAuthenticated || false;
        break;
      case "guest":
        shouldShow = !this.authManager?.isAuthenticated;
        break;
      default:
        shouldShow = true;
    }

    element.style.display = shouldShow ? "" : "none";
  }

  /**
   * Update conditional visibility element
   * Supports data-gnh-show-if, gnh-show, gnh-hide, gnh-if
   */
  updateConditionalElement(element) {
    // Check for all conditional attribute formats
    const showIf =
      element.getAttribute("gnh-show") ||
      element.getAttribute("data-gnh-show-if");
    const hideIf = element.getAttribute("gnh-hide");
    const renderIf = element.getAttribute("gnh-if");

    // For now, just handle show/hide based on existence
    // Full implementation would evaluate conditions against data
    // This is a placeholder that maintains current functionality
    if (showIf || hideIf || renderIf) {
      this.log("Conditional element registered", {
        element,
        showIf,
        hideIf,
        renderIf,
      });
    }
  }

  /**
   * Register a form for handling
   * Supports both data-gnh-form and gnh-form
   */
  registerForm(form) {
    // Check for both old and new attribute formats
    const formAction =
      form.getAttribute("gnh-form") || form.getAttribute("data-gnh-form");
    if (!formAction) return;

    this.log("Registering form", { action: formAction, form });

    // Set up form submission handler
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      await this.handleFormSubmit(form, formAction);
    });

    // Set up real-time validation if configured
    const validateOnChange = form.getAttribute("data-gnh-validate") === "live";
    if (validateOnChange) {
      const inputs = form.querySelectorAll("input, select, textarea");
      inputs.forEach((input) => {
        input.addEventListener("input", () => this.validateFormField(input));
        input.addEventListener("blur", () => this.validateFormField(input));
      });
    }
  }

  /**
   * Handle form submission
   */
  async handleFormSubmit(form, action) {
    try {
      this.log("Form submission started", { action });

      // Set loading state
      this.setFormLoading(form, true);

      // Validate form
      const isValid = this.validateForm(form);
      if (!isValid) {
        this.setFormLoading(form, false);
        return;
      }

      // Collect form data
      const formData = this.collectFormData(form);

      // Determine API endpoint
      const endpoint = this.getFormEndpoint(action, formData);

      // Submit form
      const result = await this.submitForm(endpoint, formData, action);

      // Handle success
      this.handleFormSuccess(form, result, action);
    } catch (error) {
      this.log("Form submission failed", { action, error });
      this.handleFormError(form, error, action);
    } finally {
      this.setFormLoading(form, false);
    }
  }

  /**
   * Collect form data
   */
  collectFormData(form) {
    const formData = new FormData(form);
    const data = {};

    for (const [key, value] of formData.entries()) {
      // Handle multiple values for same key (checkboxes, etc.)
      if (Object.hasOwn(data, key)) {
        if (Array.isArray(data[key])) {
          data[key].push(value);
        } else {
          data[key] = [data[key], value];
        }
      } else {
        data[key] = value;
      }
    }

    return data;
  }

  /**
   * Get API endpoint for form action
   */
  getFormEndpoint(action, data) {
    switch (action) {
      case "create-job":
        return "/v1/jobs";
      case "update-profile":
        return "/v1/auth/profile";
      case "create-organisation":
        return "/v1/organisations";
      default:
        // Custom endpoint from data-gnh-endpoint attribute
        const form = document.querySelector(
          `[data-gnh-form="${action}"], [gnh-form="${action}"]`
        );
        return (
          form?.getAttribute("data-gnh-endpoint") ||
          form?.getAttribute("gnh-endpoint") ||
          `/v1/${action}`
        );
    }
  }

  /**
   * Submit form data to API
   */
  async submitForm(endpoint, data, action) {
    const method = this.getFormMethod(action);
    const headers = {
      "Content-Type": "application/json",
    };

    // Add auth header if available
    if (this.authManager?.session?.access_token) {
      headers["Authorization"] =
        `Bearer ${this.authManager.session.access_token}`;
    }

    const response = await fetch(`${this.apiBaseUrl}${endpoint}`, {
      method,
      headers,
      body: JSON.stringify(data),
    });

    if (!response.ok) {
      const errorData = await response.json().catch(() => ({}));
      throw new Error(
        errorData.message || `HTTP ${response.status}: ${response.statusText}`
      );
    }

    return await response.json();
  }

  /**
   * Get HTTP method for form action
   */
  getFormMethod(action) {
    switch (action) {
      case "create-job":
      case "create-organisation":
        return "POST";
      case "update-profile":
        return "PUT";
      case "delete-job":
        return "DELETE";
      default:
        return "POST";
    }
  }

  /**
   * Validate entire form
   */
  validateForm(form) {
    const inputs = form.querySelectorAll("input, select, textarea");
    let isValid = true;

    inputs.forEach((input) => {
      if (!this.validateFormField(input)) {
        isValid = false;
      }
    });

    return isValid;
  }

  /**
   * Validate a single form field
   */
  validateFormField(input) {
    const rules = this.getValidationRules(input);
    const value = input.value.trim();
    const errors = [];

    // Required validation
    if (rules.required && !value) {
      errors.push("This field is required");
    }

    // Type-specific validation
    if (value && rules.type) {
      switch (rules.type) {
        case "email":
          if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)) {
            errors.push("Please enter a valid email address");
          }
          break;
        case "url":
          try {
            new URL(value);
          } catch {
            errors.push("Please enter a valid URL");
          }
          break;
        case "number":
          if (isNaN(Number(value))) {
            errors.push("Please enter a valid number");
          }
          break;
      }
    }

    // Length validation
    if (value && rules.minLength && value.length < rules.minLength) {
      errors.push(`Must be at least ${rules.minLength} characters`);
    }

    if (value && rules.maxLength && value.length > rules.maxLength) {
      errors.push(`Must be no more than ${rules.maxLength} characters`);
    }

    // Custom pattern validation using native HTML pattern handling
    if (value && rules.pattern) {
      try {
        const patternProbe = document.createElement("input");
        patternProbe.type = "text";
        patternProbe.pattern = rules.pattern;
        patternProbe.value = value;

        if (!patternProbe.checkValidity()) {
          errors.push(rules.patternMessage || "Invalid format");
        }
      } catch (e) {
        console.warn("Invalid validation pattern:", rules.pattern, e);
        errors.push("Unable to validate format");
      }
    }

    // Update field UI
    this.updateFieldValidation(input, errors);

    return errors.length === 0;
  }

  /**
   * Get validation rules for an input
   */
  getValidationRules(input) {
    const rules = {
      required: input.hasAttribute("required"),
      type: input.getAttribute("data-gnh-validate-type") || input.type,
      minLength: parseInt(input.getAttribute("data-gnh-validate-min")) || null,
      maxLength: parseInt(input.getAttribute("data-gnh-validate-max")) || null,
      pattern: input.getAttribute("data-gnh-validate-pattern"),
      patternMessage: input.getAttribute("data-gnh-validate-message"),
    };

    return rules;
  }

  /**
   * Update field validation UI
   */
  updateFieldValidation(input, errors) {
    const isValid = errors.length === 0;

    // Remove existing validation classes and messages
    input.classList.remove("gnh-field-valid", "gnh-field-invalid");
    const existingError = input.parentElement.querySelector(".gnh-field-error");
    if (existingError) {
      existingError.remove();
    }

    // Add validation state
    if (input.value.trim()) {
      input.classList.add(isValid ? "gnh-field-valid" : "gnh-field-invalid");

      // Show error message
      if (!isValid) {
        const errorDiv = document.createElement("div");
        errorDiv.className = "gnh-field-error";
        errorDiv.textContent = errors[0]; // Show first error
        errorDiv.style.cssText =
          "color: #dc2626; font-size: 12px; margin-top: 4px;";
        input.parentElement.appendChild(errorDiv);
      }
    }
  }

  /**
   * Set form loading state
   */
  setFormLoading(form, loading) {
    const submitButton = form.querySelector(
      'button[type="submit"], input[type="submit"]'
    );
    const loadingElements = form.querySelectorAll("[data-gnh-loading]");

    if (submitButton) {
      submitButton.disabled = loading;
      if (loading) {
        submitButton.setAttribute(
          "data-original-text",
          submitButton.textContent
        );
        submitButton.textContent = "Loading...";
      } else {
        const originalText = submitButton.getAttribute("data-original-text");
        if (originalText) {
          submitButton.textContent = originalText;
          submitButton.removeAttribute("data-original-text");
        }
      }
    }

    loadingElements.forEach((el) => {
      el.style.display = loading ? "" : "none";
    });
  }

  /**
   * Handle form success
   */
  handleFormSuccess(form, result, action) {
    this.log("Form submission successful", { action, result });

    // Clear form if specified
    if (form.getAttribute("data-gnh-clear-on-success") === "true") {
      form.reset();
    }

    // Action-specific post-submit side-effects
    if (action === "create-organisation") {
      const newOrg = result?.data?.organisation;
      if (newOrg) {
        window.BB_ACTIVE_ORG = newOrg;
        if (Array.isArray(window.BB_ORGANISATIONS)) {
          window.BB_ORGANISATIONS.push(newOrg);
        } else {
          window.BB_ORGANISATIONS = [newOrg];
        }
        document.dispatchEvent(
          new CustomEvent("gnh:org-switched", {
            detail: { organisation: newOrg },
          })
        );
      }
    }

    // Show success message
    this.showFormMessage(
      form,
      "Success! Your request has been processed.",
      "success"
    );

    // Trigger custom success handler
    const successEvent = new CustomEvent("gnh-form-success", {
      detail: { action, result, form },
    });
    form.dispatchEvent(successEvent);

    // Redirect if specified
    const redirectUrl = form.getAttribute("data-gnh-redirect");
    if (redirectUrl) {
      setTimeout(() => {
        window.location.href = redirectUrl;
      }, 1000);
    }
  }

  /**
   * Handle form error
   */
  handleFormError(form, error, action) {
    this.log("Form submission error", { action, error });

    // Show error message
    this.showFormMessage(
      form,
      error.message || "An error occurred. Please try again.",
      "error"
    );

    // Trigger custom error handler
    const errorEvent = new CustomEvent("gnh-form-error", {
      detail: { action, error, form },
    });
    form.dispatchEvent(errorEvent);
  }

  /**
   * Show form message
   */
  showFormMessage(form, message, type) {
    // Remove existing messages
    const existingMessage = form.querySelector(".gnh-form-message");
    if (existingMessage) {
      existingMessage.remove();
    }

    // Create message element
    const messageDiv = document.createElement("div");
    messageDiv.className = `gnh-form-message gnh-form-message-${type}`;
    messageDiv.textContent = message;

    // Style message
    const styles = {
      padding: "12px 16px",
      borderRadius: "6px",
      marginBottom: "16px",
      fontSize: "14px",
      fontWeight: "500",
    };

    if (type === "success") {
      styles.background = "#dcfce7";
      styles.color = "#16a34a";
      styles.border = "1px solid #bbf7d0";
    } else {
      styles.background = "#fee2e2";
      styles.color = "#dc2626";
      styles.border = "1px solid #fecaca";
    }

    Object.assign(messageDiv.style, styles);

    // Insert at top of form
    form.insertBefore(messageDiv, form.firstChild);

    // Auto-remove after 5 seconds
    setTimeout(() => {
      if (messageDiv.parentElement) {
        messageDiv.remove();
      }
    }, 5000);
  }

  /**
   * Fetch data from API endpoint
   */
  async fetchData(endpoint, options = {}) {
    try {
      const headers = {
        "Content-Type": "application/json",
        ...options.headers,
      };

      // Add auth header if available
      if (this.authManager?.session?.access_token) {
        headers["Authorization"] =
          `Bearer ${this.authManager.session.access_token}`;
      }

      const fetchOptions = {
        method: options.method || "GET",
        headers,
      };

      // Add body if provided (for POST, PUT, etc.)
      if (options.body) {
        fetchOptions.body = options.body;
      }

      const response = await fetch(
        `${this.apiBaseUrl}${endpoint}`,
        fetchOptions
      );

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }

      const result = await response.json();
      return result.data || result;
    } catch (error) {
      this.log("API fetch failed", { endpoint, error });
      throw error;
    }
  }

  /**
   * Update bound elements with new data
   */
  updateElements(data) {
    this.log("Updating elements with data", data);

    // Update all bound elements
    for (const [path, elements] of this.boundElements) {
      const value = this.getValueByPath(data, path);

      elements.forEach((binding) => {
        switch (binding.type) {
          case "text":
            binding.element.textContent = value ?? binding.element.textContent;
            break;

          case "style":
            const styleValue = this.processTemplate(binding.template, data);
            if (styleValue !== null) {
              binding.element.style[binding.property] = styleValue;
            }
            break;

          case "attribute":
            const attrValue = this.processTemplate(binding.template, data);
            if (attrValue !== null) {
              binding.element.setAttribute(binding.attribute, attrValue);
            }
            break;
        }
      });
    }
  }

  /**
   * Render template with array data
   */
  renderTemplate(templateName, items) {
    const template = this.templates.get(templateName);
    if (!template) {
      this.log("Template not found", templateName);
      return;
    }

    this.log("Rendering template", {
      name: templateName,
      items: items?.length,
    });

    // Remove existing instances
    const existing = template.parent.querySelectorAll(
      `[data-gnh-template-instance="${templateName}"]`
    );
    existing.forEach((el) => el.remove());

    // Create new instances
    if (Array.isArray(items) && items.length > 0) {
      items.forEach((item, index) => {
        const instance = this.createTemplateInstance(template, item, index);
        if (instance) {
          template.parent.appendChild(instance);
        }
      });
    }
  }

  /**
   * Create a template instance with data
   */
  createTemplateInstance(template, data, index) {
    const tempDiv = document.createElement("div");
    tempDiv.innerHTML = template.originalHTML;
    const instance = tempDiv.firstElementChild;

    if (!instance) return null;

    // Get template name from either old or new attribute
    const templateName =
      template.element.getAttribute("gnh-template") ||
      template.element.getAttribute("data-gnh-template");

    // Mark as template instance
    instance.setAttribute("data-gnh-template-instance", templateName);
    instance.removeAttribute("data-gnh-template");
    instance.removeAttribute("gnh-template");
    instance.style.display = "";

    // Bind data to instance elements (support both old and new attributes)
    const bindElements = instance.querySelectorAll(
      "[data-gnh-bind], [gnh-text]"
    );
    bindElements.forEach((el) => {
      const path =
        el.getAttribute("gnh-text") || el.getAttribute("data-gnh-bind");
      const value = this.getValueByPath(data, path);
      if (value !== undefined) {
        el.textContent = value;
      }
    });

    // Handle style bindings (old format: data-gnh-bind-style)
    const oldStyleElements = instance.querySelectorAll("[data-gnh-bind-style]");
    oldStyleElements.forEach((el) => {
      const styleBinding = el.getAttribute("data-gnh-bind-style");
      const match = styleBinding.match(/^([^:]+):(.+)$/);
      if (match) {
        const [, property, template] = match;
        const value = this.processTemplate(template, data);
        if (value !== null) {
          el.style[property] = value;
        }
      }
    });

    // Handle style bindings (new format: gnh-style:property)
    const styleAttrs = instance.attributes;
    for (let i = 0; i < styleAttrs.length; i++) {
      const attr = styleAttrs[i];
      if (attr.name.startsWith("gnh-style:")) {
        const property = attr.name.substring(10); // Remove 'gnh-style:' prefix
        const value = this.processTemplate(attr.value, data);
        if (value !== null) {
          instance.style[property] = value;
        }
      }
    }

    // Handle class binding (new format: gnh-class)
    if (instance.hasAttribute("gnh-class")) {
      const classTemplate = instance.getAttribute("gnh-class");
      const classValue = this.processTemplate(classTemplate, data);
      if (classValue !== null) {
        instance.setAttribute("class", classValue);
      }
    }

    // Handle attribute bindings (old format: data-gnh-bind-attr)
    const oldAttrElements = instance.querySelectorAll("[data-gnh-bind-attr]");
    oldAttrElements.forEach((el) => {
      const attrBinding = el.getAttribute("data-gnh-bind-attr");
      const match = attrBinding.match(/^([^:]+):(.+)$/);
      if (match) {
        const [, attribute, template] = match;
        const value = this.processTemplate(template, data);
        if (value !== null) {
          el.setAttribute(attribute, value);
        }
      }
    });

    // Handle data binding attributes (gnh-href, gnh-src, etc.) - these set actual HTML attributes
    const bindingAttrs = ["href", "src", "alt", "title", "placeholder"];
    bindingAttrs.forEach((attrName) => {
      const bbbAttr = `gnh-${attrName}`;
      instance.querySelectorAll(`[${bbbAttr}]`).forEach((el) => {
        if (el.hasAttribute(bbbAttr)) {
          const template = el.getAttribute(bbbAttr);
          const value = this.processTemplate(template, data);
          if (value !== null) {
            // Set the actual HTML attribute
            el.setAttribute(attrName, value);
          }
        }
      });
    });

    // Handle data storage attributes (gnh-id, gnh-value) - these stay as gnh-* with interpolated values
    const storageAttrs = ["id", "value"];
    storageAttrs.forEach((attrName) => {
      const bbbAttr = `gnh-${attrName}`;
      instance.querySelectorAll(`[${bbbAttr}]`).forEach((el) => {
        if (el.hasAttribute(bbbAttr)) {
          const template = el.getAttribute(bbbAttr);
          const value = this.processTemplate(template, data);
          if (value !== null) {
            // Keep as gnh-* attribute with interpolated value for handlers to read
            el.setAttribute(bbbAttr, value);
          }
        }
      });
    });

    // Handle conditional visibility (gnh-show, gnh-hide, gnh-if, data-gnh-show-if)
    instance
      .querySelectorAll("[gnh-show], [gnh-hide], [gnh-if], [data-gnh-show-if]")
      .forEach((el) => {
        const showCondition =
          el.getAttribute("gnh-show") || el.getAttribute("data-gnh-show-if");
        const hideCondition = el.getAttribute("gnh-hide");
        const ifCondition = el.getAttribute("gnh-if");

        let shouldShow = true;

        if (showCondition) {
          shouldShow = this.evaluateCondition(showCondition, data);
        } else if (hideCondition) {
          shouldShow = !this.evaluateCondition(hideCondition, data);
        } else if (ifCondition) {
          shouldShow = this.evaluateCondition(ifCondition, data);
        }

        if (shouldShow) {
          el.style.display = "";
        } else {
          el.style.display = "none";
        }
      });

    return instance;
  }

  /**
   * Evaluate a conditional expression
   * Supports: field=value, field=value1,value2, field>value, field<value, field!=value
   */
  evaluateCondition(condition, data) {
    // Handle equality with multiple values: status=completed,failed,cancelled
    if (condition.includes("=") && !condition.includes("!=")) {
      const [path, values] = condition.split("=");
      const fieldValue = this.getValueByPath(data, path.trim());
      const allowedValues = values.split(",").map((v) => v.trim());
      return allowedValues.includes(String(fieldValue));
    }

    // Handle not equal: status!=pending
    if (condition.includes("!=")) {
      const [path, value] = condition.split("!=");
      const fieldValue = this.getValueByPath(data, path.trim());
      return String(fieldValue) !== value.trim();
    }

    // Handle greater than: count>0
    if (condition.includes(">")) {
      const [path, value] = condition.split(">");
      const fieldValue = this.getValueByPath(data, path.trim());
      return Number(fieldValue) > Number(value.trim());
    }

    // Handle less than: count<10
    if (condition.includes("<")) {
      const [path, value] = condition.split("<");
      const fieldValue = this.getValueByPath(data, path.trim());
      return Number(fieldValue) < Number(value.trim());
    }

    return false;
  }

  /**
   * Process template string with data
   */
  processTemplate(template, data) {
    return template.replace(/\{([^}]+)\}/g, (match, path) => {
      const value = this.getValueByPath(data, path);
      return value !== undefined ? value : match;
    });
  }

  /**
   * Get value from object by dot notation path
   */
  getValueByPath(obj, path) {
    return path.split(".").reduce((current, key) => {
      return current && current[key] !== undefined ? current[key] : undefined;
    }, obj);
  }

  /**
   * Refresh all bound data
   */
  async refresh() {
    // This method should be overridden by implementations
    // or called with specific data endpoints
    this.log("Refresh called - override this method in your implementation");
  }

  /**
   * Load and bind data from specific endpoints
   */
  async loadAndBind(endpoints) {
    try {
      const promises = Object.entries(endpoints).map(
        async ([key, endpoint]) => {
          const data = await this.fetchData(endpoint);
          return [key, data];
        }
      );

      const results = await Promise.all(promises);
      const combinedData = Object.fromEntries(results);

      this.updateElements(combinedData);

      return combinedData;
    } catch (error) {
      this.log("Load and bind failed", error);
      throw error;
    }
  }

  /**
   * Bind data to templates
   */
  bindTemplates(templateData) {
    Object.entries(templateData).forEach(([templateName, items]) => {
      this.renderTemplate(templateName, items);
    });
  }

  /**
   * Debug logging
   */
  log(message, data = null) {
    if (this.debug) {
    }
  }

  /**
   * Destroy the data binder
   */
  destroy() {
    this.boundElements.clear();
    this.templates.clear();
    this.log("Data binder destroyed");
  }
}

// Export for use as module or global
if (typeof module !== "undefined" && module.exports) {
  module.exports = GNHDataBinder;
} else {
  window.GNHDataBinder = GNHDataBinder;
}
