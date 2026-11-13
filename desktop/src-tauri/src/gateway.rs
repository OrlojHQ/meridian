//! Gateway choice. The shell remembers a mode and, for another gateway, an origin.
//! It never stores an installation token.

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

pub fn validate_gateway_url(raw: &str) -> Result<String, String> {
    let raw = raw.trim();
    if raw.is_empty() {
        return Err("Enter a gateway address.".into());
    }
    if raw.len() > 300 {
        return Err("That gateway address is too long.".into());
    }
    let url = Url::parse(raw).map_err(|_| "Enter an http or https gateway address.".to_string())?;
    if !url.username().is_empty() || url.password().is_some() {
        return Err("The gateway address cannot include a username or password.".into());
    }
    if url.query().is_some() || url.fragment().is_some() {
        return Err("The gateway address cannot include a query or a fragment.".into());
    }
    let host = url
        .host_str()
        .ok_or_else(|| "Enter a gateway host.".to_string())?;
    match url.scheme() {
        "https" => {}
        "http" if is_loopback_host(host) => {}
        "http" => {
            return Err(
                "Plain HTTP is only accepted for a loopback gateway. A remote gateway needs HTTPS."
                    .into(),
            );
        }
        _ => return Err("Enter an http or https gateway address.".into()),
    }
    let origin = url.origin().ascii_serialization();
    if origin == "null" || origin.is_empty() {
        return Err("Enter a gateway address.".into());
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
