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

/// Download progress reported to the page. The updater hands over each chunk's
/// length, not a running total, and the content length when it knows one.
pub struct DownloadProgress {
    received: u64,
    last: Option<u8>,
}

impl DownloadProgress {
    pub fn new() -> Self {
        Self {
            received: 0,
            last: None,
        }
    }

    /// Records one chunk. Returns a percent only the first time that integer
    /// is reached, so the page is not updated for every packet.
    pub fn observe(&mut self, chunk: usize, total: Option<u64>) -> Option<u8> {
        let chunk = u64::try_from(chunk).unwrap_or(u64::MAX);
        self.received = self.received.saturating_add(chunk);
        let percent = download_percent(self.received, total)?;
        if self.last == Some(percent) {
            return None;
        }
        self.last = Some(percent);
        Some(percent)
    }
}

pub fn download_percent(received: u64, total: Option<u64>) -> Option<u8> {
    let total = total.filter(|value| *value > 0)?;
    let percent = received.saturating_mul(100) / total;
    Some(percent.min(100) as u8)
}

/// Page event for update progress. `phase` is only `download` or `install`,
/// and `percent` is an integer from 0 to 100 or absent. Anything else is
/// refused so the script cannot carry text the page did not already know.
pub fn update_progress_script(phase: &str, percent: Option<u8>) -> Option<String> {
    match phase {
        "download" | "install" => {}
        _ => return None,
    }
    if percent.is_some_and(|value| value > 100) {
        return None;
    }
    let percent = match percent {
        Some(value) => value.to_string(),
        None => "null".to_string(),
    };
    Some(format!(
        "window.dispatchEvent(new CustomEvent('meridian-update-progress',{{detail:{{phase:'{phase}',percent:{percent}}}}}))"
    ))
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

    #[test]
    fn download_percent_is_monotonic_and_capped() {
        assert_eq!(download_percent(0, Some(100)), Some(0));
        assert_eq!(download_percent(42, Some(100)), Some(42));
        assert_eq!(download_percent(1, Some(3)), Some(33));
        assert_eq!(download_percent(200, Some(100)), Some(100));
        assert_eq!(download_percent(50, None), None);
        assert_eq!(download_percent(50, Some(0)), None);
        assert_eq!(download_percent(u64::MAX, Some(1)), Some(100));
    }

    #[test]
    fn progress_emits_each_percent_once() {
        let mut progress = DownloadProgress::new();
        assert_eq!(progress.observe(0, Some(100)), Some(0));
        assert_eq!(progress.observe(0, Some(100)), None);
        assert_eq!(progress.observe(42, Some(100)), Some(42));
        assert_eq!(progress.observe(1, Some(100)), Some(43));
        assert_eq!(progress.observe(10, None), None);
        assert_eq!(progress.observe(0, Some(0)), None);

        let mut unknown = DownloadProgress::new();
        assert_eq!(unknown.observe(50, None), None);
        assert_eq!(unknown.observe(0, Some(100)), Some(50));

        let mut full = DownloadProgress::new();
        assert_eq!(full.observe(usize::MAX, Some(1)), Some(100));
        assert_eq!(full.observe(usize::MAX, Some(1)), None);
    }

    #[test]
    fn progress_script_is_a_fixed_event() {
        assert_eq!(
            update_progress_script("download", Some(42)).as_deref(),
            Some(
                "window.dispatchEvent(new CustomEvent('meridian-update-progress',{detail:{phase:'download',percent:42}}))"
            )
        );
        assert_eq!(
            update_progress_script("install", None).as_deref(),
            Some(
                "window.dispatchEvent(new CustomEvent('meridian-update-progress',{detail:{phase:'install',percent:null}}))"
            )
        );
        assert!(update_progress_script("download", Some(101)).is_none());
        assert!(update_progress_script("restart", Some(1)).is_none());
        assert!(update_progress_script("download';alert(1)//", Some(1)).is_none());
    }
}
