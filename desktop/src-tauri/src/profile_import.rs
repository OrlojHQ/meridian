//! Importing this computer's setup from the desktop app (ADR 0049).
//!
//! Desktop users never run a command. The import window is a shell-owned
//! page (`ui/import.html`) with its own local capability; it is never
//! granted the gateway bridge and cannot load a remote URL. Detection,
//! sanitizing, the diff, and the upload all happen in the bundled
//! `meridian-desktop-gateway` binary, which is `meridiand`, run as the
//! hidden `desktop-import` client mode. This module only starts that mode,
//! hands it the main window's session for the bound gateway origin on stdin,
//! relays its JSON lines to the window, and records change-check baselines.
//!
//! The gateway page can only ask for the window to open. It is never told
//! what the app found.

use std::collections::BTreeMap;
use std::time::{Duration, SystemTime};

use serde::{Deserialize, Serialize};
use url::{Host, Url};

/// The import window's label.
pub const WINDOW: &str = "import";

/// How often the change check runs while the app is open.
pub const CHECK_INTERVAL: Duration = Duration::from_secs(30 * 60);

/// The longest line the mode may send; larger lines are dropped.
pub const MAX_LINE: usize = 4 << 20;

/// The only notification text the change check shows. It names no harness,
/// file, or count, so a lock screen or notification history reveals nothing.
pub const CHANGED_TITLE: &str = "Meridian";
pub const CHANGED_BODY: &str = "Your Meridian setup changed";

/// One cookie from the main window's store, reduced to what deciding whether
/// a browser would send it to the gateway needs.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct StoredCookie {
    pub name: String,
    pub value: String,
    pub domain: Option<String>,
    pub path: Option<String>,
    pub secure: bool,
    /// Unix seconds; `None` is a session cookie.
    pub expires: Option<i64>,
}

/// Picks the cookies a browser would send with a request to the bound
/// gateway's API (RFC 6265 section 5.4): the domain matches the origin's
/// host exactly, or as a parent domain for a name (never for an IP literal),
/// the path covers every API path, a Secure cookie needs HTTPS, and expired
/// cookies are left out.
///
/// The webview's own per-URL cookie lookup is not used: on macOS it compares
/// `Url::domain()`, which is `None` for an IP host, so it returns nothing
/// for `http://127.0.0.1:8080`, the bundled gateway.
pub fn session_cookies(cookies: &[StoredCookie], origin: &Url, now: i64) -> Vec<(String, String)> {
    let Some(host) = origin.host() else {
        return Vec::new();
    };
    let ip = matches!(host, Host::Ipv4(_) | Host::Ipv6(_));
    let host = match host {
        Host::Ipv6(address) => address.to_string(),
        other => other.to_string().to_ascii_lowercase(),
    };
    let https = origin.scheme() == "https";
    cookies
        .iter()
        .filter(|cookie| {
            let Some(domain) = cookie.domain.as_deref() else {
                return false;
            };
            let domain = domain
                .trim_start_matches('.')
                .trim_start_matches('[')
                .trim_end_matches(']')
                .to_ascii_lowercase();
            let domain_matches = domain == host
                || (!ip && !domain.is_empty() && host.ends_with(&format!(".{domain}")));
            let path_matches = matches!(cookie.path.as_deref(), None | Some("") | Some("/"));
            let expired = cookie.expires.is_some_and(|at| at <= now);
            domain_matches && path_matches && (https || !cookie.secure) && !expired
        })
        .map(|cookie| (cookie.name.clone(), cookie.value.clone()))
        .collect()
}

/// Builds the Cookie header for the bound origin from the main window's
/// cookies, or nothing when there are none. A name or value that could end
/// the header or add one is refused.
pub fn cookie_header(cookies: &[(String, String)]) -> Option<String> {
    let mut parts = Vec::new();
    for (name, value) in cookies {
        let bad = |text: &str| text.chars().any(|c| c.is_control() || c == ';');
        if name.is_empty() || bad(name) || bad(value) || name.contains('=') || name.contains(' ') {
            return None;
        }
        parts.push(format!("{name}={value}"));
    }
    if parts.is_empty() {
        None
    } else {
        Some(parts.join("; "))
    }
}

#[derive(Serialize)]
struct Hello<'a> {
    origin: &'a str,
    cookie: &'a str,
}

/// The first line the mode reads: the bound origin and the session.
pub fn hello_line(origin: &str, cookie: Option<&str>) -> Vec<u8> {
    let mut line = serde_json::to_vec(&Hello {
        origin,
        cookie: cookie.unwrap_or(""),
    })
    .unwrap_or_default();
    line.push(b'\n');
    line
}

pub fn action_line(action: &str) -> Vec<u8> {
    let mut line = serde_json::to_vec(&serde_json::json!({ "action": action })).unwrap_or_default();
    line.push(b'\n');
    line
}

/// The message types the mode sends. Anything else is dropped.
const MESSAGE_TYPES: [&str; 5] = ["review", "signedOut", "error", "saved", "cancelled"];

