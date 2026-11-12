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
  if (install()) return;
  var tries = 0;
  var timer = window.setInterval(function () {
    tries += 1;
    if (install() || tries > 50) window.clearInterval(timer);
  }, 20);
})();
