// Installed by the shell before page scripts. The product UI treats a missing
// bridge as the browser and does nothing native.
(function () {
  function install() {
    var internals = window.__TAURI_INTERNALS__;
    if (!internals || typeof internals.invoke !== "function") return false;
    if (window.__MERIDIAN_DESKTOP__) return true;
    window.__MERIDIAN_DESKTOP__ = {
      notify: function (title, body) {
        return internals.invoke("desktop_notify", { title: title, body: body });
      },
      setAttention: function (count) {
        return internals.invoke("desktop_set_attention", { count: count });
      },
      openGatewaySettings: function () {
        return internals.invoke("desktop_open_gateway");
      },
      checkForUpdates: function () {
        return internals.invoke("desktop_update_check");
      },
      installUpdate: function () {
        return internals.invoke("desktop_update_install");
      },
    };
    return true;
  }
  // A gateway UI that knows this bridge marks its index.html with
  // <meta name="meridian-desktop-bridge">. A /ui/ page without it was served by
  // a meridiand older than the desktop app: it has no Gateway control, update
  // notice, or desktop alerts. Say so once per page instead of failing quietly.
  // The text is fixed here; nothing from the page is shown.
  function isLoopback(host) {
    return host === "127.0.0.1" || host === "localhost" || host === "[::1]" || host === "::1";
  }
  function warnIfOlderGateway() {
    if (location.protocol !== "http:" && location.protocol !== "https:") return;
    if (location.hostname === "tauri.localhost") return;
    if (location.pathname.indexOf("/ui") !== 0) return;
    if (document.querySelector('meta[name="meridian-desktop-bridge"]')) return;
    if (!document.body || document.getElementById("meridian-desktop-older-gateway")) return;
    var host = document.createElement("div");
    host.id = "meridian-desktop-older-gateway";
    var root = host.attachShadow ? host.attachShadow({ mode: "closed" }) : host;
    var box = document.createElement("div");
    var text = document.createElement("span");
    text.textContent = isLoopback(location.hostname)
      ? "This meridiand is older than the Meridian desktop app, so desktop alerts, the Gateway control, and update notices are off. Update it with brew upgrade meridian, or the curl installer with --cli."
      : "This gateway runs a meridiand older than the Meridian desktop app, so desktop alerts, the Gateway control, and update notices are off. Update that gateway to the same Meridian version.";
    var close = document.createElement("button");
    close.type = "button";
    close.textContent = "Dismiss";
    close.addEventListener("click", function () {
      host.remove();
    });
    var s = box.style;
    s.position = "fixed";
    s.left = "12px";
    s.right = "12px";
    s.bottom = "12px";
    s.zIndex = "2147483647";
    s.display = "flex";
    s.gap = "12px";
    s.alignItems = "center";
    s.justifyContent = "space-between";
    s.padding = "10px 14px";
    s.borderRadius = "8px";
    s.background = "#2a2414";
    s.border = "1px solid #6b5a24";
    s.color = "#f3ead2";
    s.font = "13px/1.4 -apple-system, BlinkMacSystemFont, sans-serif";
    close.style.font = "inherit";
    close.style.color = "inherit";
    close.style.background = "transparent";
    close.style.border = "1px solid #6b5a24";
    close.style.borderRadius = "6px";
    close.style.padding = "4px 10px";
    close.style.cursor = "pointer";
    box.appendChild(text);
    box.appendChild(close);
    root.appendChild(box);
    document.body.appendChild(host);
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", warnIfOlderGateway, { once: true });
  } else {
    warnIfOlderGateway();
  }

  if (install()) return;
  var tries = 0;
  var timer = window.setInterval(function () {
    tries += 1;
    if (install() || tries > 50) window.clearInterval(timer);
  }, 20);
})();
