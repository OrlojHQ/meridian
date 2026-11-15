//! Desktop shell. The product UI is the bundle meridiand serves at `/ui/`.
//! This process chooses a gateway, starts the bundled daemon when asked, and
//! exposes a small bridge for content-free alerts and update checks.

mod bridge;
mod gateway;
mod notify_text;
mod profile_import;
mod sidecar;
mod sign_in;
mod update;

use std::collections::BTreeSet;
use std::fs;
use std::net::TcpListener;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::{Duration, Instant, SystemTime};

use tauri::menu::{CheckMenuItem, IsMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::utils::acl::RemoteUrlPattern;
use tauri::{AppHandle, Manager, RunEvent, WebviewUrl, WebviewWindowBuilder, WindowEvent, Wry};
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;
use url::Url;

use gateway::{
    encode_saved, forget, gateway_label, is_shell_url, origin_label, parse_recent, parse_saved,
    probe_local, remember, shell_index_url, validate_gateway_url, GatewayProbe, SavedGateway,
};
use notify_text::{attention_tooltip, validate_attention, validate_needs_you};
use sidecar::Sidecar;
use tauri_plugin_updater::UpdaterExt;
use update::{
    install_source_from_exe, update_action, updater_signing_configured, InstallSource, UpdateAction,
};

const ATTENTION_ICON: &[u8] = include_bytes!("../icons/32x32-attention.png");
const IDLE_ICON: &[u8] = include_bytes!("../icons/32x32.png");

struct AppState {
    inner: Mutex<Inner>,
}

struct Inner {
    saved: Option<SavedGateway>,
    /// Other gateways' origins, most recent first, for one-click switching
    /// from the picker and the tray. Never shown to a gateway page.
    recent: Vec<String>,
    phase: Phase,
    message: String,
    active: Option<ActiveGateway>,
    /// Origins granted a bridge capability in this process. Tauri cannot
    /// revoke one, so `bridge::call_allowed` refuses all but `active`.
    bridge_grants: BTreeSet<String>,
    sidecar: Option<Sidecar>,
    /// Counts sign-ins started in the system browser. A gateway switch, a
    /// failure, or a newer sign-in bumps it, and a waiting sign-in whose
    /// number is no longer current gives up (ADR 0047).
    sign_in: u64,
    quitting: bool,
    tray: bool,
    /// The open import window's background session (ADR 0049).
    import: Option<ImportSession>,
    /// Change-check baselines and the on/off setting.
    check: profile_import::CheckState,
}

/// One import window: the gateway origin it is bound to, the hidden
/// `desktop-import` process, and the messages it has sent.
struct ImportSession {
    origin: String,
    label: String,
    child: Option<CommandChild>,
    messages: Arc<Mutex<Vec<serde_json::Value>>>,
    finished: Arc<AtomicBool>,
}

/// The gateway the window may show, and the bridge pattern that matches it.
struct ActiveGateway {
    origin: String,
    bridge: RemoteUrlPattern,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Phase {
    Choose,
    Starting,
    Error,
}

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct GatewayView {
    mode: Option<String>,
    origin: Option<String>,
    phase: &'static str,
    message: String,
    recent: Vec<RecentGateway>,
}

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct RecentGateway {
    origin: String,
    label: String,
}

impl Inner {
    fn view(&self) -> GatewayView {
        let (mode, origin) = match &self.saved {
            Some(SavedGateway::Local) => (Some("local".to_string()), None),
            Some(SavedGateway::Remote { origin }) => {
                (Some("remote".to_string()), Some(origin.clone()))
            }
            None => (None, None),
        };
        GatewayView {
            mode,
            origin,
            phase: match self.phase {
                Phase::Choose => "choose",
                Phase::Starting => "starting",
                Phase::Error => "error",
            },
            message: self.message.clone(),
            recent: self
                .recent
                .iter()
                .map(|origin| RecentGateway {
                    origin: origin.clone(),
                    label: origin_label(origin),
                })
                .collect(),
        }
    }
}

fn lock(state: &AppState) -> MutexGuard<'_, Inner> {
    state
        .inner
        .lock()
        .unwrap_or_else(|error| error.into_inner())
}

pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .setup(|app| {
            let (saved, recent) = read_saved(app.handle());
            let starting = saved.is_some();
            app.manage(AppState {
                inner: Mutex::new(Inner {
                    saved: saved.clone(),
                    recent,
                    phase: if starting {
                        Phase::Starting
                    } else {
                        Phase::Choose
                    },
                    message: match &saved {
                        Some(SavedGateway::Local) => "Connecting to the local gateway…".into(),
                        Some(SavedGateway::Remote { .. }) => "Opening the gateway…".into(),
                        None => String::new(),
                    },
                    active: None,
                    bridge_grants: BTreeSet::new(),
                    sidecar: None,
                    sign_in: 0,
                    quitting: false,
                    tray: false,
                    import: None,
                    check: read_check(app.handle()),
                }),
            });

            let navigation = app.handle().clone();
            let window =
                WebviewWindowBuilder::new(app, "main", WebviewUrl::App("index.html".into()))
                    .title("Meridian")
                    .inner_size(1200.0, 800.0)
                    .min_inner_size(760.0, 520.0)
                    .initialization_script(include_str!("init.js"))
                    .on_navigation(move |url| allow_navigation(&navigation, url))
                    .on_new_window(move |url, _features| {
                        open_external(&url);
                        tauri::webview::NewWindowResponse::Deny
                    })
                    .build()?;

            let watched = window.clone();
            window.on_window_event(move |event| {
                if let WindowEvent::CloseRequested { api, .. } = event {
                    let state = watched.state::<AppState>();
                    let hide = lock(&state).tray;
                    if hide {
                        api.prevent_close();
                        let _ = watched.hide();
                    }
                }
            });

            match build_tray(app.handle()) {
                Ok(()) => lock(&app.state::<AppState>()).tray = true,
                Err(error) => eprintln!("Meridian tray is unavailable: {error}"),
            }

            let clicks = app.handle().clone();
            let _ = app.notification().on_action(move |_| {
                // The change prompt's notification opens the import window.
                if setup_change_pending(&clicks) {
                    open_import_in_background(&clicks);
                } else {
                    show_main(&clicks);
                }
            });

            let checks = app.handle().clone();
            std::thread::spawn(move || check_loop(checks));

            if let Some(choice) = saved {
                let handle = app.handle().clone();
                std::thread::spawn(move || {
                    let _ = connect(&handle, choice);
                });
            }
            Ok(())
        })
        .invoke_handler({
            let commands: Box<dyn Fn(tauri::ipc::Invoke) -> bool + Send + Sync> =
                Box::new(tauri::generate_handler![
                    gateway_state,
                    gateway_connect,
                    gateway_forget,
                    desktop_notify,
                    desktop_set_attention,
                    desktop_open_gateway,
                    desktop_gateway,
                    desktop_update_check,
                    desktop_update_install,
                    desktop_open_import,
                    import_state,
                    import_save,
                    import_dismiss,
                    import_cancel,
                    setup_check_state,
                    setup_check_set
                ]);
            move |invoke| {
                if bridge::is_bridge_command(invoke.message.command()) {
                    let state = invoke.message.webview_ref().state::<AppState>();
                    let allowed = bridge::call_allowed(
                        lock(&state).active.as_ref().map(|active| &active.bridge),
                        invoke.acl.as_deref(),
                    );
                    if !allowed {
                        invoke
                            .resolver
                            .reject("The desktop bridge is only for the chosen gateway.");
                        return true;
                    }
                }
                commands(invoke)
            }
        })
        .build(tauri::generate_context!())
        .expect("failed to start Meridian")
        .run(|app, event| {
            // The macOS menu's Quit (Cmd+Q) terminates the app without
            // ExitRequested; only Exit arrives. Stop the sidecar on either,
            // or it outlives the app holding 127.0.0.1:8080.
            if matches!(event, RunEvent::ExitRequested { .. } | RunEvent::Exit) {
                let sidecar = {
                    let state = app.state::<AppState>();
                    let mut inner = lock(&state);
                    inner.quitting = true;
                    inner.sidecar.take()
                };
                if let Some(sidecar) = sidecar {
                    sidecar.stop();
                }
            }
        });
}

