//! How this install of the desktop app is meant to be updated.
//!
//! A Homebrew cask and a Linux package are not replaced in place. macOS
//! copies of the app can download a signed update once a public key is
//! configured. The private key never lives in the repository.

use std::path::Path;

pub const BREW_UPGRADE: &str = "brew upgrade --cask meridian-desktop";
pub const CURL_INSTALL: &str =
    "curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh";
const MARKER: &str = "meridian-install-source";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum InstallSource {
    Homebrew,
    LinuxPackage,
    MacApp,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum UpdateAction {
    Install,
    Command(&'static str),
}

pub fn updater_signing_configured() -> bool {
    let raw = include_str!("../tauri.conf.json");
    let value: serde_json::Value = serde_json::from_str(raw).unwrap_or_default();
    value
        .pointer("/plugins/updater/pubkey")
        .and_then(|key| key.as_str())
        .is_some_and(|key| !key.trim().is_empty())
}

pub fn classify_install(os: &str, exe: &Path, marker: Option<&str>) -> InstallSource {
    let text = exe.to_string_lossy().replace('\\', "/");
    if text.contains("/Caskroom/meridian-desktop/") || marker == Some("homebrew") {
        return InstallSource::Homebrew;
    }
    if os == "macos" {
        InstallSource::MacApp
    } else {
        InstallSource::LinuxPackage
    }
}

pub fn update_action(source: InstallSource, signing_configured: bool) -> UpdateAction {
    match source {
        InstallSource::Homebrew => UpdateAction::Command(BREW_UPGRADE),
        InstallSource::LinuxPackage => UpdateAction::Command(CURL_INSTALL),
        InstallSource::MacApp if signing_configured => UpdateAction::Install,
        InstallSource::MacApp => UpdateAction::Command(CURL_INSTALL),
    }
}

pub fn install_marker(exe: &Path) -> Option<String> {
    let resources = exe.parent()?.parent()?.join("Resources").join(MARKER);
    let text = std::fs::read_to_string(resources).ok()?;
    let trimmed = text.trim();
    if trimmed == "homebrew" {
        Some(trimmed.to_string())
    } else {
        None
    }
}

pub fn install_source_from_exe(exe: &Path) -> InstallSource {
    let canonical = exe.canonicalize().unwrap_or_else(|_| exe.to_path_buf());
    let marker = install_marker(&canonical).or_else(|| install_marker(exe));
    classify_install(std::env::consts::OS, &canonical, marker.as_deref())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    #[test]
    fn caskroom_path_is_homebrew() {
        let exe = Path::new(
            "/opt/homebrew/Caskroom/meridian-desktop/1.2.3/Meridian.app/Contents/MacOS/meridian-desktop",
        );
        assert_eq!(
            classify_install("macos", exe, None),
            InstallSource::Homebrew
        );
        let intel = Path::new(
            "/usr/local/Caskroom/meridian-desktop/1.2.3/Meridian.app/Contents/MacOS/meridian-desktop",
        );
        assert_eq!(
            classify_install("macos", intel, None),
            InstallSource::Homebrew
        );
    }

    #[test]
    fn marker_file_is_homebrew_even_from_applications() {
        let exe = Path::new("/Applications/Meridian.app/Contents/MacOS/meridian-desktop");
        assert_eq!(
            classify_install("macos", exe, Some("homebrew")),
            InstallSource::Homebrew
        );
    }

    #[test]
    fn plain_mac_app_can_update_itself_only_when_signed() {
        let exe = Path::new("/Applications/Meridian.app/Contents/MacOS/meridian-desktop");
        assert_eq!(classify_install("macos", exe, None), InstallSource::MacApp);
        assert_eq!(
            update_action(InstallSource::MacApp, true),
            UpdateAction::Install
        );
        assert_eq!(
            update_action(InstallSource::MacApp, false),
            UpdateAction::Command(CURL_INSTALL)
        );
    }

    #[test]
    fn linux_package_and_homebrew_use_commands() {
        let exe = Path::new("/usr/bin/meridian-desktop");
        assert_eq!(
            classify_install("linux", exe, None),
            InstallSource::LinuxPackage
        );
        assert_eq!(
            update_action(InstallSource::LinuxPackage, true),
            UpdateAction::Command(CURL_INSTALL)
        );
        assert_eq!(
            update_action(InstallSource::Homebrew, true),
            UpdateAction::Command(BREW_UPGRADE)
        );
    }

    #[test]
    fn reads_homebrew_marker_beside_the_binary() {
        let root = std::env::temp_dir().join(format!("meridian-marker-{}", std::process::id()));
        let exe = root.join("Contents/MacOS/meridian-desktop");
        let marker = root.join("Contents/Resources").join(MARKER);
        fs::create_dir_all(exe.parent().unwrap()).unwrap();
        fs::create_dir_all(marker.parent().unwrap()).unwrap();
        fs::write(&exe, "").unwrap();
        fs::write(&marker, "homebrew\n").unwrap();
        assert_eq!(install_marker(&exe).as_deref(), Some("homebrew"));
        fs::write(&marker, "other\n").unwrap();
        assert_eq!(install_marker(&exe), None);
        let _ = fs::remove_dir_all(root);
    }
}
