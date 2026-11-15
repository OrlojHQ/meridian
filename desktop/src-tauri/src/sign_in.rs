//! Team-mode sign-in through the system browser (ADR 0047).
//!
//! The webview never loads the identity provider. When a gateway page
//! navigates to `/auth/oidc/start`, the shell cancels that navigation, opens
//! the same path in the system browser with three extra parameters, and
//! waits on a one-shot listener on 127.0.0.1:
//! - `desktopPort`: the listener's port.
//! - `desktopState`: random; the listener ignores any request without it.
//! - `desktopChallenge`: the SHA-256 of a random verifier that never leaves
//!   this process until redemption.
//!
//! After the issuer verifies the member, the gateway redirects the system
//! browser to the listener with the state and a single-use ticket. The shell
//! then has the gateway's own page in the webview POST the ticket and the
//! verifier to `/auth/desktop/redeem`, and the session cookie lands in the
//! webview. A ticket seen in the browser's history is useless without the
//! verifier, and a ticket the gateway sent for someone else's sign-in reaches
//! that person's loopback address, not this one.

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::time::{Duration, Instant};

use sha2::{Digest, Sha256};
use url::Url;

/// The gateway path a page navigates to when its Sign in is clicked.
pub const START_PATH: &str = "/auth/oidc/start";
/// The path the gateway redirects the system browser to on 127.0.0.1.
pub const CALLBACK_PATH: &str = "/meridian-sign-in";
/// How long the listener waits for the browser; the gateway's own sign-in
/// attempt lasts as long.
pub const TIMEOUT: Duration = Duration::from_secs(10 * 60);

const MAX_REQUEST_HEAD: usize = 8 * 1024;
const READ_TIMEOUT: Duration = Duration::from_secs(2);
const POLL_INTERVAL: Duration = Duration::from_millis(100);

/// The shell's secrets for one sign-in.
pub struct Handoff {
    pub state: String,
    pub verifier: String,
}

impl Handoff {
    pub fn generate() -> Result<Self, String> {
        Ok(Self {
            state: random_value()?,
            verifier: random_value()?,
        })
    }

    /// The base64url SHA-256 of the verifier, as PKCE's S256 method computes
    /// it (RFC 7636 section 4.2).
    pub fn challenge(&self) -> String {
        challenge_for(&self.verifier)
    }
}

fn challenge_for(verifier: &str) -> String {
    base64url(&Sha256::digest(verifier.as_bytes()))
}

fn random_value() -> Result<String, String> {
    let mut bytes = [0u8; 32];
    getrandom::fill(&mut bytes).map_err(|_| "No randomness for sign-in.".to_string())?;
    Ok(base64url(&bytes))
}

/// Base64url without padding (RFC 4648 section 5).
fn base64url(bytes: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    let mut encoded = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        let value = (u32::from(chunk[0]) << 16)
            | (u32::from(*chunk.get(1).unwrap_or(&0)) << 8)
            | u32::from(*chunk.get(2).unwrap_or(&0));
        let symbols = chunk.len() + 1;
        for index in 0..symbols {
            let shift = 18 - 6 * index;
            encoded.push(ALPHABET[((value >> shift) & 0x3f) as usize] as char);
        }
    }
    encoded
}

fn is_base64url(value: &str) -> bool {
    value
        .bytes()
        .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-' || byte == b'_')
}

/// Reports a navigation to the active gateway's sign-in start. Any query is
/// ignored: the shell builds its own.
pub fn is_sign_in_start(url: &Url, origin: &str) -> bool {
    matches!(url.scheme(), "http" | "https")
        && url.origin().ascii_serialization() == origin
        && url.path() == START_PATH
}

/// The URL the system browser opens: the gateway's sign-in start with the
/// shell's parameters. `origin` is a serialized origin with no path.
pub fn start_url(origin: &str, port: u16, handoff: &Handoff) -> Result<Url, String> {
    let rejected = || "The Station address cannot start a sign-in.".to_string();
    let mut url = Url::parse(origin).map_err(|_| rejected())?;
    if url.origin().ascii_serialization() != origin || url.path() != "/" {
        return Err(rejected());
    }
    url.set_path(START_PATH);
    url.query_pairs_mut()
        .append_pair("desktopPort", &port.to_string())
        .append_pair("desktopState", &handoff.state)
        .append_pair("desktopChallenge", &handoff.challenge());
    Ok(url)
}

