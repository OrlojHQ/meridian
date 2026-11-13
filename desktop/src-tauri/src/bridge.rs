//! The gateway bridge's permission. Only the gateway origin the operator chose
//! may call the bridge commands, and only while it is the active gateway.
//!
//! Tauri can add a capability at runtime but cannot remove one, so a grant
//! outlives a gateway switch. Two checks keep a stale grant inert:
//! - The shell grants one capability per chosen origin, with a single remote
//!   URL pattern that matches that origin and nothing else.
//! - Every bridge call must also have been matched by the active origin's
//!   pattern. Tauri matches patterns against the IPC request's own origin, so
//!   a page on a previously chosen origin is refused after a switch.

use tauri::ipc::CapabilityBuilder;
use tauri::utils::acl::resolved::ResolvedCommand;
use tauri::utils::acl::{ExecutionContext, RemoteUrlPattern};
use url::Url;

/// The window that loads the gateway.
pub const WINDOW: &str = "main";

/// Commands a gateway page may call through `window.__MERIDIAN_DESKTOP__`.
pub const COMMANDS: [&str; 5] = [
    "desktop_notify",
    "desktop_set_attention",
    "desktop_open_gateway",
    "desktop_update_check",
    "desktop_update_install",
];

/// The app permissions that allow [`COMMANDS`]. The picker's own permissions
/// (`capabilities/local.json`) are never granted to a gateway origin.
pub const PERMISSIONS: [&str; 5] = [
    "allow-desktop-notify",
    "allow-desktop-set-attention",
    "allow-desktop-open-gateway",
    "allow-desktop-update-check",
    "allow-desktop-update-install",
];

pub fn is_bridge_command(command: &str) -> bool {
    COMMANDS.contains(&command)
}

/// The capability identifier for the `index`th origin granted in this process.
pub fn capability_id(index: usize) -> String {
    format!("gateway-bridge-{index}")
}

/// A Tauri remote URL pattern that matches exactly one origin: its scheme,
/// host, and port, with any path, query, or fragment. `origin` must be the
/// serialized origin that `validate_gateway_url` returns.
pub fn origin_pattern(origin: &str) -> Result<String, String> {
    let rejected = || "The gateway origin cannot be granted the desktop bridge.".to_string();
    let url = Url::parse(origin).map_err(|_| rejected())?;
    if !matches!(url.scheme(), "http" | "https")
        || url.origin().ascii_serialization() != origin
        || !url.username().is_empty()
        || url.password().is_some()
        || url.path() != "/"
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err(rejected());
    }
    let host = url.host_str().ok_or_else(rejected)?;
    let mut pattern = format!("{}://{}", url.scheme(), escape(host));
    if let Some(port) = url.port() {
        pattern.push(':');
        pattern.push_str(&port.to_string());
    }
    // Tauri reads an empty or "/" path as any path.
    Ok(pattern)
}

/// Escapes URL pattern syntax so a host name is matched literally.
fn escape(host: &str) -> String {
    let mut escaped = String::with_capacity(host.len());
    for character in host.chars() {
        if matches!(
            character,
            ':' | '*' | '?' | '+' | '(' | ')' | '{' | '}' | '\\'
        ) {
            escaped.push('\\');
        }
        escaped.push(character);
    }
    escaped
}

/// Parses an [`origin_pattern`] into the matcher Tauri uses.
pub fn parse_pattern(pattern: &str) -> Result<RemoteUrlPattern, String> {
    pattern
        .parse()
        .map_err(|_| "The gateway origin cannot be granted the desktop bridge.".to_string())
}

/// The bridge capability for one gateway origin pattern: the main window,
/// that remote pattern only, no local shell pages, and only [`PERMISSIONS`].
pub fn capability(id: &str, pattern: &str) -> CapabilityBuilder {
    PERMISSIONS.iter().fold(
        CapabilityBuilder::new(id)
            .local(false)
            .window(WINDOW)
            .remote(pattern.to_string()),
        |builder, permission| builder.permission(*permission),
    )
}

