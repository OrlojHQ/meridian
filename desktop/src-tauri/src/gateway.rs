//! Gateway choice. The shell remembers which Station the window uses, the
//! name of the Station on this computer, and every saved remote Station.
//! It never stores an installation token, a session, or anything but names
//! and origins.

use std::io::{Read, Write};
use std::net::{IpAddr, Ipv4Addr, SocketAddr, TcpStream, ToSocketAddrs};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{mpsc, Mutex};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use url::Url;

pub const LOCAL_ORIGIN: &str = "http://127.0.0.1:8080";
pub const LOCAL_LISTEN: &str = "127.0.0.1:8080";
/// Stable id of the Station on this computer. It can be renamed and cannot
/// be removed.
pub const LOCAL_ID: &str = "local";
const LOCAL_PORT: u16 = 8080;
/// The longest Station name, in Unicode scalar values. The web UI checks
/// names with the same count.
pub const MAX_NAME_CHARS: usize = 60;
/// The longest saved origin, in bytes, after a scheme is added and the host
/// is converted to ASCII. The web UI accepts ids up to this length.
pub const MAX_ORIGIN_BYTES: usize = 300;
/// A hostile file cannot grow without bound. This is not a product cap:
/// a person can save every Station they use.
const MAX_SAVED_STATIONS: usize = 512;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "mode", rename_all = "snake_case")]
pub enum SavedGateway {
    Local,
    Remote { origin: String },
}

impl SavedGateway {
    pub fn origin(&self) -> &str {
        match self {
            Self::Local => LOCAL_ORIGIN,
            Self::Remote { origin } => origin,
        }
    }

    pub fn ui_url(&self) -> String {
        format!("{}/ui/", self.origin().trim_end_matches('/'))
    }
}

pub fn parse_saved(bytes: &[u8]) -> Option<SavedGateway> {
    let value: SavedGateway = serde_json::from_slice(bytes).ok()?;
    match value {
        SavedGateway::Local => Some(SavedGateway::Local),
        SavedGateway::Remote { origin } => {
            let origin = validate_gateway_url(&origin).ok()?;
            Some(SavedGateway::Remote { origin })
        }
    }
}

/// One saved remote Station: a display name and a validated origin.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RemoteStation {
    pub name: String,
    pub origin: String,
}

/// Names and origins the shell keeps. The local Station is always present.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StationBook {
    pub local_name: String,
    pub remotes: Vec<RemoteStation>,
}

impl Default for StationBook {
    fn default() -> Self {
        Self {
            local_name: default_local_name().to_string(),
            remotes: Vec::new(),
        }
    }
}

/// What a saved file contained, after validation. `migrated` is set when the
/// file still used the old recent-origin list, so the caller rewrites it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LoadedStations {
    pub choice: Option<SavedGateway>,
    pub book: StationBook,
    pub migrated: bool,
}

pub fn default_local_name() -> &'static str {
    if cfg!(target_os = "macos") {
        "This Mac"
    } else {
        "This computer"
    }
}

pub fn validate_station_name(raw: &str) -> Result<String, String> {
    let name = raw.trim();
    if name.is_empty() {
        return Err("Enter a name.".into());
    }
    if name.chars().count() > MAX_NAME_CHARS {
        return Err("That name is too long.".into());
    }
    if name.chars().any(char::is_control) {
        return Err("That name cannot include a line break or other control character.".into());
    }
    Ok(name.to_string())
}

