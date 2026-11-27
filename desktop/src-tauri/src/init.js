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
      gateway: function () {
        return internals.invoke("desktop_gateway");
      },
      // ⌘1–9: the shell resolves the slot. The page gets no list.
      switchStation: function (slot) {
        return internals.invoke("desktop_switch_station", { slot: slot });
      },
      openAddStation: function () {
        return internals.invoke("desktop_open_gateway", { add: true });
      },
      setupCheck: function () {
        return internals.invoke("desktop_setup_check");
      },
      setSetupCheck: function (enabled) {
        return internals.invoke("desktop_setup_check_set", { enabled: enabled });
      },
      checkForUpdates: function () {
        return internals.invoke("desktop_update_check");
      },
      installUpdate: function () {
        return internals.invoke("desktop_update_install");
      },
      appInfo: function () {
        return internals.invoke("desktop_app_info");
      },
      // Opens the app's own import window. Nothing is passed, and only
      // whether it opened comes back (ADR 0049).
      openImport: function () {
        return internals.invoke("desktop_open_import");
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
      ? "This meridiand is older than the Meridian desktop app, so desktop alerts, the Station control, and update notices are off. Update it with brew upgrade meridian, or the curl installer with --cli."
      : "This Station runs a meridiand older than the Meridian desktop app, so desktop alerts, the Station control, and update notices are off. Update that Station to the same Meridian version.";
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

  // A target="_blank" link to another site can do nothing in this webview:
  // WebKit's new-window path for a noopener link on a Station page (which
  // sends Cross-Origin-Opener-Policy) never reaches the shell with an address
  // to open. Send that click as a navigation of this page instead. The shell
  // opens every address outside the Station in the system browser and cancels
  // the navigation, so the page stays where it is. Same-origin links are left
  // alone.
  //
  // A click arrives here already cancelled: tauri-plugin-shell injects a body
  // listener that cancels every new-tab http(s) link and asks for
  // plugin:shell|open, which a Station page is never granted. So a cancelled
  // click is not a sign that the page handled it.
  function externalLink(event) {
    if (event.button !== 0) return undefined;
    var node = event.target;
    var link = node && typeof node.closest === "function" ? node.closest("a[href]") : null;
    if (!link || link.hasAttribute("download")) return undefined;
    if ((link.getAttribute("target") || "").toLowerCase() !== "_blank") return undefined;
    var url;
    try {
      url = new URL(link.href, location.href);
    } catch (error) {
      return undefined;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") return undefined;
    if (url.origin === location.origin) return undefined;
    return url.href;
  }
  window.addEventListener("click", function (event) {
    var href = externalLink(event);
    if (!href) return;
    event.preventDefault();
    location.assign(href);
  });

  if (install()) return;
  var tries = 0;
  var timer = window.setInterval(function () {
    tries += 1;
    if (install() || tries > 50) window.clearInterval(timer);
  }, 20);
})();
