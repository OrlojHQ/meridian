//! Desktop shell. The product UI is the bundle meridiand serves at `/ui/`.
//! This process chooses a gateway, starts the bundled daemon when asked, and
//! exposes a three-command bridge for content-free alerts.

mod gateway;
mod notify_text;
mod sidecar;

use std::fs;
use std::path::PathBuf;
use std::sync::{Mutex, MutexGuard};
use std::time::{Duration, Instant};

use tauri::menu::{Menu, MenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager, RunEvent, WebviewUrl, WebviewWindowBuilder, WindowEvent};
use tauri_plugin_notification::NotificationExt;
use url::Url;

use gateway::{
    is_shell_url, parse_saved, probe_local, shell_index_url, validate_gateway_url, GatewayProbe,
    SavedGateway,
};
use notify_text::{attention_tooltip, validate_attention, validate_needs_you};
use sidecar::Sidecar;

const ATTENTION_ICON: &[u8] = include_bytes!("../icons/32x32-attention.png");
const IDLE_ICON: &[u8] = include_bytes!("../icons/32x32.png");

struct AppState {
    inner: Mutex<Inner>,
}

struct Inner {
    saved: Option<SavedGateway>,
    phase: Phase,
    message: String,
    active_origin: Option<String>,
    sidecar: Option<Sidecar>,
    quitting: bool,
    tray: bool,
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
        .setup(|app| {
            let saved = read_saved(app.handle());
            let starting = saved.is_some();
            app.manage(AppState {
                inner: Mutex::new(Inner {
                    saved: saved.clone(),
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
                    active_origin: None,
                    sidecar: None,
                    quitting: false,
                    tray: false,
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
            let _ = app.notification().on_action(move |_| show_main(&clicks));

            if let Some(choice) = saved {
                let handle = app.handle().clone();
                std::thread::spawn(move || {
                    let _ = connect(&handle, choice);
                });
            }
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            gateway_state,
            gateway_connect,
            desktop_notify,
            desktop_set_attention,
            desktop_open_gateway
        ])
        .build(tauri::generate_context!())
        .expect("failed to start Meridian")
        .run(|app, event| {
            if let RunEvent::ExitRequested { .. } = event {
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
    }
    if let Err(error) = write_saved(app, &choice) {
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
                    let detail = if tail.trim().is_empty() {
                        "The local gateway exited before it was ready.".to_string()
                    } else {
                        format!("The local gateway exited before it was ready.\n{tail}")
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
    {
        let state = app.state::<AppState>();
        lock(&state).active_origin = Some(choice.origin().to_string());
    }
    navigate(app, &url)
}

fn open_gateway(app: &AppHandle) -> Result<(), String> {
    {
        let state = app.state::<AppState>();
        let mut inner = lock(&state);
        inner.phase = Phase::Choose;
        inner.message.clear();
        inner.active_origin = None;
    }
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
    inner.active_origin = None;
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
        .active_origin
        .as_ref()
        .is_some_and(|active| active == &origin);
    if !allowed {
        open_external(url);
    }
    allowed
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

fn show_main(app: &AppHandle) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    let _ = window.show();
    let _ = window.unminimize();
    let _ = window.set_focus();
}

fn build_tray(app: &AppHandle) -> Result<(), String> {
    let open_item = MenuItem::with_id(app, "open", "Open Meridian", true, None::<&str>)
        .map_err(|error| error.to_string())?;
    let gateway_item = MenuItem::with_id(app, "gateway", "Gateway…", true, None::<&str>)
        .map_err(|error| error.to_string())?;
    let quit_item = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)
        .map_err(|error| error.to_string())?;
    let menu = Menu::with_items(app, &[&open_item, &gateway_item, &quit_item])
        .map_err(|error| error.to_string())?;
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
            "quit" => {
                lock(&app.state::<AppState>()).quitting = true;
                app.exit(0);
            }
            _ => {}
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

fn read_saved(app: &AppHandle) -> Option<SavedGateway> {
    let path = config_path(app).ok()?;
    let bytes = fs::read(path).ok()?;
    parse_saved(&bytes)
}

fn write_saved(app: &AppHandle, choice: &SavedGateway) -> Result<(), String> {
    let path = config_path(app)?;
    let temporary = path.with_extension("json.tmp");
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let file = options
        .open(&temporary)
        .map_err(|error| error.to_string())?;
    serde_json::to_writer_pretty(file, choice).map_err(|error| error.to_string())?;
    fs::rename(&temporary, &path).map_err(|error| error.to_string())?;
    Ok(())
}