/// Reads a saved file. Invalid origins are dropped. A file that still has
/// only `recent` becomes named Stations, one per origin, named by host.
pub fn load_stations(bytes: &[u8]) -> LoadedStations {
    let Ok(value) = serde_json::from_slice::<serde_json::Value>(bytes) else {
        return LoadedStations {
            choice: None,
            book: StationBook::default(),
            migrated: false,
        };
    };
    let choice = parse_saved(bytes);
    let local_name = value
        .get("localName")
        .and_then(|name| name.as_str())
        .and_then(|name| validate_station_name(name).ok())
        .unwrap_or_else(|| default_local_name().to_string());
    let explicit = value
        .get("stations")
        .and_then(|stations| stations.as_array());
    let migrated = explicit.is_none();
    let mut remotes = Vec::new();
    let raws: Vec<serde_json::Value> = if let Some(stations) = explicit {
        stations.clone()
    } else {
        value
            .get("recent")
            .and_then(|recent| recent.as_array())
            .cloned()
            .unwrap_or_default()
    };
    for raw in raws.into_iter().take(MAX_SAVED_STATIONS.saturating_mul(2)) {
        let (name, origin_raw) = if let Some(origin) = raw.as_str() {
            (None, origin.to_string())
        } else if let Some(object) = raw.as_object() {
            let origin = object
                .get("origin")
                .and_then(|origin| origin.as_str())
                .unwrap_or("");
            let name = object.get("name").and_then(|name| name.as_str());
            (name, origin.to_string())
        } else {
            continue;
        };
        let Ok(origin) = validate_gateway_url(&origin_raw) else {
            continue;
        };
        if origin == LOCAL_ORIGIN
            || remotes
                .iter()
                .any(|station: &RemoteStation| station.origin == origin)
        {
            continue;
        }
        let name = name
            .and_then(|name| validate_station_name(name).ok())
            .unwrap_or_else(|| origin_label(&origin));
        remotes.push(RemoteStation { name, origin });
        if remotes.len() == MAX_SAVED_STATIONS {
            break;
        }
    }
    if let Some(SavedGateway::Remote { origin }) = &choice {
        if origin != LOCAL_ORIGIN && !remotes.iter().any(|station| &station.origin == origin) {
            if remotes.len() < MAX_SAVED_STATIONS {
                remotes.insert(
                    0,
                    RemoteStation {
                        name: origin_label(origin),
                        origin: origin.clone(),
                    },
                );
            }
        }
    }
    LoadedStations {
        choice,
        book: StationBook {
            local_name,
            remotes,
        },
        migrated: migrated && value.get("recent").is_some(),
    }
}

pub fn encode_saved(choice: Option<&SavedGateway>, book: &StationBook) -> Result<Vec<u8>, String> {
    let mut value = match choice {
        Some(choice) => serde_json::to_value(choice).map_err(|error| error.to_string())?,
        None => serde_json::json!({}),
    };
    let Some(object) = value.as_object_mut() else {
        return Err("Could not save Stations.".into());
    };
    object.insert("localName".into(), serde_json::json!(book.local_name));
    object.insert(
        "stations".into(),
        serde_json::json!(book
            .remotes
            .iter()
            .map(|station| serde_json::json!({
                "name": station.name,
                "origin": station.origin,
            }))
            .collect::<Vec<_>>()),
    );
    serde_json::to_vec_pretty(&value).map_err(|error| error.to_string())
}

pub fn upsert_remote(
    book: &mut StationBook,
    name: Option<&str>,
    origin: &str,
) -> Result<String, String> {
    let origin = validate_gateway_url(origin)?;
    if origin == LOCAL_ORIGIN {
        return Err("That address is the Station on this computer.".into());
    }
    let explicit = name.map(str::trim).filter(|name| !name.is_empty());
    let name = match explicit {
        Some(name) => validate_station_name(name)?,
        None => origin_label(&origin),
    };
    if let Some(existing) = book
        .remotes
        .iter_mut()
        .find(|station| station.origin == origin)
    {
        if explicit.is_some() {
            existing.name = name;
        }
        return Ok(existing.origin.clone());
    }
    if book.remotes.len() >= MAX_SAVED_STATIONS {
        return Err("Too many Stations are saved.".into());
    }
    book.remotes.push(RemoteStation {
        name,
        origin: origin.clone(),
    });
    Ok(origin)
}