#[tauri::command]
fn gateway_state(state: tauri::State<'_, AppState>) -> GatewayView {
    lock(&state).view()
}

// Async so the picker's click does not run on the main thread: connecting
// to the bundled gateway can wait up to a minute for readiness.
#[tauri::command]
async fn gateway_connect(app: AppHandle, mode: String, url: Option<String>) -> Result<(), String> {
    let choice = match mode.as_str() {
        "local" => SavedGateway::Local,
        "remote" => {
            let origin = validate_gateway_url(url.as_deref().unwrap_or(""))?;
            SavedGateway::Remote { origin }
        }
        _ => return Err("Choose this computer or another gateway.".into()),
    };
    tauri::async_runtime::spawn_blocking(move || connect(&app, choice))
        .await
        .map_err(|_| "Could not open the gateway.".to_string())?
}

/// Removes a gateway from the recent list. Picker only.
#[tauri::command]
fn gateway_forget(app: AppHandle, origin: String) -> Result<(), String> {
    let (saved, recent) = {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        inner.recent = forget(&inner.recent, &origin);
        (inner.saved.clone(), inner.recent.clone())
    };
    if let Some(saved) = saved {
        write_saved(&app, &saved, &recent)?;
    }
    refresh_tray(&app);
    Ok(())
}

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct GatewayIdentity {
    label: String,
    local: bool,
}

/// Names the gateway the window is on, for the page's status bar. It says
/// nothing about other gateways the operator uses.
#[tauri::command]
fn desktop_gateway(state: tauri::State<'_, AppState>) -> Result<GatewayIdentity, String> {
    let inner = lock(&state);
    match (&inner.saved, &inner.active) {
        (Some(saved), Some(active)) if saved.origin() == active.origin => Ok(GatewayIdentity {
            label: gateway_label(saved),
            local: *saved == SavedGateway::Local,
        }),
        _ => Err("No gateway is open.".into()),
    }
}

#[tauri::command]
fn desktop_notify(app: AppHandle, title: String, body: String) -> Result<(), String> {
    validate_needs_you(&title, &body).map_err(|_| "Notification text was rejected.".to_string())?;
    app.notification()
        .builder()
        .title(title)
        .body(body)
        .show()
        .map_err(|_| "The notification could not be shown.".to_string())
}