/// Parses one line from the mode, keeping only known message types.
pub fn parse_message(line: &[u8]) -> Option<serde_json::Value> {
    if line.len() > MAX_LINE {
        return None;
    }
    let value: serde_json::Value = serde_json::from_slice(line).ok()?;
    let kind = value.get("type")?.as_str()?;
    if MESSAGE_TYPES.contains(&kind) {
        Some(value)
    } else {
        None
    }
}

/// Reads `desktop-import --check` output: exactly one `digest`, 64 hex.
pub fn parse_check(output: &[u8]) -> Option<String> {
    let value: serde_json::Value = serde_json::from_slice(output).ok()?;
    let object = value.as_object()?;
    if object.len() != 1 {
        return None;
    }
    let digest = object.get("digest")?.as_str()?;
    if digest.len() == 64 && digest.bytes().all(|b| b.is_ascii_hexdigit()) {
        Some(digest.to_string())
    } else {
        None
    }
}

/// The baseline for one gateway: the digest of the last Save or Dismiss
/// and the member it was for.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Baseline {
    pub member: String,
    pub digest: String,
}

/// What the change check keeps in the app's data directory: no file
/// contents and no paths, only digests and member ids per gateway origin.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CheckState {
    /// "Check this computer for setup changes". On by default.
    #[serde(default = "enabled_by_default")]
    pub enabled: bool,
    #[serde(default)]
    pub baselines: BTreeMap<String, Baseline>,
    /// The last digest a notification was shown for, per origin, so each
    /// change notifies at most once.
    #[serde(default)]
    pub notified: BTreeMap<String, String>,
}

fn enabled_by_default() -> bool {
    true
}

impl Default for CheckState {
    fn default() -> Self {
        Self {
            enabled: true,
            baselines: BTreeMap::new(),
            notified: BTreeMap::new(),
        }
    }
}

impl CheckState {
    pub fn parse(bytes: &[u8]) -> Self {
        serde_json::from_slice(bytes).unwrap_or_default()
    }

    /// Whether a fresh digest for the active origin is a change the user
    /// has not saved, dismissed, or already been told about.
    pub fn should_prompt(&self, origin: &str, digest: &str) -> bool {
        self.enabled
            && self.baselines.get(origin).map(|b| b.digest.as_str()) != Some(digest)
            && self.notified.get(origin).map(String::as_str) != Some(digest)
    }

    /// Whether the active origin has a change waiting for review, for the
    /// tray item's label.
    pub fn pending(&self, origin: &str) -> bool {
        self.enabled
            && self
                .notified
                .get(origin)
                .is_some_and(|digest| self.baselines.get(origin).map(|b| &b.digest) != Some(digest))
    }

    /// Records a Save or Dismiss. A different member signing in on the same
    /// origin replaces the baseline, so it never inherits someone else's.
    pub fn record(&mut self, origin: &str, member: &str, digest: &str) {
        self.baselines.insert(
            origin.to_string(),
            Baseline {
                member: member.to_string(),
                digest: digest.to_string(),
            },
        );
    }
}

