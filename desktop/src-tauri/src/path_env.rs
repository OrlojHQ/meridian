//! PATH for a Station this app starts.
//!
//! A graphical launch gives the process `/usr/bin:/bin:/usr/sbin:/sbin`,
//! which hides a GitHub CLI installed in the usual place. The Station
//! inherits the login shell's PATH, then those install locations, then
//! the path this process already had.

use std::env;
use std::fs;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::thread;
use std::time::{Duration, Instant};

const PATH_MARKER: &str = "__MERIDIAN_PATH__";

/// PATH passed to the bundled Station. Computed once per process.
pub fn station_path() -> String {
    use std::sync::OnceLock;
    static PATH: OnceLock<String> = OnceLock::new();
    PATH.get_or_init(|| {
        let current = env::var("PATH").unwrap_or_default();
        let login = login_shell().and_then(|shell| read_login_path(&shell, Duration::from_secs(2)));
        let home = env::var("HOME").ok();
        build_station_path(&current, login.as_deref(), home.as_deref())
    })
    .clone()
}

/// Joins a login-shell PATH, the usual install locations, and the current
/// PATH. Earlier entries win. Empty and duplicate entries are dropped.
pub fn build_station_path(current: &str, login: Option<&str>, home: Option<&str>) -> String {
    let mut dirs = Vec::new();
    if let Some(login) = login {
        dirs.extend(split_path(login));
    }
    dirs.push("/opt/homebrew/bin".to_string());
    dirs.push("/usr/local/bin".to_string());
    dirs.push("/home/linuxbrew/.linuxbrew/bin".to_string());
    if let Some(home) = home.filter(|value| !value.is_empty()) {
        dirs.push(format!("{home}/.local/bin"));
    }
    dirs.extend(split_path(current));
    for dir in ["/usr/bin", "/bin", "/usr/sbin", "/sbin"] {
        dirs.push(dir.to_string());
    }
    let mut seen = std::collections::BTreeSet::new();
    let mut ordered = Vec::new();
    for dir in dirs {
        if dir.is_empty() || dir.contains('\n') || dir.contains('\0') || !seen.insert(dir.clone()) {
            continue;
        }
        ordered.push(dir);
    }
    ordered.join(":")
}

fn split_path(path: &str) -> Vec<String> {
    path.split(':')
        .filter(|dir| !dir.is_empty())
        .map(str::to_string)
        .collect()
}

fn login_shell() -> Option<PathBuf> {
    if let Some(shell) = env::var_os("SHELL") {
        let path = PathBuf::from(shell);
        if path.is_absolute() && is_executable(&path) {
            return Some(path);
        }
    }
    for candidate in ["/bin/zsh", "/bin/bash", "/bin/sh"] {
        let path = PathBuf::from(candidate);
        if is_executable(&path) {
            return Some(path);
        }
    }
    None
}

fn is_executable(path: &Path) -> bool {
    let Ok(info) = fs::metadata(path) else {
        return false;
    };
    if !info.is_file() {
        return false;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        info.permissions().mode() & 0o111 != 0
    }
    #[cfg(not(unix))]
    {
        true
    }
}

/// Runs the shell as a login shell and reads PATH from between markers, so
/// other text the profile prints is ignored. A shell that does not finish
/// is stopped.
pub fn read_login_path(shell: &Path, timeout: Duration) -> Option<String> {
    let mut command = Command::new(shell);
    command
        .args([
            "-lc",
            "printf '\\n__MERIDIAN_PATH__%s__MERIDIAN_PATH__\\n' \"$PATH\"",
        ])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    // A new process group lets a timeout stop the shell and anything it
    // started, so a profile that does not return cannot hold the pipes open.
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        command.process_group(0);
    }
    let mut child = command.spawn().ok()?;
    let mut stdout = child.stdout.take()?;
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
        let mut buf = Vec::new();
        let _ = stdout.read_to_end(&mut buf);
        let _ = sender.send(buf);
    });
    let started = Instant::now();
    loop {
        match receiver.try_recv() {
            Ok(buf) => {
                let _ = child.wait();
                return parse_marked_path(&buf);
            }
            Err(mpsc::TryRecvError::Disconnected) => {
                let _ = child.wait();
                return None;
            }
            Err(mpsc::TryRecvError::Empty) => {
                if started.elapsed() > timeout {
                    stop_child(&mut child);
                    return None;
                }
                thread::sleep(Duration::from_millis(20));
            }
        }
    }
}

fn stop_child(child: &mut std::process::Child) {
    #[cfg(unix)]
    unsafe {
        libc::kill(-(child.id() as i32), libc::SIGKILL);
    }
    let _ = child.kill();
    let _ = child.wait();
}

fn parse_marked_path(output: &[u8]) -> Option<String> {
    let text = String::from_utf8(output.to_vec()).ok()?;
    let start = text.find(PATH_MARKER)? + PATH_MARKER.len();
    let rest = &text[start..];
    let end = rest.find(PATH_MARKER)?;
    let path = &rest[..end];
    if path.is_empty() || path.contains('\n') || path.contains('\r') || path.contains('\0') {
        return None;
    }
    Some(path.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::OpenOptionsExt;

    #[test]
    fn graphical_path_gains_the_usual_install_locations() {
        let path = build_station_path("/usr/bin:/bin:/usr/sbin:/sbin", None, Some("/Users/ada"));
        let dirs: Vec<&str> = path.split(':').collect();
        assert_eq!(
            dirs,
            vec![
                "/opt/homebrew/bin",
                "/usr/local/bin",
                "/home/linuxbrew/.linuxbrew/bin",
                "/Users/ada/.local/bin",
                "/usr/bin",
                "/bin",
                "/usr/sbin",
                "/sbin",
            ]
        );
    }

    #[test]
    fn login_shell_path_comes_first_and_duplicates_are_dropped() {
        let path = build_station_path(
            "/usr/bin:/bin:/usr/sbin:/sbin",
            Some("/opt/homebrew/bin:/usr/bin:/Users/ada/bin"),
            Some("/Users/ada"),
        );
        assert!(path.starts_with("/opt/homebrew/bin:/usr/bin:/Users/ada/bin:"));
        assert_eq!(path.matches("/opt/homebrew/bin").count(), 1);
        assert!(path.contains("/usr/local/bin"));
        assert!(path.contains("/Users/ada/.local/bin"));
    }

    #[test]
    fn login_shell_output_keeps_only_the_marked_path() {
        let dir = std::env::temp_dir().join(format!("meridian-path-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        let shell = dir.join("shell");
        let mut file = fs::OpenOptions::new()
            .create(true)
            .write(true)
            .mode(0o755)
            .open(&shell)
            .unwrap();
        use std::io::Write;
        write!(
            file,
            "#!/bin/sh\nprintf 'hello\\n{PATH_MARKER}/opt/custom/bin:/usr/bin{PATH_MARKER}\\nbye\\n'\n"
        )
        .unwrap();
        drop(file);
        let got = read_login_path(&shell, Duration::from_secs(2)).unwrap();
        assert_eq!(got, "/opt/custom/bin:/usr/bin");

        let stuck = dir.join("stuck");
        let mut file = fs::OpenOptions::new()
            .create(true)
            .write(true)
            .mode(0o755)
            .open(&stuck)
            .unwrap();
        write!(file, "#!/bin/sh\nsleep 30\n").unwrap();
        drop(file);
        let started = Instant::now();
        assert_eq!(read_login_path(&stuck, Duration::from_millis(200)), None);
        assert!(started.elapsed() < Duration::from_secs(5));
        let _ = fs::remove_dir_all(&dir);
    }
}