/// Reads the ticket from one HTTP request head, if it is the gateway's
/// redirect for this sign-in: `GET /meridian-sign-in` with exactly one
/// matching `state` and exactly one well-formed `ticket`.
pub fn ticket_from_request(head: &str, state: &str) -> Option<String> {
    let line = head.split("\r\n").next()?;
    let mut parts = line.split(' ');
    let (method, target, version) = (parts.next()?, parts.next()?, parts.next()?);
    if method != "GET" || !version.starts_with("HTTP/1.") || parts.next().is_some() {
        return None;
    }
    if !target.starts_with('/') {
        return None;
    }
    let url = Url::parse(&format!("http://127.0.0.1{target}")).ok()?;
    if url.path() != CALLBACK_PATH {
        return None;
    }
    let mut states = Vec::new();
    let mut tickets = Vec::new();
    for (name, value) in url.query_pairs() {
        match name.as_ref() {
            "state" => states.push(value.into_owned()),
            "ticket" => tickets.push(value.into_owned()),
            _ => {}
        }
    }
    if states.len() != 1 || tickets.len() != 1 || states[0] != state {
        return None;
    }
    let ticket = tickets.pop()?;
    if ticket.len() < 16 || ticket.len() > 128 || !is_base64url(&ticket) {
        return None;
    }
    Some(ticket)
}

/// Waits for the gateway's redirect. Requests that are not it are answered
/// and ignored, so a page that probes loopback ports cannot end the wait.
/// Returns `None` at the deadline or once `cancelled` reports true.
pub fn wait_for_ticket(
    listener: &TcpListener,
    state: &str,
    deadline: Instant,
    cancelled: &dyn Fn() -> bool,
) -> Option<String> {
    if listener.set_nonblocking(true).is_err() {
        return None;
    }
    loop {
        if cancelled() || Instant::now() >= deadline {
            return None;
        }
        match listener.accept() {
            Ok((stream, _)) => {
                if let Some(ticket) = answer(stream, state) {
                    return Some(ticket);
                }
            }
            Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                std::thread::sleep(POLL_INTERVAL);
            }
            Err(_) => std::thread::sleep(POLL_INTERVAL),
        }
    }
}

fn answer(mut stream: TcpStream, state: &str) -> Option<String> {
    let _ = stream.set_nonblocking(false);
    let _ = stream.set_read_timeout(Some(READ_TIMEOUT));
    let _ = stream.set_write_timeout(Some(READ_TIMEOUT));
    let head = read_head(&mut stream)?;
    let ticket = ticket_from_request(&head, state);
    let (status, message) = if ticket.is_some() {
        (
            "200 OK",
            "You are signed in. Return to the Meridian app; you can close this tab.",
        )
    } else {
        ("404 Not Found", "This is not a Meridian sign-in.")
    };
    let body = format!(
        "<!DOCTYPE html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">\
<meta name=\"referrer\" content=\"no-referrer\"><title>Meridian sign-in</title></head>\
<body><p>{message}</p></body></html>"
    );
    let response = format!(
        "HTTP/1.1 {status}\r\nContent-Type: text/html; charset=utf-8\r\n\
Content-Length: {}\r\nCache-Control: no-store\r\nReferrer-Policy: no-referrer\r\n\
X-Content-Type-Options: nosniff\r\n\
Content-Security-Policy: default-src 'none'; base-uri 'none'; form-action 'none'\r\n\
Connection: close\r\n\r\n{body}",
        body.len()
    );
    let _ = stream.write_all(response.as_bytes());
    let _ = stream.flush();
    ticket
}

fn read_head(stream: &mut TcpStream) -> Option<String> {
    let mut head = Vec::with_capacity(1024);
    let mut buffer = [0u8; 1024];
    while !head.windows(4).any(|window| window == b"\r\n\r\n") {
        if head.len() >= MAX_REQUEST_HEAD {
            return None;
        }
        let read = stream.read(&mut buffer).ok()?;
        if read == 0 {
            break;
        }
        head.extend_from_slice(&buffer[..read]);
    }
    String::from_utf8(head).ok()
}