pub fn rename_station(book: &mut StationBook, id: &str, name: &str) -> Result<(), String> {
    let name = validate_station_name(name)?;
    if id == LOCAL_ID {
        book.local_name = name;
        return Ok(());
    }
    let station = book
        .remotes
        .iter_mut()
        .find(|station| station.origin == id)
        .ok_or_else(|| "That Station is not saved.".to_string())?;
    station.name = name;
    Ok(())
}

/// Removes a remote Station. The Station on this computer cannot be removed.
pub fn remove_remote(book: &mut StationBook, id: &str) -> Result<(), String> {
    if id == LOCAL_ID {
        return Err("The Station on this computer stays in the list.".into());
    }
    let before = book.remotes.len();
    book.remotes.retain(|station| station.origin != id);
    if book.remotes.len() == before {
        return Err("That Station is not saved.".into());
    }
    Ok(())
}

/// The Station a page shortcut names: slot 1 is this computer, then saved
/// remote Stations in the order the picker lists them. Only slots 1–9 exist.
pub fn slot_choice(book: &StationBook, slot: u32) -> Option<SavedGateway> {
    match slot {
        1 => Some(SavedGateway::Local),
        2..=9 => book
            .remotes
            .get(slot as usize - 2)
            .map(|station| SavedGateway::Remote {
                origin: station.origin.clone(),
            }),
        _ => None,
    }
}

pub fn station_name(book: &StationBook, choice: &SavedGateway) -> String {
    match choice {
        SavedGateway::Local => book.local_name.clone(),
        SavedGateway::Remote { origin } => book
            .remotes
            .iter()
            .find(|station| &station.origin == origin)
            .map(|station| station.name.clone())
            .unwrap_or_else(|| origin_label(origin)),
    }
}

/// What the window title, status bar, and tray call a gateway: "This
/// computer" for the bundled-local choice, or the remote's host and any
/// non-default port.
pub fn gateway_label(choice: &SavedGateway) -> String {
    match choice {
        SavedGateway::Local => "Local".into(),
        SavedGateway::Remote { origin } => origin_label(origin),
    }
}

pub fn origin_label(origin: &str) -> String {
    let Ok(url) = Url::parse(origin) else {
        return origin.to_string();
    };
    match (url.host_str(), url.port()) {
        (Some(host), Some(port)) => format!("{host}:{port}"),
        (Some(host), None) => host.to_string(),
        _ => origin.to_string(),
    }
}

pub fn validate_gateway_url(raw: &str) -> Result<String, String> {
    let raw = raw.trim();
    if raw.is_empty() {
        return Err("Enter a Station address.".into());
    }
    if raw.len() > 300 {
        return Err("That Station address is too long.".into());
    }
    // A host, or host:port, is enough. HTTPS is assumed. An explicit http://
    // is still accepted only where the rules below allow it.
    let candidate = if raw.contains("://") {
        raw.to_string()
    } else {
        format!("https://{raw}")
    };
    let url = Url::parse(&candidate)
        .map_err(|_| "Enter a Station address, such as spark.local:8080.".to_string())?;
    if !url.username().is_empty() || url.password().is_some() {
        return Err("The Station address cannot include a username or password.".into());
    }
    if url.query().is_some() || url.fragment().is_some() {
        return Err("The Station address cannot include a query or a fragment.".into());
    }
    let host = url
        .host_str()
        .ok_or_else(|| "Enter a Station host.".to_string())?;
    match url.scheme() {
        "https" => {}
        "http" if is_loopback_host(host) => {}
        "http" => {
            return Err(
                "Plain HTTP is only accepted for a loopback Station. A remote Station needs HTTPS."
                    .into(),
            );
        }
        _ => return Err("Enter an http or https Station address.".into()),
    }
    let origin = url.origin().ascii_serialization();
    if origin == "null" || origin.is_empty() {
        return Err("Enter a Station address.".into());
    }
    // A host typed without a scheme, or one that becomes punycode, can grow
    // past the raw-input check. The saved origin itself stays bounded.
    if origin.len() > MAX_ORIGIN_BYTES {
        return Err("That Station address is too long.".into());
    }
    Ok(origin)
}

