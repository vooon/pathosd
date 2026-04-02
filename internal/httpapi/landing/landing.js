(function () {
  const buttons = document.querySelectorAll("[data-trigger-check]");
  for (const button of buttons) {
    button.addEventListener("click", async function () {
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
        button.disabled = false;
      }
    });
  }
})();
