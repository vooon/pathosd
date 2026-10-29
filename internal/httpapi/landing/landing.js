(function () {
  const REFRESH_MS = 5000;
  const STORAGE_KEY = "pathosd.autoRefresh";
  // Fragments of the page that are swapped on refresh; everything else
  // (header, links, the toggle itself) is static.
  const LIVE_IDS = ["updated-at", "config-notice", "summary", "vips-body", "peers-body"];

  async function triggerCheck(button) {
    const vip = button.getAttribute("data-vip");
    const output = document.getElementById("result-" + vip);
    if (!vip || !output) {
      return;
    }

    button.disabled = true;
    output.textContent = "running...";
    output.className = "check-result";

    try {
      const endpoint = "/api/v1/vips/" + encodeURIComponent(vip) + "/check";
      const resp = await fetch(endpoint, { method: "POST" });
      const payload = await resp.json().catch(function () {
        return {};
      });
      if (!resp.ok) {
        throw new Error(payload.error || "HTTP " + resp.status);
      }

      const result = payload && payload.result ? payload.result : {};
      const success = result.success === true;
      const detail = result.detail || "no detail";
      output.textContent = success ? "success: " + detail : "fail: " + detail;
      output.className = success ? "check-result ok" : "check-result fail";
    } catch (err) {
      const message = err && err.message ? err.message : "request failed";
      output.textContent = "error: " + message;
      output.className = "check-result fail";
    } finally {
      // The row may have been re-rendered meanwhile; look the button up again.
      const current = document.querySelector('[data-trigger-check][data-vip="' + CSS.escape(vip) + '"]');
      (current || button).disabled = false;
    }
  }

  // Event delegation so re-rendered rows keep working without re-binding.
  document.addEventListener("click", function (event) {
    const button = event.target.closest("[data-trigger-check]");
    if (button) {
      triggerCheck(button);
    }
  });

  // Re-fetch the page and swap only the live fragments, carrying over the
  // ad-hoc check results and the disabled state of in-flight triggers.
  async function refresh() {
    let doc;
    try {
      const resp = await fetch(window.location.pathname, { cache: "no-store" });
      if (!resp.ok) {
        throw new Error("HTTP " + resp.status);
      }
      doc = new DOMParser().parseFromString(await resp.text(), "text/html");
    } catch (err) {
      const updated = document.getElementById("updated-at");
      if (updated) {
        updated.textContent = "refresh failed: " + (err && err.message ? err.message : "request failed");
      }
      return;
    }

    const results = {};
    for (const span of document.querySelectorAll(".check-result")) {
      if (span.textContent) {
        results[span.id] = { text: span.textContent, className: span.className };
      }
    }
    const inFlight = new Set();
    for (const button of document.querySelectorAll("[data-trigger-check]:disabled")) {
      inFlight.add(button.getAttribute("data-vip"));
    }

    for (const id of LIVE_IDS) {
      const next = doc.getElementById(id);
      const current = document.getElementById(id);
      if (next && current) {
        current.replaceWith(next);
      }
    }

    for (const id of Object.keys(results)) {
      const span = document.getElementById(id);
      if (span) {
        span.textContent = results[id].text;
        span.className = results[id].className;
      }
    }
    for (const button of document.querySelectorAll("[data-trigger-check]")) {
      if (inFlight.has(button.getAttribute("data-vip"))) {
        button.disabled = true;
      }
    }
  }

  const toggle = document.querySelector("[data-auto-refresh]");
  if (!toggle) {
    return;
  }

  let timer = null;

  function setEnabled(enabled) {
    toggle.textContent = "Auto-refresh: " + (enabled ? "ON" : "OFF");
    toggle.setAttribute("aria-pressed", enabled ? "true" : "false");
    toggle.classList.toggle("on", enabled);
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
    if (enabled) {
      timer = setInterval(function () {
        if (!document.hidden) {
          refresh();
        }
      }, REFRESH_MS);
    }
    try {
      localStorage.setItem(STORAGE_KEY, enabled ? "1" : "0");
    } catch (_) {
      // Storage may be unavailable (private mode); the toggle still works for this page load.
    }
  }

  toggle.addEventListener("click", function () {
    setEnabled(timer === null);
  });

  let stored = "0";
  try {
    stored = localStorage.getItem(STORAGE_KEY) || "0";
  } catch (_) {
    // ignore
  }
  setEnabled(stored === "1");
})();