pub fn is_loopback_host(host: &str) -> bool {
    let host = host.trim_matches(|character| character == '[' || character == ']');
    if host.eq_ignore_ascii_case("localhost") {
        return true;
    }
    host.parse::<IpAddr>().is_ok_and(|ip| ip.is_loopback())
}

/// The picker page. `tauri dev` builds embed no assets and serve the picker
/// from `devUrl` in tauri.conf.json, so a dev build loads it from there.
pub fn shell_index_url() -> &'static str {
    if cfg!(dev) {
        "http://127.0.0.1:1420/index.html"
    } else if cfg!(target_os = "macos") {
        "tauri://localhost/index.html"
    } else {
        "http://tauri.localhost/index.html"
    }
}

pub fn is_shell_url(url: &Url) -> bool {
    if matches!(url.scheme(), "tauri" | "about" | "asset") {
        return true;
    }
    if matches!(url.host_str(), Some("tauri.localhost")) {
        return true;
    }
    // Debug builds load the picker from the dev server in tauri.conf.json
    // instead of the embedded assets. Release builds do not.
    #[cfg(debug_assertions)]
    if url.port() == Some(1420) && url.host_str().is_some_and(is_loopback_host) {
        return true;
    }
    false
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum GatewayProbe {
    Down,
    Starting,
    Ready,
    Other,
}

pub fn probe_local() -> GatewayProbe {
    probe_port(LOCAL_PORT)
}

/// How many saved Stations one status pass checks at a time.
const PROBE_WORKERS: usize = 8;
/// One status pass ends here. A Station it did not reach stays unknown.
pub const PROBE_BUDGET: Duration = Duration::from_secs(3);
const PROBE_CONNECT: Duration = Duration::from_millis(400);
const PROBE_RESOLVE: Duration = Duration::from_secs(1);
/// Addresses tried per Station.
const PROBE_ADDRESSES: usize = 2;
/// The system resolver cannot be cancelled. A lookup that outlives
/// [`PROBE_RESOLVE`] is left to finish on its own thread; past this many
/// lookups in flight, a Station is reported unknown instead of starting
/// another one.
const MAX_LOOKUPS_IN_FLIGHT: usize = 16;
static LOOKUPS_IN_FLIGHT: AtomicUsize = AtomicUsize::new(0);

/// Checks every origin with a fixed pool of workers inside
/// [`PROBE_BUDGET`]. Returns `(origin, online)` for each Station it could
/// decide; one it could not reach in time, or whose lookup did not answer,
/// is left out and shows no status.
pub fn probe_stations(origins: &[String]) -> Vec<(String, bool)> {
    probe_stations_within(origins, PROBE_BUDGET)
}

fn probe_stations_within(origins: &[String], budget: Duration) -> Vec<(String, bool)> {
    let deadline = Instant::now() + budget;
    let next = AtomicUsize::new(0);
    let results: Mutex<Vec<Option<bool>>> = Mutex::new(vec![None; origins.len()]);
    std::thread::scope(|scope| {
        for _ in 0..PROBE_WORKERS.min(origins.len()) {
            scope.spawn(|| loop {
                let index = next.fetch_add(1, Ordering::Relaxed);
                if index >= origins.len() || Instant::now() >= deadline {
                    break;
                }
                let online = probe_online(&origins[index], deadline);
                if let Ok(mut results) = results.lock() {
                    results[index] = online;
                }
            });
        }
    });
    let results = results.into_inner().unwrap_or_default();
    origins
        .iter()
        .zip(results)
        .filter_map(|(origin, online)| online.map(|online| (origin.clone(), online)))
        .collect()
}

/// Whether a saved Station answers, or `None` when that could not be decided
/// before `deadline`. The bundled Station uses the readiness probe. Any other
/// saved origin is a short TCP connect, so a dot can update off the UI thread
/// without a new TLS stack. Offline does not block opening.
fn probe_online(origin: &str, deadline: Instant) -> Option<bool> {
    if origin == LOCAL_ORIGIN {
        return Some(matches!(
            probe_local(),
            GatewayProbe::Ready | GatewayProbe::Starting
        ));
    }
    let Ok(url) = Url::parse(origin) else {
        return Some(false);
    };
    let Some(host) = url.host_str() else {
        return Some(false);
    };
    let Some(port) = url.port_or_known_default() else {
        return Some(false);
    };
    let host = host.trim_matches(|character| character == '[' || character == ']');
    let remaining = deadline.saturating_duration_since(Instant::now());
    let addresses = resolve(host, port, remaining.min(PROBE_RESOLVE))?;
    for address in addresses.iter().take(PROBE_ADDRESSES) {
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            return None;
        }
        if TcpStream::connect_timeout(address, remaining.min(PROBE_CONNECT)).is_ok() {
            return Some(true);
        }
    }
    Some(false)
}