#[tauri::command]
fn desktop_set_attention(app: AppHandle, count: u32) -> Result<(), String> {
    let count =
        validate_attention(count).map_err(|_| "Attention count was rejected.".to_string())?;
    let tooltip = attention_tooltip(count);
    let bytes: &'static [u8] = if count == 0 {
        IDLE_ICON
    } else {
        ATTENTION_ICON
    };
    let handle = app.clone();
    let _ = app.run_on_main_thread(move || {
        let Some(tray) = handle.tray_by_id("meridian") else {
            return;
        };
        let _ = tray.set_tooltip(Some(tooltip));
        if let Ok(icon) = tauri::image::Image::from_bytes(bytes) {
            let _ = tray.set_icon(Some(icon));
        }
    });
    Ok(())
}

#[tauri::command]
fn desktop_open_gateway(app: AppHandle) -> Result<(), String> {
    open_gateway(&app)
}

/// The gateway page's only way into the import (ADR 0049): it takes no
/// arguments and returns only whether the window opened. Async so reading
/// the main window's cookies never runs on the main thread.
#[tauri::command]
async fn desktop_open_import(app: AppHandle) -> bool {
    open_import(&app).is_ok()
}

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct ImportView {
    origin: Option<String>,
    label: Option<String>,
    messages: Vec<serde_json::Value>,
    finished: bool,
}

/// The import window's own view of its session. Import window only.
#[tauri::command]
fn import_state(state: tauri::State<'_, AppState>) -> ImportView {
    let inner = lock(&state);
    match &inner.import {
        Some(session) => ImportView {
            origin: Some(session.origin.clone()),
            label: Some(session.label.clone()),
            messages: session
                .messages
                .lock()
                .map(|messages| messages.clone())
                .unwrap_or_default(),
            finished: session.finished.load(Ordering::SeqCst),
        },
        None => ImportView {
            origin: None,
            label: None,
            messages: Vec::new(),
            finished: true,
        },
    }
}

/// Save: the only step that uploads. Import window only.
#[tauri::command]
fn import_save(app: AppHandle) -> Result<(), String> {
    send_import_action(&app, "save")
}

/// Not now: record this computer's state as reviewed, upload nothing, and
/// stop prompting about this change. Import window only.
#[tauri::command]
fn import_dismiss(app: AppHandle) -> Result<(), String> {
    let review = {
        let state = app.state::<AppState>();
        let inner = lock(&state);
        inner.import.as_ref().and_then(|session| {
            let messages = session.messages.lock().ok()?;
            let review = messages.iter().find(|m| m["type"] == "review")?;
            Some((
                session.origin.clone(),
                review["account"]["id"].as_str()?.to_string(),
                review["digest"].as_str()?.to_string(),
            ))
        })
    };
    if let Some((origin, member, digest)) = review {
        record_baseline(&app, &origin, &member, &digest);
    }
    let _ = send_import_action(&app, "cancel");
    close_import(&app);
    Ok(())
}

/// Cancel: upload nothing. Import window only.
#[tauri::command]
fn import_cancel(app: AppHandle) -> Result<(), String> {
    let _ = send_import_action(&app, "cancel");
    close_import(&app);
    Ok(())
}

/// "Check this computer for setup changes". Picker only.
#[tauri::command]
fn setup_check_state(state: tauri::State<'_, AppState>) -> bool {
    lock(&state).check.enabled
}

#[tauri::command]
fn setup_check_set(app: AppHandle, enabled: bool) -> Result<(), String> {
    let check = {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        inner.check.enabled = enabled;
        inner.check.clone()
    };
    write_check(&app, &check)?;
    refresh_tray(&app);
    Ok(())
}

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct UpdateStatus {
    available: bool,
    version: Option<String>,
    action: Option<&'static str>,
    command: Option<&'static str>,
    detail: Option<&'static str>,
}

#[tauri::command]
async fn desktop_update_check(app: AppHandle) -> UpdateStatus {
    let updater = match app.updater() {
        Ok(updater) => updater,
        Err(_) => {
            return UpdateStatus {
                available: false,
                version: None,
                action: None,
                command: None,
                detail: Some("Couldn't check for updates."),
            };
        }
    };
    let checked = match updater.check().await {
        Ok(update) => update,
        Err(_) => {
            return UpdateStatus {
                available: false,
                version: None,
                action: None,
                command: None,
                detail: Some("Couldn't check for updates."),
            };
        }
    };
    let Some(update) = checked else {
        return UpdateStatus {
            available: false,
            version: None,
            action: None,
            command: None,
            detail: None,
        };
    };
    let source = current_exe_source();
    let (action, command) = match update_action(source, updater_signing_configured()) {
        UpdateAction::Install => ("install", None),
        UpdateAction::Command(command) => ("command", Some(command)),
    };
    UpdateStatus {
        available: true,
        version: Some(update.version),
        action: Some(action),
        command,
        detail: None,
    }
}

