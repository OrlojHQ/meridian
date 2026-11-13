//! How this install of the desktop app is meant to be updated.
//!
//! A Homebrew cask and a Linux package are not replaced in place. macOS
//! copies of the app can download a signed update once a public key is
//! configured. The private key never lives in the repository.

use std::path::Path;

pub const BREW_UPGRADE: &str = "brew upgrade --cask meridian-desktop";
pub const CURL_INSTALL: &str =
    "curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh";
/// Where `brew install --cask meridian-desktop` keeps its receipt, for the
/// Apple silicon and Intel default prefixes. The cask moves the app to
/// /Applications, so the binary path alone does not show Homebrew.
const CASKROOMS: [&str; 2] = [
    "/opt/homebrew/Caskroom/meridian-desktop",
    "/usr/local/Caskroom/meridian-desktop",
];

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

pub fn classify_install(os: &str, exe: &Path, cask_installed: bool) -> InstallSource {
    let text = exe.to_string_lossy().replace('\\', "/");
    if os == "macos" && (cask_installed || text.contains("/Caskroom/meridian-desktop/")) {
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

pub fn cask_installed(caskrooms: &[&str]) -> bool {
    caskrooms.iter().any(|path| Path::new(path).is_dir())
}

pub fn install_source_from_exe(exe: &Path) -> InstallSource {
    let canonical = exe.canonicalize().unwrap_or_else(|_| exe.to_path_buf());
    let os = std::env::consts::OS;
    classify_install(os, &canonical, os == "macos" && cask_installed(&CASKROOMS))
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
            classify_install("macos", exe, false),
            InstallSource::Homebrew
        );
        let intel = Path::new(
            "/usr/local/Caskroom/meridian-desktop/1.2.3/Meridian.app/Contents/MacOS/meridian-desktop",
        );
        assert_eq!(
            classify_install("macos", intel, false),
            InstallSource::Homebrew
        );
    }

    #[test]
    fn cask_receipt_is_homebrew_even_from_applications() {
        let exe = Path::new("/Applications/Meridian.app/Contents/MacOS/meridian-desktop");
        assert_eq!(
            classify_install("macos", exe, true),
            InstallSource::Homebrew
        );
        assert_eq!(
            classify_install("linux", Path::new("/usr/bin/meridian-desktop"), true),
            InstallSource::LinuxPackage
        );
    }

    #[test]
    fn plain_mac_app_can_update_itself_only_when_signed() {
        let exe = Path::new("/Applications/Meridian.app/Contents/MacOS/meridian-desktop");
        assert_eq!(classify_install("macos", exe, false), InstallSource::MacApp);
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
            classify_install("linux", exe, false),
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
    fn finds_a_caskroom_directory() {
        let root = std::env::temp_dir().join(format!("meridian-caskroom-{}", std::process::id()));
        let caskroom = root.join("Caskroom/meridian-desktop");
        let missing = root.join("missing");
        let (present, absent) = (
            caskroom.to_string_lossy().to_string(),
            missing.to_string_lossy().to_string(),
        );
        assert!(!cask_installed(&[&present, &absent]));
        fs::create_dir_all(&caskroom).unwrap();
        assert!(cask_installed(&[&absent, &present]));
        assert!(!cask_installed(&[&absent]));
        let _ = fs::remove_dir_all(root);
    }
}