/// Resolves `host` within `timeout`. `None` means the lookup did not answer
/// in time, or too many lookups are already waiting; an empty list means the
/// name did not resolve.
fn resolve(host: &str, port: u16, timeout: Duration) -> Option<Vec<SocketAddr>> {
    if let Ok(ip) = host.parse::<IpAddr>() {
        return Some(vec![SocketAddr::new(ip, port)]);
    }
    if timeout.is_zero() {
        return None;
    }
    if LOOKUPS_IN_FLIGHT.fetch_add(1, Ordering::AcqRel) >= MAX_LOOKUPS_IN_FLIGHT {
        LOOKUPS_IN_FLIGHT.fetch_sub(1, Ordering::AcqRel);
        return None;
    }
    let (sender, receiver) = mpsc::channel();
    let name = host.to_string();
    let spawned = std::thread::Builder::new()
        .name("station-lookup".into())
        .spawn(move || {
            let addresses = (name.as_str(), port)
                .to_socket_addrs()
                .map(|addresses| addresses.take(PROBE_ADDRESSES).collect::<Vec<_>>())
                .unwrap_or_default();
            LOOKUPS_IN_FLIGHT.fetch_sub(1, Ordering::AcqRel);
            let _ = sender.send(addresses);
        });
    if spawned.is_err() {
        LOOKUPS_IN_FLIGHT.fetch_sub(1, Ordering::AcqRel);
        return None;
    }
    receiver.recv_timeout(timeout).ok()
}