#[tauri::command]
async fn desktop_update_install(app: AppHandle) -> Result<(), String> {
    if !updater_signing_configured() || current_exe_source() != InstallSource::MacApp {
        return Err("This install does not update itself.".into());
    }
    let updater = app
        .updater()
        .map_err(|_| "Couldn't check for updates.".to_string())?;
    let update = updater
        .check()
        .await
        .map_err(|_| "Couldn't check for updates.".to_string())?
        .ok_or_else(|| "Meridian is up to date.".to_string())?;
    let bytes = update
        .download(|_, _| {}, || {})
        .await
        .map_err(|_| "The update could not be downloaded.".to_string())?;
    update
        .install(&bytes)
        .map_err(|_| "The update could not be installed.".to_string())?;
    let sidecar = {
        let state = app.state::<AppState>();
        let taken = lock(&state).sidecar.take();
        taken
    };
    if let Some(sidecar) = sidecar {
        sidecar.stop();
    }
    app.restart();
}

fn current_exe_source() -> InstallSource {
    let exe =
        std::env::current_exe().unwrap_or_else(|_| std::path::PathBuf::from("meridian-desktop"));
    install_source_from_exe(&exe)
}

fn connect(app: &AppHandle, choice: SavedGateway) -> Result<(), String> {
    {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        if inner.quitting {
            return Err("Meridian is quitting.".into());
        }
        inner.phase = Phase::Starting;
        inner.message = match &choice {
            SavedGateway::Local => "Connecting to the local gateway…".into(),
            SavedGateway::Remote { origin } => format!("Opening {origin}…"),
        };
        inner.saved = Some(choice.clone());
        if let SavedGateway::Remote { origin } = &choice {
            inner.recent = remember(&inner.recent, origin);
        }
    }
    let recent = lock(&app.state::<AppState>()).recent.clone();
    if let Err(error) = write_saved(app, &choice, &recent) {
        return fail(app, error);
    }
    let result = match &choice {
        SavedGateway::Local => connect_local(app),
        SavedGateway::Remote { origin } => connect_remote(app, origin),
    };
    if let Err(error) = result {
        return fail(app, error);
    }
    Ok(())
}

fn connect_local(app: &AppHandle) -> Result<(), String> {
    match probe_local() {
        GatewayProbe::Ready => {}
        GatewayProbe::Other => {
            return Err("127.0.0.1:8080 is open, but it is not a Meridian gateway.".into());
        }
        GatewayProbe::Starting => wait_until_ready(app, false)?,
        GatewayProbe::Down => {
            {
                let state = app.state::<AppState>();
                let mut inner = lock(&state);
                if inner.quitting {
                    return Err("Meridian is quitting.".into());
                }
                inner.message = "Starting the local gateway…".into();
                if inner.sidecar.as_ref().is_some_and(Sidecar::exited) {
                    inner.sidecar.take();
                }
                if inner.sidecar.is_none() {
                    inner.sidecar = Some(Sidecar::spawn(app)?);
                }
            }
            wait_until_ready(app, true)?;
        }
    }
    finish_navigation(app, &SavedGateway::Local)
}

fn connect_remote(app: &AppHandle, origin: &str) -> Result<(), String> {
    let sidecar = {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        inner.sidecar.take()
    };
    if let Some(sidecar) = sidecar {
        sidecar.stop();
    }
    finish_navigation(
        app,
        &SavedGateway::Remote {
            origin: origin.to_string(),
        },
    )
}

