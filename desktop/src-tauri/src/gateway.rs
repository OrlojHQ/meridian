//! Gateway choice. The shell remembers a mode and, for another gateway, an origin,
//! plus the origins of a few recently used gateways. It never stores an
//! installation token, a session, or anything but those origins.

use std::io::{Read, Write};
use std::net::{IpAddr, Ipv4Addr, SocketAddr, TcpStream};
use std::time::Duration;

use serde::{Deserialize, Serialize};
use url::Url;

pub const LOCAL_ORIGIN: &str = "http://127.0.0.1:8080";
pub const LOCAL_LISTEN: &str = "127.0.0.1:8080";
const LOCAL_PORT: u16 = 8080;

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

/// How many other gateways the picker and tray offer to switch back to.
pub const MAX_RECENT: usize = 5;

/// The recently used gateway origins in a saved file, most recent first.
/// An entry that no longer validates is dropped, so a tampered file cannot
/// add a plain-HTTP remote or an address with a token in it.
pub fn parse_recent(bytes: &[u8]) -> Vec<String> {
    #[derive(Deserialize)]
    struct File {
        #[serde(default)]
        recent: Vec<String>,
    }
    let Ok(file) = serde_json::from_slice::<File>(bytes) else {
        return Vec::new();
    };
    let mut recent = Vec::new();
    for raw in file.recent.iter().take(MAX_RECENT * 4) {
        if let Ok(origin) = validate_gateway_url(raw) {
            if !recent.contains(&origin) {
                recent.push(origin);
            }
        }
    }
    recent.truncate(MAX_RECENT);
    recent
}

/// `recent` with `origin` moved to the front.
pub fn remember(recent: &[String], origin: &str) -> Vec<String> {
    let mut next = vec![origin.to_string()];
    next.extend(recent.iter().filter(|kept| *kept != origin).cloned());
    next.truncate(MAX_RECENT);
    next
}

/// `recent` without `origin`.
pub fn forget(recent: &[String], origin: &str) -> Vec<String> {
    recent
        .iter()
        .filter(|kept| *kept != origin)
        .cloned()
        .collect()
}

/// The saved file: the choice, plus the recent origins.
pub fn encode_saved(choice: &SavedGateway, recent: &[String]) -> Result<Vec<u8>, String> {
    let mut value = serde_json::to_value(choice).map_err(|error| error.to_string())?;
    if let Some(object) = value.as_object_mut() {
        object.insert("recent".into(), serde_json::json!(recent));
    }
    serde_json::to_vec_pretty(&value).map_err(|error| error.to_string())
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
    let url = Url::parse(raw).map_err(|_| "Enter an http or https Station address.".to_string())?;
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
    Ok(origin)
}

pub fn is_loopback_host(host: &str) -> bool {
    let host = host.trim_matches(|character| character == '[' || character == ']');
    if host.eq_ignore_ascii_case("localhost") {
        return true;
    }
    host.parse::<IpAddr>().is_ok_and(|ip| ip.is_loopback())
}

pub fn shell_index_url() -> &'static str {
    if cfg!(target_os = "macos") {
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
    fn recent_gateways_are_validated_origins_only() {
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
        assert_eq!(parse_saved(file), Some(SavedGateway::Local));
        assert_eq!(
            parse_recent(file),
            vec![
                "https://a.example.com",
                "https://d.example.com:8443",
                "http://127.0.0.1:18090",
                "https://e.example.com",
                "https://f.example.com",
            ]
        );
        assert!(parse_recent(br#"{"mode":"local"}"#).is_empty());
        assert!(parse_recent(b"not json").is_empty());
        assert!(parse_recent(br#"{"recent":"https://a.example.com"}"#).is_empty());
    }

    #[test]
    fn remember_moves_to_front_and_caps() {
        let recent: Vec<String> = ["https://a.test", "https://b.test", "https://c.test"]
            .map(String::from)
            .to_vec();
        assert_eq!(
            remember(&recent, "https://c.test"),
            vec!["https://c.test", "https://a.test", "https://b.test"]
        );
        let mut many = recent.clone();
        for origin in ["https://d.test", "https://e.test", "https://f.test"] {
            many = remember(&many, origin);
        }
        assert_eq!(many.len(), MAX_RECENT);
        assert_eq!(many[0], "https://f.test");
        assert_eq!(
            forget(&recent, "https://b.test"),
            vec!["https://a.test", "https://c.test"]
        );
    }

    #[test]
    fn saved_file_round_trips_choice_and_recent() {
        let choice = SavedGateway::Remote {
            origin: "https://gw.example.com".into(),
        };
        let recent = vec!["https://gw.example.com".to_string()];
        let bytes = encode_saved(&choice, &recent).unwrap();
        assert_eq!(parse_saved(&bytes), Some(choice));
        assert_eq!(parse_recent(&bytes), recent);
        let text = String::from_utf8(bytes).unwrap();
        assert!(!text.contains("token"));
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