/// Whether the computer slept: wall time advanced well past the monotonic
/// interval the check thread measured.
pub fn woke(wall_before: SystemTime, wall_now: SystemTime, monotonic: Duration) -> bool {
    match wall_now.duration_since(wall_before) {
        Ok(wall) => wall > monotonic + Duration::from_secs(120),
        Err(_) => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn stored(name: &str, domain: &str) -> StoredCookie {
        StoredCookie {
            name: name.into(),
            value: "v".into(),
            domain: Some(domain.into()),
            path: Some("/".into()),
            ..Default::default()
        }
    }

    #[test]
    fn session_cookies_work_for_ip_literal_gateways() {
        let local = Url::parse("http://127.0.0.1:8080").unwrap();
        let cookies = vec![
            stored("meridian_session", "127.0.0.1"),
            stored("other_gateway", "10.0.0.5"),
            stored("parent", ".0.0.1"),
            stored("site", "example.com"),
        ];
        let picked: Vec<String> = session_cookies(&cookies, &local, 0)
            .into_iter()
            .map(|(name, _)| name)
            .collect();
        assert_eq!(picked, vec!["meridian_session"]);

        let v6 = Url::parse("http://[::1]:8080").unwrap();
        let picked = session_cookies(
            &[
                stored("s", "::1"),
                stored("bracketed", "[::1]"),
                stored("v4", "127.0.0.1"),
            ],
            &v6,
            0,
        );
        assert_eq!(picked.len(), 2, "{picked:?}");
    }

    #[test]
    fn session_cookies_follow_domain_path_secure_and_expiry() {
        let remote = Url::parse("https://gw.example.com").unwrap();
        let mut secure = stored("__Host-meridian_user", "gw.example.com");
        secure.secure = true;
        let mut scoped = stored("meridian_oidc", "gw.example.com");
        scoped.path = Some("/auth/oidc".into());
        let mut expired = stored("old", "gw.example.com");
        expired.expires = Some(100);
        let mut later = stored("later", "gw.example.com");
        later.expires = Some(10_000);
        let cookies = vec![
            secure.clone(),
            stored("parent", ".example.com"),
            stored("sibling", "other.example.com"),
            stored("suffix", "le.com"),
            scoped,
            expired,
            later,
            StoredCookie {
                domain: None,
                ..stored("nodomain", "")
            },
        ];
        let picked: Vec<String> = session_cookies(&cookies, &remote, 1_000)
            .into_iter()
            .map(|(name, _)| name)
            .collect();
        assert_eq!(picked, vec!["__Host-meridian_user", "parent", "later"]);

        // A Secure cookie is never sent to a plain-HTTP gateway.
        let plain = Url::parse("http://gw.example.com").unwrap();
        assert!(session_cookies(&[secure], &plain, 0).is_empty());
    }

    #[test]
    fn cookie_header_joins_and_refuses_injection() {
        let cookies = vec![
            ("__Host-meridian_user".to_string(), "abc".to_string()),
            ("meridian_session".to_string(), "def".to_string()),
        ];
        assert_eq!(
            cookie_header(&cookies).as_deref(),
            Some("__Host-meridian_user=abc; meridian_session=def")
        );
        assert_eq!(cookie_header(&[]), None);
        for (name, value) in [
            ("a", "b\r\nX-Evil: 1"),
            ("a", "b; c=d"),
            ("a=b", "c"),
            ("", "x"),
            ("a b", "c"),
        ] {
            assert_eq!(
                cookie_header(&[(name.into(), value.into())]),
                None,
                "{name}={value}"
            );
        }
    }

    #[test]
    fn hello_and_actions_are_one_json_line() {
        let hello = hello_line("https://gw.example.com", Some("a=b"));
        assert_eq!(hello.last(), Some(&b'\n'));
        let value: serde_json::Value = serde_json::from_slice(&hello).unwrap();
        assert_eq!(value["origin"], "https://gw.example.com");
        assert_eq!(value["cookie"], "a=b");
        assert_eq!(hello.iter().filter(|b| **b == b'\n').count(), 1);
        let empty: serde_json::Value =
            serde_json::from_slice(&hello_line("http://127.0.0.1:8080", None)).unwrap();
        assert_eq!(empty["cookie"], "");
        assert_eq!(action_line("save"), b"{\"action\":\"save\"}\n");
    }

    #[test]
    fn only_known_messages_reach_the_window() {
        assert!(parse_message(br#"{"type":"review","review":{}}"#).is_some());
        assert!(parse_message(br#"{"type":"saved"}"#).is_some());
        assert!(parse_message(br#"{"type":"navigate","url":"https://evil"}"#).is_none());
        assert!(parse_message(b"not json").is_none());
        assert!(parse_message(&vec![b' '; MAX_LINE + 1]).is_none());
    }

    #[test]
    fn check_output_is_only_a_digest() {
        let digest = "a".repeat(64);
        assert_eq!(
            parse_check(format!(r#"{{"digest":"{digest}"}}"#).as_bytes()),
            Some(digest.clone())
        );
        assert_eq!(
            parse_check(format!(r#"{{"digest":"{digest}","files":[]}}"#).as_bytes()),
            None
        );
        assert_eq!(parse_check(br#"{"digest":"short"}"#), None);
        assert_eq!(parse_check(b"{}"), None);
    }

    #[test]
    fn a_change_prompts_once_until_saved_or_dismissed() {
        let mut state = CheckState::default();
        let origin = "https://gw.example.com";
        assert!(state.enabled);
        assert!(state.should_prompt(origin, "d1"));
        state.notified.insert(origin.into(), "d1".into());
        assert!(
            !state.should_prompt(origin, "d1"),
            "the same change notifies once"
        );
        assert!(state.pending(origin));
        state.record(origin, "usr_ada", "d1");
        assert!(!state.pending(origin));
        assert!(!state.should_prompt(origin, "d1"));
        assert!(
            state.should_prompt(origin, "d2"),
            "a later change prompts again"
        );
        assert!(
            state.should_prompt("http://127.0.0.1:8080", "d1"),
            "each gateway has its own baseline"
        );
        state.enabled = false;
        assert!(!state.should_prompt(origin, "d3"));
        assert!(!state.pending(origin));
    }

    #[test]
    fn check_state_defaults_on_and_survives_garbage() {
        assert_eq!(CheckState::parse(b"not json"), CheckState::default());
        assert!(CheckState::parse(b"{}").enabled);
        let state = CheckState::parse(br#"{"enabled":false}"#);
        assert!(!state.enabled);
        let encoded = serde_json::to_vec(&state).unwrap();
        assert!(!String::from_utf8(encoded).unwrap().contains("contents"));
    }

    #[test]
    fn sleep_is_a_wall_clock_jump() {
        let before = SystemTime::UNIX_EPOCH + Duration::from_secs(1_000_000);
        assert!(!woke(
            before,
            before + Duration::from_secs(60),
            Duration::from_secs(60)
        ));
        assert!(woke(
            before,
            before + Duration::from_secs(3600),
            Duration::from_secs(60)
        ));
        assert!(!woke(
            before,
            before - Duration::from_secs(10),
            Duration::from_secs(60)
        ));
    }
}