/// Whether a bridge call that Tauri's ACL let through was matched by the
/// active gateway's pattern. `matched` is the call's resolved ACL entries.
pub fn call_allowed(
    active: Option<&RemoteUrlPattern>,
    matched: Option<&[ResolvedCommand]>,
) -> bool {
    let (Some(active), Some(matched)) = (active, matched) else {
        return false;
    };
    matched.iter().any(|command| match &command.context {
        ExecutionContext::Remote { url } => url == active,
        ExecutionContext::Local => false,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::gateway::{validate_gateway_url, LOCAL_ORIGIN};
    use tauri::ipc::RuntimeCapability;
    use tauri::utils::acl::capability::{CapabilityFile, PermissionEntry};

    fn matcher(origin: &str) -> RemoteUrlPattern {
        parse_pattern(&origin_pattern(origin).unwrap()).unwrap()
    }

    fn url(raw: &str) -> Url {
        raw.parse().unwrap()
    }

    #[test]
    fn pattern_is_the_origin() {
        assert_eq!(
            origin_pattern("http://127.0.0.1:9090").unwrap(),
            "http://127.0.0.1:9090"
        );
        assert_eq!(
            origin_pattern("https://gateway.example.com").unwrap(),
            "https://gateway.example.com"
        );
        assert_eq!(
            origin_pattern("http://[::1]:9090").unwrap(),
            r"http://[\:\:1]:9090"
        );
    }

    #[test]
    fn pattern_matches_any_page_on_the_origin() {
        let pattern = matcher("http://127.0.0.1:9090");
        for page in [
            "http://127.0.0.1:9090",
            "http://127.0.0.1:9090/",
            "http://127.0.0.1:9090/ui/",
            "http://127.0.0.1:9090/ui/threads/abc/def?tab=1#end",
        ] {
            assert!(pattern.test(&url(page)), "{page}");
        }
    }

    #[test]
    fn pattern_rejects_every_other_origin() {
        let pattern = matcher("http://127.0.0.1:9090");
        for other in [
            "http://127.0.0.1:8080/ui/",
            "http://127.0.0.1/ui/",
            "https://127.0.0.1:9090/ui/",
            "http://localhost:9090/ui/",
            "http://127.0.0.2:9090/ui/",
            "tauri://localhost/index.html",
        ] {
            assert!(!pattern.test(&url(other)), "{other}");
        }

        let pattern = matcher("https://gateway.example.com");
        assert!(pattern.test(&url("https://gateway.example.com/ui/")));
        for other in [
            "https://gateway.example.com:8443/ui/",
            "http://gateway.example.com/ui/",
            "https://evil.gateway.example.com/ui/",
            "https://gateway.example.com.evil.test/ui/",
            "https://gatewayxexample.com/ui/",
            "https://example.com/ui/",
        ] {
            assert!(!pattern.test(&url(other)), "{other}");
        }
    }

    #[test]
    fn ipv6_loopback_pattern_is_literal() {
        let origin = validate_gateway_url("http://[::1]:9090/ui/").unwrap();
        let pattern = matcher(&origin);
        assert!(pattern.test(&url("http://[::1]:9090/ui/")));
        assert!(!pattern.test(&url("http://[::1]:9091/ui/")));
        assert!(!pattern.test(&url("http://[::2]:9090/ui/")));
    }

    #[test]
    fn pattern_syntax_in_a_host_is_literal() {
        // The URL parser accepts these characters in a host name; each is
        // URL pattern syntax and must not act as a wildcard or group.
        for (raw, lookalike) in [
            ("https://a*b.example", "https://axxb.example"),
            ("https://a+b.example", "https://aab.example"),
            ("https://a(b).example", "https://ab.example"),
            ("https://a{b}.example", "https://ab.example"),
        ] {
            let origin = validate_gateway_url(raw).unwrap();
            let pattern = matcher(&origin);
            assert!(pattern.test(&url(&format!("{origin}/ui/"))), "{raw}");
            assert!(!pattern.test(&url(lookalike)), "{raw} matched {lookalike}");
        }
    }

    #[test]
    fn pattern_requires_a_serialized_origin() {
        for raw in [
            "",
            "127.0.0.1:9090",
            "http://127.0.0.1:9090/ui/",
            "http://127.0.0.1:9090/",
            "http://user@127.0.0.1:9090",
            "http://127.0.0.1:9090?x=1",
            "ftp://127.0.0.1",
            "tauri://localhost",
            "null",
        ] {
            assert!(origin_pattern(raw).is_err(), "{raw}");
        }
    }

    #[test]
    fn every_validated_origin_has_a_pattern() {
        for raw in [
            "http://127.0.0.1:9090/ui/",
            "http://localhost:8080",
            "https://gateway.example.com/",
            "https://gateway.example.com:443",
            "https://GATEWAY.example.com:8443/ui/",
            "https://xn--bcher-kva.example",
            "https://bücher.example",
        ] {
            let origin = validate_gateway_url(raw).unwrap();
            let pattern = matcher(&origin);
            assert!(pattern.test(&url(&format!("{origin}/ui/"))), "{raw}");
        }
        let pattern = matcher(LOCAL_ORIGIN);
        assert!(pattern.test(&url("http://127.0.0.1:8080/ui/")));
    }

    #[test]
    fn capability_grants_only_the_bridge_to_one_origin() {
        let CapabilityFile::Capability(capability) =
            capability("gateway-bridge-0", "http://127.0.0.1:9090").build()
        else {
            panic!("expected one capability");
        };
        assert_eq!(capability.identifier, "gateway-bridge-0");
        assert!(
            !capability.local,
            "a gateway grant must not reach shell pages"
        );
        assert_eq!(capability.windows, vec![WINDOW.to_string()]);
        assert!(capability.webviews.is_empty());
        assert_eq!(
            capability.remote.map(|remote| remote.urls),
            Some(vec!["http://127.0.0.1:9090".to_string()])
        );
        let permissions: Vec<String> = capability
            .permissions
            .iter()
            .map(|entry| match entry {
                PermissionEntry::PermissionRef(identifier) => identifier.get().to_string(),
                PermissionEntry::ExtendedPermission { .. } => panic!("unexpected scope"),
            })
            .collect();
        assert_eq!(permissions, PERMISSIONS.map(String::from).to_vec());
    }

    #[test]
    fn bridge_permissions_match_the_commands() {
        for (command, permission) in COMMANDS.iter().zip(PERMISSIONS) {
            assert_eq!(format!("allow-{}", command.replace('_', "-")), permission);
            assert!(is_bridge_command(command));
        }
        assert!(!is_bridge_command("gateway_state"));
        assert!(!is_bridge_command("gateway_connect"));
    }

    #[test]
    fn picker_capability_stays_local() {
        let local: serde_json::Value =
            serde_json::from_str(include_str!("../capabilities/local.json")).unwrap();
        assert!(local.get("remote").is_none());
        assert_ne!(local.get("local"), Some(&serde_json::Value::Bool(false)));
        let permissions = local["permissions"].as_array().unwrap();
        for permission in PERMISSIONS {
            assert!(!permissions.contains(&serde_json::Value::from(permission)));
        }
        for permission in ["allow-gateway-state", "allow-gateway-connect"] {
            assert!(!PERMISSIONS.contains(&permission));
        }
    }

    fn resolved(context: ExecutionContext) -> ResolvedCommand {
        ResolvedCommand {
            context,
            ..Default::default()
        }
    }

    fn remote(origin: &str) -> ResolvedCommand {
        resolved(ExecutionContext::Remote {
            url: matcher(origin),
        })
    }

    #[test]
    fn call_needs_the_active_origin_grant() {
        let active = matcher("http://127.0.0.1:9090");
        let current = [remote("http://127.0.0.1:9090")];
        assert!(call_allowed(Some(&active), Some(&current)));

        // A grant left from a gateway the operator switched away from.
        let stale = [remote("https://old.example.com")];
        assert!(!call_allowed(Some(&active), Some(&stale)));

        // No gateway is active while the picker is shown.
        assert!(!call_allowed(None, Some(&current)));
        assert!(!call_allowed(Some(&active), None));
        assert!(!call_allowed(Some(&active), Some(&[])));
        assert!(!call_allowed(
            Some(&active),
            Some(&[resolved(ExecutionContext::Local)])
        ));
    }
}