fn wait_until_ready(app: &AppHandle, own_process: bool) -> Result<(), String> {
    let started = Instant::now();
    loop {
        if lock(&app.state::<AppState>()).quitting {
            return Err("Meridian is quitting.".into());
        }
        match probe_local() {
            GatewayProbe::Ready => return Ok(()),
            GatewayProbe::Other => {
                return Err("127.0.0.1:8080 is open, but it is not a Meridian gateway.".into());
            }
            GatewayProbe::Down | GatewayProbe::Starting => {}
        }
        if own_process {
            let state = app.state::<AppState>();
            let inner = lock(&state);
            if let Some(sidecar) = &inner.sidecar {
                if sidecar.exited() {
                    let tail = sidecar.log_tail();
                    let lead = sidecar::explain_exit(&tail)
                        .unwrap_or("The local gateway exited before it was ready.");
                    let detail = if tail.trim().is_empty() {
                        lead.to_string()
                    } else {
                        format!("{lead}\n{tail}")
                    };
                    return Err(detail);
                }
            }
        }
        if started.elapsed() > Duration::from_secs(60) {
            return Err("The local gateway did not become ready.".into());
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

fn finish_navigation(app: &AppHandle, choice: &SavedGateway) -> Result<(), String> {
    let url = choice.ui_url();
    let origin = choice.origin().to_string();
    let pattern = bridge::origin_pattern(&origin)?;
    let matcher = bridge::parse_pattern(&pattern)?;
    {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        if !inner.bridge_grants.contains(&origin) {
            let id = bridge::capability_id(inner.bridge_grants.len());
            app.add_capability(bridge::capability(&id, &pattern))
                .map_err(|_| "The desktop bridge could not be granted.".to_string())?;
            inner.bridge_grants.insert(origin.clone());
        }
        inner.active = Some(ActiveGateway {
            origin,
            bridge: matcher,
        });
        inner.sign_in += 1;
    }
    close_import(app);
    set_title(app, &format!("Meridian — {}", gateway_label(choice)));
    refresh_tray(app);
    navigate(app, &url)
}

fn open_gateway(app: &AppHandle) -> Result<(), String> {
    {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        inner.phase = Phase::Choose;
        inner.message.clear();
        inner.active = None;
        inner.sign_in += 1;
    }
    close_import(app);
    set_title(app, "Meridian");
    refresh_tray(app);
    navigate(app, shell_index_url())
}

fn navigate(app: &AppHandle, url: &str) -> Result<(), String> {
    let parsed: Url = url
        .parse()
        .map_err(|_| "The gateway address is not a URL.".to_string())?;
    let (sender, receiver) = std::sync::mpsc::channel();
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let result = match handle.get_webview_window("main") {
            Some(window) => window
                .navigate(parsed)
                .map_err(|error| error.to_string())
                .map(|_| {
                    let _ = window.show();
                    let _ = window.unminimize();
                    let _ = window.set_focus();
                }),
            None => Err("The Meridian window is not open.".into()),
        };
        let _ = sender.send(result);
    })
    .map_err(|error| error.to_string())?;
    receiver
        .recv_timeout(Duration::from_secs(5))
        .map_err(|_| "The window did not open the gateway.".to_string())?
}

fn fail(app: &AppHandle, message: String) -> Result<(), String> {
    let message = clamp(&message);
    let state = app.state::<AppState>();
    let mut inner = lock(&state);
    inner.phase = Phase::Error;
    inner.message = message.clone();
    inner.active = None;
    inner.sign_in += 1;
    Err(message)
}

fn clamp(message: &str) -> String {
    let trimmed = message.trim();
    if trimmed.chars().count() <= 600 {
        return trimmed.to_string();
    }
    let mut end = 600;
    while !trimmed.is_char_boundary(end) {
        end -= 1;
    }
    format!("{}…", &trimmed[..end])
}

fn allow_navigation(app: &AppHandle, url: &Url) -> bool {
    if is_shell_url(url) {
        return true;
    }
    let origin = url.origin().ascii_serialization();
    let allowed = lock(&app.state::<AppState>())
        .active
        .as_ref()
        .is_some_and(|active| active.origin == origin);
    if allowed && sign_in::is_sign_in_start(url, &origin) {
        // The identity provider never loads in the webview (ADR 0047).
        begin_sign_in(app, origin);
        return false;
    }
    if !allowed {
        open_external(url);
    }
    allowed
}

/// Runs a team-mode sign-in in the system browser and redeems its ticket in
/// the webview, so the session cookie lands here (ADR 0047).
fn begin_sign_in(app: &AppHandle, origin: String) {
    let number = {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        inner.sign_in += 1;
        inner.sign_in
    };
    let prepared = TcpListener::bind(("127.0.0.1", 0))
        .map_err(|_| "No loopback port for sign-in.".to_string())
        .and_then(|listener| {
            let port = listener
                .local_addr()
                .map_err(|_| "No loopback port for sign-in.".to_string())?
                .port();
            let handoff = sign_in::Handoff::generate()?;
            let start = sign_in::start_url(&origin, port, &handoff)?;
            Ok((listener, handoff, start))
        });
    let (listener, handoff, start) = match prepared {
        Ok(prepared) => prepared,
        Err(error) => {
            eprintln!("Meridian sign-in could not start: {error}");
            return;
        }
    };
    open_external(&start);

    let handle = app.clone();
    std::thread::spawn(move || {
        let deadline = Instant::now() + sign_in::TIMEOUT;
        let ticket = sign_in::wait_for_ticket(&listener, &handoff.state, deadline, &|| {
            !sign_in_current(&handle, number, &origin)
        });
        drop(listener);
        let Some(ticket) = ticket else {
            return;
        };
        let script = sign_in::redeem_script(&origin, &ticket, &handoff.verifier);
        let main = handle.clone();
        let _ = handle.run_on_main_thread(move || {
            if !sign_in_current(&main, number, &origin) {
                return;
            }
            let Some(window) = main.get_webview_window("main") else {
                return;
            };
            let on_gateway = window
                .url()
                .is_ok_and(|url| url.origin().ascii_serialization() == origin);
            if !on_gateway {
                return;
            }
            let _ = window.eval(&script);
            show_main(&main);
        });
    });
}

/// Reports whether sign-in `number` is still the one to finish: no newer
/// sign-in, gateway switch, failure, or quit has happened since.
fn sign_in_current(app: &AppHandle, number: u64, origin: &str) -> bool {
    let state = app.state::<AppState>();
    let inner = lock(&state);
    !inner.quitting
        && inner.sign_in == number
        && inner
            .active
            .as_ref()
            .is_some_and(|active| active.origin == origin)
}

fn open_external(url: &Url) {
    if url.scheme() != "http" && url.scheme() != "https" {
        return;
    }
    let target = url.to_string();
    std::thread::spawn(move || {
        let _ = open::that(target);
    });
}

fn request_update_check(app: &AppHandle) {
    show_main(app);
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    let _ = window.eval("window.dispatchEvent(new CustomEvent('meridian-check-updates'))");
}

fn show_main(app: &AppHandle) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    let _ = window.show();
    let _ = window.unminimize();
    let _ = window.set_focus();
}

fn set_title(app: &AppHandle, title: &str) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.set_title(title);
    }
}