/// The script the shell runs in the gateway's page to redeem the ticket.
/// It does nothing unless the page is still on `origin`, and it only follows
/// a `/ui/` path the gateway names.
pub fn redeem_script(origin: &str, ticket: &str, verifier: &str) -> String {
    let origin = serde_json::to_string(origin).unwrap_or_default();
    let body = serde_json::json!({ "ticket": ticket, "verifier": verifier }).to_string();
    let body = serde_json::to_string(&body).unwrap_or_default();
    format!(
        r#"(() => {{
  const origin = {origin};
  if (window.location.origin !== origin) return;
  const done = (next) => window.location.assign(
    typeof next === "string" && next.startsWith("/ui/") ? next : "/ui/");
  fetch(origin + "/auth/desktop/redeem", {{
    method: "POST",
    credentials: "same-origin",
    cache: "no-store",
    headers: {{ "Content-Type": "application/json" }},
    body: {body},
  }})
    .then((response) => (response.ok ? response.json() : null))
    .then((result) => done(result && result.next))
    .catch(() => done(null));
}})();"#
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::Arc;

    #[test]
    fn challenge_matches_pkce_s256() {
        // RFC 7636 appendix B.
        assert_eq!(
            challenge_for("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"),
            "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
        );
        assert_eq!(base64url(b""), "");
        assert_eq!(base64url(b"f"), "Zg");
        assert_eq!(base64url(b"fo"), "Zm8");
        assert_eq!(base64url(b"foo"), "Zm9v");
        assert_eq!(base64url(&[0xfb, 0xff]), "-_8");
    }

    #[test]
    fn generated_values_are_32_random_bytes() {
        let first = Handoff::generate().unwrap();
        let second = Handoff::generate().unwrap();
        for value in [&first.state, &first.verifier, &second.state] {
            assert_eq!(value.len(), 43);
            assert!(is_base64url(value));
        }
        assert_ne!(first.state, first.verifier);
        assert_ne!(first.verifier, second.verifier);
        assert_eq!(first.challenge().len(), 43);
    }

    #[test]
    fn only_the_active_gateways_start_is_intercepted() {
        let origin = "https://meridian.example";
        let yes = |url: &str| is_sign_in_start(&Url::parse(url).unwrap(), origin);
        assert!(yes("https://meridian.example/auth/oidc/start"));
        assert!(yes(
            "https://meridian.example/auth/oidc/start?desktopPort=1"
        ));
        assert!(!yes("https://meridian.example/auth/oidc/callback"));
        assert!(!yes("https://meridian.example/auth/oidc/start/x"));
        assert!(!yes("https://meridian.example/ui/"));
        assert!(!yes("https://other.example/auth/oidc/start"));
        assert!(!yes("http://meridian.example/auth/oidc/start"));
        assert!(!yes("https://meridian.example:8443/auth/oidc/start"));
    }

    #[test]
    fn start_url_carries_only_the_challenge() {
        let handoff = Handoff::generate().unwrap();
        let url = start_url("https://meridian.example", 51234, &handoff).unwrap();
        assert_eq!(
            url.origin().ascii_serialization(),
            "https://meridian.example"
        );
        assert_eq!(url.path(), START_PATH);
        let pairs: Vec<(String, String)> = url.query_pairs().into_owned().collect();
        assert_eq!(
            pairs,
            vec![
                ("desktopPort".into(), "51234".into()),
                ("desktopState".into(), handoff.state.clone()),
                ("desktopChallenge".into(), handoff.challenge()),
            ]
        );
        assert!(!url.as_str().contains(&handoff.verifier));
        assert!(start_url("https://meridian.example/ui", 51234, &handoff).is_err());
        assert!(start_url("not a url", 51234, &handoff).is_err());
    }

    #[test]
    fn only_the_matching_redirect_yields_a_ticket() {
        let state = "s".repeat(43);
        let ticket = "t".repeat(43);
        let request = |target: &str| format!("GET {target} HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n");
        let good = format!("/meridian-sign-in?state={state}&ticket={ticket}");
        assert_eq!(
            ticket_from_request(&request(&good), &state),
            Some(ticket.clone())
        );
        for bad in [
            format!("/meridian-sign-in?state=other&ticket={ticket}"),
            format!("/meridian-sign-in?ticket={ticket}"),
            format!("/meridian-sign-in?state={state}"),
            format!("/meridian-sign-in?state={state}&ticket={ticket}&ticket={ticket}"),
            format!("/meridian-sign-in?state={state}&state={state}&ticket={ticket}"),
            format!("/meridian-sign-in?state={state}&ticket=%22%3E%3Cscript%3E"),
            format!("/meridian-sign-in?state={state}&ticket=short"),
            format!("/favicon.ico?state={state}&ticket={ticket}"),
            format!("//evil.example/meridian-sign-in?state={state}&ticket={ticket}"),
        ] {
            assert_eq!(ticket_from_request(&request(&bad), &state), None, "{bad}");
        }
        let post = format!("POST {good} HTTP/1.1\r\n\r\n");
        assert_eq!(ticket_from_request(&post, &state), None);
        assert_eq!(ticket_from_request("garbage", &state), None);
    }

    fn get(port: u16, target: &str) -> String {
        let mut stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
        write!(stream, "GET {target} HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n").unwrap();
        let mut response = String::new();
        stream.read_to_string(&mut response).unwrap();
        response
    }

    #[test]
    fn listener_ignores_strangers_and_returns_the_ticket() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let port = listener.local_addr().unwrap().port();
        let state = "s".repeat(43);
        let ticket = "t".repeat(43);
        let expected = state.clone();
        let waiter = std::thread::spawn(move || {
            wait_for_ticket(
                &listener,
                &expected,
                Instant::now() + Duration::from_secs(10),
                &|| false,
            )
        });
        let stranger = get(
            port,
            &format!("/meridian-sign-in?state=wrong&ticket={ticket}"),
        );
        assert!(stranger.starts_with("HTTP/1.1 404"));
        assert!(stranger.contains("Cache-Control: no-store"));
        let probe = get(port, "/favicon.ico");
        assert!(probe.starts_with("HTTP/1.1 404"));
        let redirect = get(
            port,
            &format!("/meridian-sign-in?state={state}&ticket={ticket}"),
        );
        assert!(redirect.starts_with("HTTP/1.1 200"));
        assert!(redirect.contains("Return to the Meridian app"));
        assert!(!redirect.contains(&ticket));
        assert_eq!(waiter.join().unwrap(), Some(ticket));
    }

    #[test]
    fn listener_stops_when_cancelled_or_late() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let cancelled = Arc::new(AtomicBool::new(false));
        let flag = cancelled.clone();
        let waiter = std::thread::spawn(move || {
            wait_for_ticket(
                &listener,
                "state",
                Instant::now() + Duration::from_secs(30),
                &|| flag.load(Ordering::SeqCst),
            )
        });
        cancelled.store(true, Ordering::SeqCst);
        assert_eq!(waiter.join().unwrap(), None);

        let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
        let started = Instant::now();
        assert_eq!(
            wait_for_ticket(
                &listener,
                "state",
                Instant::now() + Duration::from_millis(250),
                &|| false
            ),
            None
        );
        assert!(started.elapsed() < Duration::from_secs(5));
    }

    #[test]
    fn redeem_script_is_bound_to_the_origin_and_quotes_its_values() {
        let script = redeem_script("https://meridian.example", "ti\"cket", "veri</script>");
        assert!(script.contains(r#"const origin = "https://meridian.example";"#));
        assert!(script.contains("if (window.location.origin !== origin) return;"));
        assert!(script.contains(r#"origin + "/auth/desktop/redeem""#));
        assert!(script.contains(r#"next.startsWith("/ui/")"#));
        // The body is one JSON string literal holding the JSON document.
        let body = script
            .lines()
            .find_map(|line| line.trim().strip_prefix("body: "))
            .unwrap()
            .trim_end_matches(',');
        let document: String = serde_json::from_str(body).unwrap();
        let value: serde_json::Value = serde_json::from_str(&document).unwrap();
        assert_eq!(value["ticket"], "ti\"cket");
        assert_eq!(value["verifier"], "veri</script>");
    }
}