pub fn probe_port(port: u16) -> GatewayProbe {
    match request(port, "/readyz") {
        Hit::Down => match request(port, "/healthz") {
            Hit::Ready | Hit::Starting => GatewayProbe::Starting,
            Hit::Other => GatewayProbe::Other,
            Hit::Down => GatewayProbe::Down,
        },
        Hit::Ready => GatewayProbe::Ready,
        Hit::Starting => GatewayProbe::Starting,
        Hit::Other => GatewayProbe::Other,
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Hit {
    Down,
    Starting,
    Ready,
    Other,
}

fn request(port: u16, path: &str) -> Hit {
    let address = SocketAddr::from((Ipv4Addr::LOCALHOST, port));
    let mut stream = match TcpStream::connect_timeout(&address, Duration::from_millis(400)) {
        Ok(stream) => stream,
        Err(_) => return Hit::Down,
    };
    let _ = stream.set_read_timeout(Some(Duration::from_millis(800)));
    let _ = stream.set_write_timeout(Some(Duration::from_millis(400)));
    let message = format!(
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\nAccept: application/json\r\n\r\n"
    );
    if stream.write_all(message.as_bytes()).is_err() {
        return Hit::Down;
    }
    let mut buffer = Vec::new();
    let mut chunk = [0_u8; 1024];
    loop {
        if buffer.len() > 8192 {
            break;
        }
        match stream.read(&mut chunk) {
            Ok(0) => break,
            Ok(count) => buffer.extend_from_slice(&chunk[..count]),
            Err(error)
                if error.kind() == std::io::ErrorKind::WouldBlock
                    || error.kind() == std::io::ErrorKind::TimedOut =>
            {
                break;
            }
            Err(_) => break,
        }
    }
    classify(&buffer)
}

fn classify(bytes: &[u8]) -> Hit {
    let text = String::from_utf8_lossy(bytes);
    let Some((head, body)) = text.split_once("\r\n\r\n") else {
        return Hit::Other;
    };
    let status: u16 = head
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .and_then(|code| code.parse().ok())
        .unwrap_or(0);
    let probe: ProbeBody = match serde_json::from_str(body.trim()) {
        Ok(probe) => probe,
        Err(_) => return Hit::Other,
    };
    match (status, probe.status.as_str()) {
        (200, "ok") => Hit::Ready,
        (503, "not_ready") => Hit::Starting,
        _ => Hit::Other,
    }
}

#[derive(Debug, Deserialize)]
struct ProbeBody {
    status: String,
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use std::net::TcpListener;
    use std::thread;

    #[test]
    fn accepts_https_and_loopback_http_only() {
        assert_eq!(
            validate_gateway_url("spark.local:8080").unwrap(),
            "https://spark.local:8080"
        );
        assert_eq!(
            validate_gateway_url("10.0.0.5:8080").unwrap(),
            "https://10.0.0.5:8080"
        );
        assert_eq!(
            validate_gateway_url("https://meridian.example.com/ui/").unwrap(),
            "https://meridian.example.com"
        );
        assert_eq!(
            validate_gateway_url("https://gw.example.com:8443").unwrap(),
            "https://gw.example.com:8443"
        );
        assert_eq!(
            validate_gateway_url("http://127.0.0.1:9090/ui").unwrap(),
            "http://127.0.0.1:9090"
        );
        assert_eq!(
            validate_gateway_url("http://localhost:8080").unwrap(),
            "http://localhost:8080"
        );
        assert_eq!(
            validate_gateway_url("http://[::1]:8080").unwrap(),
            "http://[::1]:8080"
        );
        assert!(validate_gateway_url("http://meridian.example.com").is_err());
        assert!(validate_gateway_url("https://user:secret@example.com").is_err());
        assert!(validate_gateway_url("https://example.com/ui/?token=secret").is_err());
        assert!(validate_gateway_url("ftp://example.com").is_err());
        assert!(validate_gateway_url("   ").is_err());
    }

    #[test]
    fn saved_file_drops_a_tampered_origin() {
        assert_eq!(
            parse_saved(br#"{"mode":"local"}"#),
            Some(SavedGateway::Local)
        );
        assert_eq!(
            parse_saved(br#"{"mode":"remote","origin":"https://gw.example.com/ui/"}"#),
            Some(SavedGateway::Remote {
                origin: "https://gw.example.com".into()
            })
        );
        assert_eq!(
            parse_saved(br#"{"mode":"remote","origin":"http://evil.example"}"#),
            None
        );
        assert_eq!(SavedGateway::Local.ui_url(), "http://127.0.0.1:8080/ui/");
    }

    #[test]
    fn recent_origins_migrate_to_named_stations() {
        let file = br#"{"mode":"local","recent":[
            "https://a.example.com/ui/",
            "http://evil.example",
            "https://user:pw@b.example.com",
            "https://c.example.com/?token=secret",
            "https://a.example.com",
            "https://d.example.com:8443",
            "http://127.0.0.1:18090",
            "https://e.example.com",
            "https://f.example.com",
            "https://g.example.com"
        ]}"#;
        let loaded = load_stations(file);
        assert_eq!(loaded.choice, Some(SavedGateway::Local));
        assert!(loaded.migrated);
        assert_eq!(
            loaded
                .book
                .remotes
                .iter()
                .map(|station| (station.name.as_str(), station.origin.as_str()))
                .collect::<Vec<_>>(),
            vec![
                ("a.example.com", "https://a.example.com"),
                ("d.example.com:8443", "https://d.example.com:8443"),
                ("127.0.0.1:18090", "http://127.0.0.1:18090"),
                ("e.example.com", "https://e.example.com"),
                ("f.example.com", "https://f.example.com"),
                ("g.example.com", "https://g.example.com"),
            ]
        );
        let empty = load_stations(br#"{"mode":"local"}"#);
        assert!(!empty.migrated);
        assert!(empty.book.remotes.is_empty());
        assert!(load_stations(b"not json").choice.is_none());
    }

    #[test]
    fn stations_add_rename_remove_and_keep_the_local_one() {
        let mut book = StationBook::default();
        let spark = upsert_remote(&mut book, Some("Spark"), "spark.local:8080").unwrap();
        let work = upsert_remote(&mut book, None, "10.0.0.5:8080").unwrap();
        upsert_remote(&mut book, Some("Spark desk"), "spark.local:8080").unwrap();
        assert_eq!(book.remotes.len(), 2);
        assert_eq!(book.remotes[0].name, "Spark desk");
        assert_eq!(book.remotes[1].name, "10.0.0.5:8080");
        rename_station(&mut book, LOCAL_ID, "  Studio  ").unwrap();
        assert_eq!(book.local_name, "Studio");
        rename_station(&mut book, &work, "Work").unwrap();
        assert_eq!(book.remotes[1].name, "Work");
        assert!(remove_remote(&mut book, LOCAL_ID).is_err());
        remove_remote(&mut book, &spark).unwrap();
        assert_eq!(book.remotes.len(), 1);
        assert!(validate_station_name("").is_err());
        assert!(validate_station_name("line\nbreak").is_err());
        assert!(upsert_remote(&mut book, None, "http://evil.example").is_err());
        assert!(upsert_remote(&mut book, Some("x"), "https://user:pw@b.example.com").is_err());
    }

    #[test]
    fn saved_file_round_trips_names_and_origins_only() {
        let choice = SavedGateway::Remote {
            origin: "https://spark.local:8080".into(),
        };
        let book = StationBook {
            local_name: "This Mac".into(),
            remotes: vec![RemoteStation {
                name: "Spark".into(),
                origin: "https://spark.local:8080".into(),
            }],
        };
        let bytes = encode_saved(Some(&choice), &book).unwrap();
        let loaded = load_stations(&bytes);
        assert_eq!(loaded.choice, Some(choice));
        assert!(!loaded.migrated);
        assert_eq!(loaded.book, book);
        let text = String::from_utf8(bytes).unwrap();
        assert!(!text.contains("token"));
        assert!(!text.contains("recent"));
    }

    #[test]
    fn page_shortcuts_reach_only_the_first_nine_saved_stations() {
        let mut book = StationBook::default();
        for index in 0..10 {
            upsert_remote(&mut book, None, &format!("s{index}.example:8443")).unwrap();
        }
        assert_eq!(slot_choice(&book, 1), Some(SavedGateway::Local));
        assert_eq!(
            slot_choice(&book, 2),
            Some(SavedGateway::Remote {
                origin: "https://s0.example:8443".into()
            })
        );
        assert_eq!(
            slot_choice(&book, 9),
            Some(SavedGateway::Remote {
                origin: "https://s7.example:8443".into()
            })
        );
        assert_eq!(slot_choice(&book, 0), None);
        assert_eq!(slot_choice(&book, 10), None);
        assert_eq!(slot_choice(&StationBook::default(), 2), None);
    }

    #[test]
    fn saved_origins_stay_within_the_page_limit() {
        let host = format!(
            "{}.example",
            "a".repeat(MAX_ORIGIN_BYTES - "https://.example".len())
        );
        assert!(validate_gateway_url(&host).is_ok());
        let longer = format!("b{host}");
        assert!(longer.len() <= 300);
        assert_eq!(
            validate_gateway_url(&longer).unwrap_err(),
            "That Station address is too long."
        );
    }

    #[test]
    fn status_pass_is_bounded_and_reports_what_it_decided() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let online = format!("http://127.0.0.1:{}", listener.local_addr().unwrap().port());
        let closed = {
            let spare = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            format!("http://127.0.0.1:{}", spare.local_addr().unwrap().port())
        };
        let mut origins = vec![online.clone(), closed.clone()];
        origins.extend((0..MAX_SAVED_STATIONS).map(|_| closed.clone()));
        let started = Instant::now();
        let statuses = probe_stations_within(&origins, Duration::from_secs(2));
        assert!(
            started.elapsed() < Duration::from_secs(4),
            "{:?}",
            started.elapsed()
        );
        assert!(statuses.contains(&(online, true)));
        assert!(statuses.contains(&(closed, false)));
        assert!(statuses.len() <= origins.len());
        let none = probe_stations_within(&origins, Duration::ZERO);
        assert!(none.is_empty());
    }

    #[test]
    fn more_than_five_stations_all_persist() {
        let mut book = StationBook::default();
        for index in 0..8 {
            upsert_remote(
                &mut book,
                Some(&format!("Station {index}")),
                &format!("s{index}.example:8443"),
            )
            .unwrap();
        }
        let bytes = encode_saved(Some(&SavedGateway::Local), &book).unwrap();
        let loaded = load_stations(&bytes);
        assert_eq!(loaded.book.remotes.len(), 8);
        assert_eq!(loaded.book.remotes[7].name, "Station 7");
    }

    #[test]
    fn labels_name_the_host() {
        assert_eq!(gateway_label(&SavedGateway::Local), "Local");
        for (origin, label) in [
            ("https://meridian.example.com", "meridian.example.com"),
            ("https://gw.example.com:8443", "gw.example.com:8443"),
            ("http://127.0.0.1:18090", "127.0.0.1:18090"),
            ("http://[::1]:9090", "[::1]:9090"),
        ] {
            assert_eq!(
                gateway_label(&SavedGateway::Remote {
                    origin: origin.into()
                }),
                label
            );
        }
    }

    #[test]
    fn picker_url_is_a_shell_url() {
        let url = Url::parse(shell_index_url()).unwrap();
        assert!(is_shell_url(&url));
        if cfg!(dev) {
            assert_eq!(url.as_str(), "http://127.0.0.1:1420/index.html");
            let config: serde_json::Value =
                serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
            assert_eq!(config["build"]["devUrl"], "http://127.0.0.1:1420");
        }
    }

    #[test]
    fn dev_server_counts_as_the_shell_while_debugging() {
        let url = Url::parse("http://127.0.0.1:1420/index.html").unwrap();
        assert_eq!(cfg!(debug_assertions), is_shell_url(&url));
        assert!(is_shell_url(
            &Url::parse("http://tauri.localhost/index.html").unwrap()
        ));
        assert!(!is_shell_url(
            &Url::parse("https://example.com/ui/").unwrap()
        ));
    }

    #[test]
    fn probe_recognizes_meridian_and_ignores_other_servers() {
        assert_eq!(
            probe_port(listen_status(200, r#"{"status":"ok"}"#)),
            GatewayProbe::Ready
        );
        assert_eq!(
            probe_port(listen_status(503, r#"{"status":"not_ready"}"#)),
            GatewayProbe::Starting
        );
        assert_eq!(probe_port(listen_status(200, "ok")), GatewayProbe::Other);
        assert_eq!(probe_port(closed_port()), GatewayProbe::Down);
    }

    fn listen_status(status: u16, body: &str) -> u16 {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let body = body.to_string();
        thread::spawn(move || {
            let mut stream = listener.accept().unwrap().0;
            let response = format!(
                "HTTP/1.1 {status} TEST\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
            let _ = stream.write_all(response.as_bytes());
        });
        port
    }

    fn closed_port() -> u16 {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        drop(listener);
        port
    }
}
