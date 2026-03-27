(function () {
  if (window.BBIntegrationHttp) {
    return;
  }

  const INTEGRATION_REQUEST_TIMEOUT_MS = 15000;

  class IntegrationHttpError extends Error {
    constructor(message, details = {}) {
      super(message, { cause: details.cause });
      this.name = "IntegrationHttpError";
      this.status = details.status;
      this.statusText = details.statusText;
      this.url = details.url;
      this.body = details.body;
      this.context = details.context;
    }
  }

  function withTimeoutSignal(timeoutMs = INTEGRATION_REQUEST_TIMEOUT_MS) {
    const controller = new AbortController();
    const timeoutId = window.setTimeout(() => {
      controller.abort("Request timed out");
    }, timeoutMs);
    return { signal: controller.signal, timeoutId };
  }

  async function fetchWithTimeout(url, options = {}, context = {}) {
    const { signal, timeoutId } = withTimeoutSignal();
    try {
      return await fetch(url, { ...options, signal });
    } catch (error) {
      if (error?.name === "AbortError") {
        throw new IntegrationHttpError("Request timed out", {
          cause: error,
          context,
        });
      }
      throw error;
    } finally {
      window.clearTimeout(timeoutId);
    }
  }

  function normaliseIntegrationError(response, body, context = {}) {
    return new IntegrationHttpError(body || `HTTP ${response.status}`, {
      status: response.status,
      statusText: response.statusText,
      url: response.url,
      body,
      context,
    });
  }

  window.BBIntegrationHttp = {
    IntegrationHttpError,
    withTimeoutSignal,
    fetchWithTimeout,
    normaliseIntegrationError,
  };
})();
