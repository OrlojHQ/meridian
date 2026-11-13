//! The bundled meridiand process. Quit signals only a process this app started.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use tauri::AppHandle;
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;

use crate::gateway::LOCAL_LISTEN;

pub struct Sidecar {
    pid: u32,
    child: CommandChild,
    log: Arc<Mutex<String>>,
    exited: Arc<AtomicBool>,
}

impl Sidecar {
    pub fn spawn(app: &AppHandle) -> Result<Self, String> {
        let command = app
            .shell()
            .sidecar("meridian-desktop-gateway")
            .map_err(|error| format!("The bundled gateway is not installed ({error})."))?;
        let (mut events, child) = command
            .args(["--listen", LOCAL_LISTEN])
            .kill_on_drop(true)
            .spawn()
            .map_err(|error| format!("Could not start the local gateway ({error})."))?;
        let pid = child.pid();
        let log = Arc::new(Mutex::new(String::new()));
        let exited = Arc::new(AtomicBool::new(false));
        let log_task = Arc::clone(&log);
        let exited_task = Arc::clone(&exited);
        tauri::async_runtime::spawn(async move {
            while let Some(event) = events.recv().await {
                match event {
                    CommandEvent::Terminated(_) => {
                        exited_task.store(true, Ordering::SeqCst);
                        break;
                    }
                    CommandEvent::Stdout(bytes) | CommandEvent::Stderr(bytes) => {
                        append_log(&log_task, &bytes);
                    }
                    CommandEvent::Error(message) => append_log(&log_task, message.as_bytes()),
                    _ => {}
                }
            }
            exited_task.store(true, Ordering::SeqCst);
        });
        Ok(Self {
            pid,
            child,
            log,
            exited,
        })
    }

    pub fn exited(&self) -> bool {
        self.exited.load(Ordering::SeqCst)
    }

    pub fn log_tail(&self) -> String {
        self.log
            .lock()
            .unwrap_or_else(|error| error.into_inner())
            .clone()
    }

    /// SIGTERM first so meridiand can release its data-directory lease, then SIGKILL.
    pub fn stop(self) {
        signal_term(self.pid);
        let started = Instant::now();
        while started.elapsed() < Duration::from_secs(5) {
            if self.exited() {
                return;
            }
            thread::sleep(Duration::from_millis(50));
        }
        let _ = self.child.kill();
    }
}

fn signal_term(pid: u32) {
    #[cfg(unix)]
    unsafe {
        libc::kill(pid as i32, libc::SIGTERM);
    }
    #[cfg(not(unix))]
    {
        let _ = pid;
    }
}

/// Plain-language reason for the bundled gateway exiting before it was ready,
/// for the failures an operator can act on. The strings matched are meridiand's
/// own errors: `datadir.ErrInUse` and the SQLite store's schema check.
pub fn explain_exit(log: &str) -> Option<&'static str> {
    if log.contains("data directory is in use") {
        return Some(
            "Another Meridian is already using this computer's data, but it is not \
             answering at 127.0.0.1:8080. If it listens at another address, choose \
             Another gateway and enter that address, such as http://127.0.0.1:9090. \
             Otherwise stop it (for Homebrew: brew services stop meridian) and try again.",
        );
    }
    if log.contains("is newer than supported version") {
        return Some(
            "This computer's Meridian data was last opened by a newer meridiand than \
             the one inside this app. Update the desktop app (brew upgrade --cask \
             meridian-desktop, or run the curl installer again), then try again.",
        );
    }
    None
}

fn append_log(slot: &Mutex<String>, bytes: &[u8]) {
    let mut text = String::from_utf8_lossy(bytes).to_string();
    if text.to_ascii_lowercase().contains("bearer ") {
        return;
    }
    if !text.ends_with('\n') {
        text.push('\n');
    }
    let mut guard = slot.lock().unwrap_or_else(|error| error.into_inner());
    guard.push_str(&text);
    if guard.len() > 1200 {
        let start = guard.len() - 1200;
        *guard = guard[start..].to_string();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn explains_a_held_data_directory_and_a_newer_database() {
        let held = "Error: data directory is in use: another Meridian process on this host holds /Users/a/Library/Application Support/meridian\n";
        assert!(explain_exit(held).unwrap().contains("Another gateway"));
        let newer = "Error: open store: schema mismatch: database schema version 31 is newer than supported version 29\n";
        assert!(explain_exit(newer)
            .unwrap()
            .contains("Update the desktop app"));
        assert_eq!(
            explain_exit("cannot reach Docker at unix:///var/run/docker.sock\n"),
            None
        );
    }

    #[test]
    fn log_keeps_a_docker_error_and_drops_a_bearer() {
        let slot = Mutex::new(String::new());
        append_log(
            &slot,
            b"cannot reach Docker at unix:///var/run/docker.sock\n",
        );
        append_log(
            &slot,
            b"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789\n",
        );
        let text = slot.lock().unwrap().clone();
        assert!(text.contains("cannot reach Docker"));
        assert!(!text.to_ascii_lowercase().contains("bearer "));
    }
}