/// The tray menu: the window, the picker, one-click switching between this
/// computer and recent gateways (the active one checked), updates, and Quit.
fn tray_menu(app: &AppHandle) -> Result<Menu<Wry>, String> {
    let (saved, active, recent) = {
        let state = app.state::<AppState>();
        let inner = lock(&state);
        (
            inner.saved.clone(),
            inner.active.as_ref().map(|active| active.origin.clone()),
            inner.recent.clone(),
        )
    };
    let on = |choice: &SavedGateway| {
        saved.as_ref() == Some(choice) && active.as_deref() == Some(choice.origin())
    };
    let error = |error: tauri::Error| error.to_string();
    let mut switches = vec![CheckMenuItem::with_id(
        app,
        "switch-local",
        "This computer",
        true,
        on(&SavedGateway::Local),
        None::<&str>,
    )
    .map_err(error)?];
    for origin in &recent {
        let choice = SavedGateway::Remote {
            origin: origin.clone(),
        };
        switches.push(
            CheckMenuItem::with_id(
                app,
                format!("switch:{origin}"),
                origin_label(origin),
                true,
                on(&choice),
                None::<&str>,
            )
            .map_err(error)?,
        );
    }
    let switch_items: Vec<&dyn IsMenuItem<Wry>> = switches
        .iter()
        .map(|item| item as &dyn IsMenuItem<Wry>)
        .collect();
    let switch = Submenu::with_items(app, "Switch gateway", true, &switch_items).map_err(error)?;
    let open_item =
        MenuItem::with_id(app, "open", "Open Meridian", true, None::<&str>).map_err(error)?;
    let gateway_item =
        MenuItem::with_id(app, "gateway", "Gateway…", true, None::<&str>).map_err(error)?;
    let import_label = if setup_change_pending(app) {
        "Review setup changes…"
    } else {
        "Import this computer's setup…"
    };
    let import_item =
        MenuItem::with_id(app, "import", import_label, active.is_some(), None::<&str>)
            .map_err(error)?;
    let updates_item = MenuItem::with_id(app, "updates", "Check for updates…", true, None::<&str>)
        .map_err(error)?;
    let quit_item = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>).map_err(error)?;
    let separator = PredefinedMenuItem::separator(app).map_err(error)?;
    Menu::with_items(
        app,
        &[
            &open_item,
            &gateway_item,
            &switch,
            &import_item,
            &separator,
            &updates_item,
            &quit_item,
        ],
    )
    .map_err(error)
}

fn refresh_tray(app: &AppHandle) {
    let handle = app.clone();
    let _ = app.run_on_main_thread(move || {
        let Some(tray) = handle.tray_by_id("meridian") else {
            return;
        };
        if let Ok(menu) = tray_menu(&handle) {
            let _ = tray.set_menu(Some(menu));
        }
    });
}

/// Switches from the tray. A remote must still be in the recent list and
/// validate, so a stale menu cannot open an address the operator forgot.
fn switch_gateway(app: &AppHandle, id: &str) {
    let choice = if id == "switch-local" {
        SavedGateway::Local
    } else {
        let Some(origin) = id.strip_prefix("switch:") else {
            return;
        };
        let known = lock(&app.state::<AppState>())
            .recent
            .iter()
            .any(|kept| kept == origin);
        match validate_gateway_url(origin) {
            Ok(valid) if known && valid == origin => SavedGateway::Remote { origin: valid },
            _ => return,
        }
    };
    let already = {
        let state = app.state::<AppState>();
        let inner = lock(&state);
        inner.saved.as_ref() == Some(&choice)
            && inner
                .active
                .as_ref()
                .is_some_and(|active| active.origin == choice.origin())
    };
    if already {
        show_main(app);
        refresh_tray(app);
        return;
    }
    let handle = app.clone();
    std::thread::spawn(move || {
        if connect(&handle, choice).is_err() {
            let _ = navigate(&handle, shell_index_url());
        }
    });
}

fn build_tray(app: &AppHandle) -> Result<(), String> {
    let menu = tray_menu(app)?;
    let icon = app
        .default_window_icon()
        .cloned()
        .ok_or_else(|| "The application icon is missing.".to_string())?;
    let _tray = TrayIconBuilder::with_id("meridian")
        .icon(icon)
        .menu(&menu)
        .tooltip("Meridian")
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id.as_ref() {
            "open" => show_main(app),
            "gateway" => {
                let _ = open_gateway(app);
            }
            "updates" => request_update_check(app),
            "import" => open_import_in_background(app),
            "quit" => {
                lock(&app.state::<AppState>()).quitting = true;
                app.exit(0);
            }
            other => switch_gateway(app, other),
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                show_main(tray.app_handle());
            }
        })
        .build(app)
        .map_err(|error| error.to_string())?;
    Ok(())
}

/// Opens the import window from the tray or a notification, off the main
/// thread so reading cookies cannot block it.
fn open_import_in_background(app: &AppHandle) {
    let handle = app.clone();
    std::thread::spawn(move || {
        let _ = open_import(&handle);
    });
}

