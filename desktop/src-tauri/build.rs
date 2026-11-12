fn main() {
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        tauri_build::AppManifest::new().commands(&[
            "gateway_state",
            "gateway_connect",
            "desktop_notify",
            "desktop_set_attention",
            "desktop_open_gateway",
            "desktop_update_check",
            "desktop_update_install",
        ]),
    ))
    .expect("failed to run tauri-build");
}
