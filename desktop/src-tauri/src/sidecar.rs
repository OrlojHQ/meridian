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
            .sidecar("meridiand")
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