/// Opens the import window bound to the active gateway origin, and starts
/// the hidden `desktop-import` mode with the main window's session for that
/// origin. A window already open is focused instead.
fn open_import(app: &AppHandle) -> Result<(), String> {
    if let Some(window) = app.get_webview_window(profile_import::WINDOW) {
        let _ = window.show();
        let _ = window.set_focus();
        return Ok(());
    }
    let (origin, label) = {
        let state = app.state::<AppState>();
        let inner = lock(&state);
        match (&inner.saved, &inner.active) {
            (Some(saved), Some(active)) if saved.origin() == active.origin => {
                (active.origin.clone(), gateway_label(saved))
            }
            _ => return Err("Open a gateway first.".into()),
        }
    };
    let url = Url::parse(&origin).map_err(|_| "The gateway address is not valid.".to_string())?;
    let main = app
        .get_webview_window("main")
        .ok_or_else(|| "The Meridian window is closed.".to_string())?;
    let cookies: Vec<(String, String)> = main
        .cookies_for_url(url)
        .unwrap_or_default()
        .into_iter()
        .map(|cookie| (cookie.name().to_string(), cookie.value().to_string()))
        .collect();
    let header = profile_import::cookie_header(&cookies);
    let command = app
        .shell()
        .sidecar("meridian-desktop-gateway")
        .map_err(|error| format!("The bundled gateway is not installed ({error})."))?;
    let (mut events, mut child) = command
        .args(["desktop-import"])
        .spawn()
        .map_err(|error| format!("Could not start the import ({error})."))?;
    child
        .write(&profile_import::hello_line(&origin, header.as_deref()))
        .map_err(|_| "Could not start the import.".to_string())?;
    drop(header);
    drop(cookies);

    let messages = Arc::new(Mutex::new(Vec::new()));
    let finished = Arc::new(AtomicBool::new(false));
    {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        if let Some(previous) = inner.import.take() {
            if let Some(child) = previous.child {
                let _ = child.kill();
            }
        }
        inner.import = Some(ImportSession {
            origin: origin.clone(),
            label,
            child: Some(child),
            messages: Arc::clone(&messages),
            finished: Arc::clone(&finished),
        });
    }
    let relay = app.clone();
    let bound = origin.clone();
    tauri::async_runtime::spawn(async move {
        let mut member = String::new();
        while let Some(event) = events.recv().await {
            match event {
                CommandEvent::Stdout(line) => {
                    let Some(message) = profile_import::parse_message(line.trim_ascii()) else {
                        continue;
                    };
                    if message["type"] == "review" {
                        member = message["account"]["id"]
                            .as_str()
                            .unwrap_or_default()
                            .to_string();
                    }
                    if message["type"] == "saved" && !member.is_empty() {
                        if let Some(digest) = message["digest"].as_str() {
                            record_baseline(&relay, &bound, &member, digest);
                        }
                    }
                    if let Ok(mut kept) = messages.lock() {
                        kept.push(message);
                    }
                }
                CommandEvent::Terminated(_) => break,
                _ => {}
            }
        }
        finished.store(true, Ordering::SeqCst);
    });

    let closing = app.clone();
    let window = WebviewWindowBuilder::new(
        app,
        profile_import::WINDOW,
        WebviewUrl::App("import.html".into()),
    )
    .title("Import this computer's setup")
    .inner_size(640.0, 720.0)
    .min_inner_size(480.0, 480.0)
    .on_navigation(is_shell_url)
    .on_new_window(|_url, _features| tauri::webview::NewWindowResponse::Deny)
    .build()
    .map_err(|_| "The import window could not open.".to_string())?;
    window.on_window_event(move |event| {
        if let WindowEvent::Destroyed = event {
            end_import(&closing);
        }
    });
    Ok(())
}

fn send_import_action(app: &AppHandle, action: &str) -> Result<(), String> {
    let state = app.state::<AppState>();
    let mut inner = lock(&state);
    let child = inner
        .import
        .as_mut()
        .and_then(|session| session.child.as_mut())
        .ok_or_else(|| "The import has ended.".to_string())?;
    child
        .write(&profile_import::action_line(action))
        .map_err(|_| "The import has ended.".to_string())
}

/// Ends the session without uploading: its process is stopped.
fn end_import(app: &AppHandle) {
    let session = lock(&app.state::<AppState>()).import.take();
    if let Some(child) = session.and_then(|session| session.child) {
        let _ = child.kill();
    }
}

/// Closes the import window, for Cancel and whenever the gateway changes.
fn close_import(app: &AppHandle) {
    end_import(app);
    if let Some(window) = app.get_webview_window(profile_import::WINDOW) {
        let _ = window.close();
    }
}

fn record_baseline(app: &AppHandle, origin: &str, member: &str, digest: &str) {
    let check = {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        inner.check.record(origin, member, digest);
        inner.check.clone()
    };
    let _ = write_check(app, &check);
    refresh_tray(app);
}

fn setup_change_pending(app: &AppHandle) -> bool {
    let state = app.state::<AppState>();
    let inner = lock(&state);
    inner
        .active
        .as_ref()
        .is_some_and(|active| inner.check.pending(&active.origin))
}

