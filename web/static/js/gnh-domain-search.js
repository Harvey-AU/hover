(() => {
  const domains = [];
  let loadPromise = null;
  let domainsLoaded = false;

  const getDomains = () => domains.slice();

  const getAuthToken = async () => {
    if (!window.supabase?.auth?.getSession) {
      return "";
    }

    const session = await window.supabase.auth.getSession();
    return session?.data?.session?.access_token || "";
  };

  const upsertDomain = (domain) => {
    const index = domains.findIndex((item) => item.id === domain.id);
    if (index >= 0) {
      domains[index] = domain;
      return;
    }
    domains.push(domain);
  };

  const loadOrganisationDomains = async () => {
    if (!window.supabase?.auth) {
      domains.length = 0;
      domainsLoaded = false;
      return [];
    }

    try {
      const token = await getAuthToken();
      if (!token) {
        domains.length = 0;
        domainsLoaded = false;
        return [];
      }

      const domainsResponse = await fetch("/v1/integrations/google/domains", {
        headers: { Authorization: `Bearer ${token}` },
      });

      if (!domainsResponse.ok) {
        console.warn(
          "[Domains] Failed to fetch domains, continuing without them"
        );
        domains.length = 0;
        domainsLoaded = false;
        return [];
      }

      const domainsData = await domainsResponse.json();
      const domainList =
        domainsData?.data?.domains ?? domainsData?.domains ?? [];

      domains.length = 0;
      if (Array.isArray(domainList)) {
        domainList.forEach((domain) => {
          if (domain && Number.isFinite(domain.id) && domain.name) {
            domains.push({ id: domain.id, name: domain.name });
          }
        });
      }

      domainsLoaded = true;
      return domains;
    } catch (error) {
      console.error("Failed to fetch organisation domains:", error);
      domains.length = 0;
      domainsLoaded = false;
      return [];
    }
  };

  const ensureDomainsLoaded = async () => {
    if (domainsLoaded) {
      return domains;
    }
    if (loadPromise) {
      return loadPromise;
    }

    loadPromise = loadOrganisationDomains().finally(() => {
      loadPromise = null;
    });
    return loadPromise;
  };

  const findExactDomain = (query, domainList) => {
    const normalised = query.toLowerCase().trim();
    return (
      domainList.find((domain) => domain.name.toLowerCase() === normalised) ||
      null
    );
  };

  const createDomain = async (domainName) => {
    let normalised = (domainName || "").toString().trim();
    if (normalised === "") {
      throw new Error("Domain cannot be empty");
    }

    normalised = normalised.replace(/^https?:\/\//i, "");
    normalised = normalised.replace(/^www\./i, "");
    normalised = normalised.replace(/\/$/, "");
    normalised = normalised.toLowerCase();
    if (!normalised.includes(".") || !/^[a-z0-9.-]+$/.test(normalised)) {
      throw new Error("Domain must be a valid hostname");
    }

    const token = await getAuthToken();
    if (!token) {
      throw new Error("Please sign in to create domains");
    }

    const response = await fetch("/v1/domains", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ domain: normalised }),
    });

    if (!response.ok) {
      let errorMessage = "Failed to create domain";
      try {
        const errorData = await response.json();
        errorMessage = errorData.message || errorMessage;
      } catch (error) {
        console.error("Failed to parse domain error:", error);
      }
      throw new Error(errorMessage);
    }

    const result = await response.json();
    const rawDomainId = result?.data?.domain_id ?? result?.domain_id ?? null;
    if (rawDomainId === null || rawDomainId === undefined) {
      throw new Error("Invalid domain ID in response");
    }
    const newDomainId = Number(rawDomainId);
    const newDomainName = result?.data?.domain ?? result?.domain ?? normalised;

    if (!Number.isFinite(newDomainId) || newDomainId < 1) {
      throw new Error("Invalid domain ID in response");
    }

    const domain = { id: newDomainId, name: newDomainName };
    upsertDomain(domain);
    return domain;
  };

  const ensureDomainByName = async (domainName, options = {}) => {
    const { allowCreate = true } = options;
    const query = (domainName || "").toString().trim();
    if (!query) {
      return null;
    }

    if (!domainsLoaded) {
      await ensureDomainsLoaded();
    }

    const exactMatch = findExactDomain(query, domains);
    if (exactMatch) {
      return exactMatch;
    }

    if (!allowCreate) {
      return null;
    }

    return createDomain(query);
  };

  const createDropdownElement = () => {
    const dropdown = document.createElement("div");
    dropdown.style.cssText =
      "display: none; position: absolute; top: 100%; left: 0; right: 0; max-height: 200px; overflow-y: auto; background: white; border: 1px solid #d1d5db; border-radius: 6px; margin-top: 4px; z-index: 1000; box-shadow: 0 2px 8px rgba(0,0,0,0.1);";
    return dropdown;
  };

  const setupDomainSearchInput = (options = {}) => {
    const {
      input,
      dropdown,
      container,
      form,
      getExcludedDomainIds,
      onSelectDomain,
      onCreateDomain,
      createMode,
      searchMode,
      showCreateOption,
      allowCreate,
      clearOnSelect = false,
      autoCreateOnSubmit,
      createOptionText,
      onError,
    } = options;

    if (!input) {
      return;
    }

    if (input.dataset.domainSearchInitialised === "true") {
      return;
    }
    input.dataset.domainSearchInitialised = "true";

    const resolvedSearchMode =
      searchMode || input.getAttribute("bbb-domain-search") || "on";
    if (resolvedSearchMode === "off" || resolvedSearchMode === "disabled") {
      return;
    }

    const wrapper = container || input.parentElement;
    if (wrapper) {
      const position = window.getComputedStyle(wrapper).position;
      if (position === "static") {
        wrapper.style.position = "relative";
      }
    }

    const dropdownEl = dropdown || createDropdownElement();
    if (!dropdown && wrapper) {
      wrapper.appendChild(dropdownEl);
    }
    dropdownEl.setAttribute("role", "listbox");

    const reportError = (message) => {
      if (typeof onError === "function") {
        onError(message);
        return;
      }
      console.error(message);
    };

    const resolvedMode =
      createMode || input.getAttribute("bbb-domain-create") || "auto";
    const resolvedAllowCreate =
      allowCreate !== undefined ? allowCreate : resolvedMode !== "block";
    const resolvedShowCreateOption =
      showCreateOption !== undefined
        ? showCreateOption
        : resolvedMode === "option";
    const resolvedAutoCreateOnSubmit =
      autoCreateOnSubmit !== undefined
        ? autoCreateOnSubmit
        : resolvedMode === "auto";

    const handleSelect = async (domain) => {
      if (input.setCustomValidity) {
        input.setCustomValidity("");
      }
      if (typeof onSelectDomain === "function") {
        await onSelectDomain(domain);
      }
      if (clearOnSelect) {
        input.value = "";
      } else {
        input.value = domain.name;
      }
    };

    const handleCreate = async (query) => {
      try {
        const domain = await createDomain(query);
        if (!domain) {
          return;
        }
        if (input.setCustomValidity) {
          input.setCustomValidity("");
        }
        if (typeof onCreateDomain === "function") {
          await onCreateDomain(domain);
        } else {
          await handleSelect(domain);
        }
      } catch (error) {
        const message =
          error.message || "Failed to create domain. Please try again.";
        reportError(message);
        if (input.setCustomValidity) {
          input.setCustomValidity(message);
          input.reportValidity();
        }
      }
    };

    let documentListenerActive = false;
    const onDocumentClick = (event) => {
      if (!wrapper || !wrapper.contains(event.target)) {
        dropdownEl.style.display = "none";
        focusedIndex = -1;
        if (documentListenerActive) {
          document.removeEventListener("click", onDocumentClick);
          documentListenerActive = false;
        }
      }
    };

    const ensureDocumentListener = () => {
      if (documentListenerActive) {
        return;
      }
      documentListenerActive = true;
      document.addEventListener("click", onDocumentClick);
    };

    let renderToken = 0;
    let optionItems = [];
    let focusedIndex = -1;
    const updateFocusedIndex = (nextIndex) => {
      if (!optionItems.length) {
        focusedIndex = -1;
        return;
      }
      const clamped = Math.max(0, Math.min(nextIndex, optionItems.length - 1));
      focusedIndex = clamped;
      optionItems.forEach((item, index) => {
        item.el.setAttribute(
          "aria-selected",
          index === focusedIndex ? "true" : "false"
        );
      });
      optionItems[focusedIndex].el.focus();
    };

    const handleDropdownKey = async (event) => {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        if (optionItems.length === 0) {
          await renderDropdown(input.value);
        }
        updateFocusedIndex(
          focusedIndex < 0
            ? 0
            : Math.min(focusedIndex + 1, optionItems.length - 1)
        );
        return;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        if (optionItems.length === 0) {
          await renderDropdown(input.value);
        }
        updateFocusedIndex(
          focusedIndex < 0
            ? optionItems.length - 1
            : Math.max(focusedIndex - 1, 0)
        );
        return;
      }
      if (event.key === "Enter" && focusedIndex >= 0) {
        event.preventDefault();
        await optionItems[focusedIndex].onSelect();
        dropdownEl.style.display = "none";
        onDocumentClick({ target: document.body });
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        dropdownEl.style.display = "none";
        onDocumentClick({ target: document.body });
      }
    };
    const renderDropdown = async (query) => {
      const currentToken = ++renderToken;
      optionItems = [];
      focusedIndex = -1;
      while (dropdownEl.firstChild) {
        dropdownEl.removeChild(dropdownEl.firstChild);
      }

      const lowerQuery = query.toLowerCase().trim();
      await ensureDomainsLoaded();
      if (currentToken !== renderToken) {
        return;
      }
      const excludedIds =
        typeof getExcludedDomainIds === "function"
          ? getExcludedDomainIds()
          : [];

      const availableDomains = domains.filter(
        (domain) => !excludedIds.includes(domain.id)
      );

      const filtered = lowerQuery
        ? availableDomains.filter((domain) =>
            domain.name.toLowerCase().includes(lowerQuery)
          )
        : availableDomains;

      if (filtered.length > 0) {
        filtered.forEach((domain) => {
          const option = document.createElement("div");
          option.textContent = domain.name;
          option.setAttribute("role", "option");
          option.setAttribute("tabindex", "0");
          option.setAttribute("aria-selected", "false");
          option.style.cssText =
            "padding: 10px 16px; cursor: pointer; font-size: 14px; border-bottom: 1px solid #f3f4f6;";
          option.onmouseover = () => {
            option.style.background = "#f9fafb";
          };
          option.onmouseout = () => {
            option.style.background = "white";
          };
          const optionIndex = optionItems.length;
          optionItems.push({
            el: option,
            onSelect: async () => {
              await handleSelect(domain);
            },
          });
          option.addEventListener("focus", () => {
            updateFocusedIndex(optionIndex);
          });
          option.addEventListener("keydown", handleDropdownKey);
          option.onmousedown = async (event) => {
            event.preventDefault();
            await handleSelect(domain);
            if (currentToken !== renderToken) {
              return;
            }
            dropdownEl.style.display = "none";
            onDocumentClick({ target: document.body });
          };
          dropdownEl.appendChild(option);
        });
        dropdownEl.style.display = "block";
        ensureDocumentListener();
        return;
      }

      if (lowerQuery && resolvedShowCreateOption && resolvedAllowCreate) {
        const label =
          typeof createOptionText === "function"
            ? createOptionText(lowerQuery)
            : `Add new domain: ${lowerQuery}`;
        const addOption = document.createElement("div");
        addOption.textContent = label;
        addOption.setAttribute("role", "option");
        addOption.setAttribute("tabindex", "0");
        addOption.setAttribute("aria-selected", "false");
        addOption.style.cssText =
          "padding: 10px 16px; cursor: pointer; font-size: 14px; color: #6366f1; font-weight: 500;";
        addOption.onmouseover = () => {
          addOption.style.background = "#f9fafb";
        };
        addOption.onmouseout = () => {
          addOption.style.background = "white";
        };
        const optionIndex = optionItems.length;
        optionItems.push({
          el: addOption,
          onSelect: async () => {
            await handleCreate(lowerQuery);
          },
        });
        addOption.addEventListener("focus", () => {
          updateFocusedIndex(optionIndex);
        });
        addOption.addEventListener("keydown", handleDropdownKey);
        addOption.onmousedown = async (event) => {
          event.preventDefault();
          await handleCreate(lowerQuery);
          if (currentToken !== renderToken) {
            return;
          }
          dropdownEl.style.display = "none";
          onDocumentClick({ target: document.body });
        };
        dropdownEl.appendChild(addOption);
        dropdownEl.style.display = "block";
        ensureDocumentListener();
        return;
      }

      dropdownEl.style.display = "none";
      onDocumentClick({ target: document.body });
    };

    input.addEventListener("focus", () => {
      renderDropdown(input.value);
    });

    input.addEventListener("input", () => {
      if (input.setCustomValidity) {
        input.setCustomValidity("");
      }
      renderDropdown(input.value);
    });

    input.addEventListener("click", (event) => {
      event.stopPropagation();
    });

    input.addEventListener("keydown", handleDropdownKey);

    let isSubmitting = false;
    if (form) {
      form.addEventListener("submit", async (event) => {
        event.preventDefault();
        if (isSubmitting) {
          return;
        }
        const query = input.value.toLowerCase().trim();
        if (!query) {
          return;
        }

        isSubmitting = true;
        try {
          await ensureDomainsLoaded();
          const excludedIds =
            typeof getExcludedDomainIds === "function"
              ? getExcludedDomainIds()
              : [];
          const availableDomains = domains.filter(
            (domain) => !excludedIds.includes(domain.id)
          );
          const exactMatch = findExactDomain(query, availableDomains);

          if (exactMatch) {
            await handleSelect(exactMatch);
          } else if (resolvedAutoCreateOnSubmit && resolvedAllowCreate) {
            await handleCreate(query);
          }

          dropdownEl.style.display = "none";
          onDocumentClick({ target: document.body });
        } finally {
          isSubmitting = false;
        }
      });
    }
  };

  window.BBDomainSearch = {
    getDomains,
    loadOrganisationDomains,
    ensureDomainsLoaded,
    createDomain,
    ensureDomainByName,
    createDropdownElement,
    setupDomainSearchInput,
  };
})();