/// The change check (ADR 0049): at launch, on wake, and every 30 minutes
/// while the setting is on and a gateway is open. It runs only the hidden
/// mode's `--check`, which takes no session, makes no network request, and
/// prints only a digest.
fn check_loop(app: AppHandle) {
    let mut last: Option<Instant> = None;
    let mut wall = SystemTime::now();
    let mut tick = Instant::now();
    loop {
        std::thread::sleep(if last.is_none() {
            Duration::from_secs(5)
        } else {
            Duration::from_secs(60)
        });
        let slept = profile_import::woke(wall, SystemTime::now(), tick.elapsed());
        wall = SystemTime::now();
        tick = Instant::now();
        let (origin, enabled, quitting) = {
            let state = app.state::<AppState>();
            let inner = lock(&state);
            (
                inner.active.as_ref().map(|active| active.origin.clone()),
                inner.check.enabled,
                inner.quitting,
            )
        };
        if quitting {
            return;
        }
        let Some(origin) = origin else { continue };
        let due = slept || last.is_none_or(|at| at.elapsed() >= profile_import::CHECK_INTERVAL);
        if !enabled || !due {
            continue;
        }
        last = Some(Instant::now());
        let Some(digest) = run_check(&app) else {
            continue;
        };
        let prompt = {
            let state = app.state::<AppState>();
            let mut inner = lock(&state);
            let still = inner
                .active
                .as_ref()
                .is_some_and(|active| active.origin == origin);
            let prompt = still && inner.check.should_prompt(&origin, &digest);
            if prompt {
                inner.check.notified.insert(origin.clone(), digest.clone());
            }
            prompt.then(|| inner.check.clone())
        };
        if let Some(check) = prompt {
            let _ = write_check(&app, &check);
            let _ = app
                .notification()
                .builder()
                .title(profile_import::CHANGED_TITLE)
                .body(profile_import::CHANGED_BODY)
                .show();
            refresh_tray(&app);
        }
    }
}

/// Runs `desktop-import --check` and returns its digest. A check that does
/// not finish within a minute is stopped and reports nothing.
fn run_check(app: &AppHandle) -> Option<String> {
    let command = app.shell().sidecar("meridian-desktop-gateway").ok()?;
    let (mut events, child) = command.args(["desktop-import", "--check"]).spawn().ok()?;
    let child = Arc::new(Mutex::new(Some(child)));
    let watchdog = Arc::clone(&child);
    std::thread::spawn(move || {
        std::thread::sleep(Duration::from_secs(60));
        if let Some(child) = watchdog.lock().ok().and_then(|mut child| child.take()) {
            let _ = child.kill();
        }
    });
    let mut output = Vec::new();
    while let Some(event) = events.blocking_recv() {
        match event {
            CommandEvent::Stdout(line) => {
                if output.len() + line.len() <= 4096 {
                    output.extend_from_slice(&line);
                }
            }
            CommandEvent::Terminated(status) => {
                if status.code != Some(0) {
                    return None;
                }
                break;
            }
            _ => {}
        }
    }
    child.lock().ok().and_then(|mut child| child.take());
    profile_import::parse_check(output.trim_ascii())
}

fn check_path(app: &AppHandle) -> Result<PathBuf, String> {
    Ok(config_path(app)?.with_file_name("profile-check.json"))
}

fn read_check(app: &AppHandle) -> profile_import::CheckState {
    check_path(app)
        .ok()
        .and_then(|path| fs::read(path).ok())
        .map(|bytes| profile_import::CheckState::parse(&bytes))
        .unwrap_or_default()
}

fn write_check(app: &AppHandle, check: &profile_import::CheckState) -> Result<(), String> {
    let bytes = serde_json::to_vec(check).map_err(|error| error.to_string())?;
    write_private(&check_path(app)?, &bytes)
}

/// Writes a file only this user can read, atomically.
fn write_private(path: &std::path::Path, bytes: &[u8]) -> Result<(), String> {
    let temporary = path.with_extension("json.tmp");
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options
        .open(&temporary)
        .map_err(|error| error.to_string())?;
    std::io::Write::write_all(&mut file, bytes).map_err(|error| error.to_string())?;
    fs::rename(&temporary, path).map_err(|error| error.to_string())?;
    Ok(())
}

fn config_path(app: &AppHandle) -> Result<PathBuf, String> {
    let directory = app
        .path()
        .app_config_dir()
        .map_err(|error| error.to_string())?;
    fs::create_dir_all(&directory).map_err(|error| error.to_string())?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&directory, fs::Permissions::from_mode(0o700));
    }
    Ok(directory.join("gateway.json"))
}

fn read_saved(app: &AppHandle) -> (Option<SavedGateway>, Vec<String>) {
    let Some(bytes) = config_path(app).ok().and_then(|path| fs::read(path).ok()) else {
        return (None, Vec::new());
    };
    (parse_saved(&bytes), parse_recent(&bytes))
}

fn write_saved(app: &AppHandle, choice: &SavedGateway, recent: &[String]) -> Result<(), String> {
    let path = config_path(app)?;
    let temporary = path.with_extension("json.tmp");
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let bytes = encode_saved(choice, recent)?;
    let mut file = options
        .open(&temporary)
        .map_err(|error| error.to_string())?;
    std::io::Write::write_all(&mut file, &bytes).map_err(|error| error.to_string())?;
    fs::rename(&temporary, &path).map_err(|error| error.to_string())?;
    Ok(())
}

#[cfg(test)]
mod tests {
    #[test]
    fn older_gateway_check_matches_the_ui_marker() {
        let init = include_str!("init.js");
        let index = include_str!("../../../frontend/index.html");
        assert!(init.contains(r#"meta[name="meridian-desktop-bridge"]"#));
        assert!(index.contains(r#"<meta name="meridian-desktop-bridge" content="1" />"#));
    }
}
