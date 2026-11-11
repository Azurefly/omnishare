//! OmniShare desktop runtime manager.
//!
//! Coordinates desktop startup lifecycle and backend readiness.

use std::{
    path::Path,
    sync::{
        atomic::{AtomicBool, Ordering},
        Mutex,
    },
};

use serde::Serialize;

use crate::process::backend::{BackendProcess, DEFAULT_BACKEND_PORT};
use crate::runtime::bootstrap;

#[derive(Debug, Clone, Serialize)]
pub struct BackendStatus {
    pub running: bool,
    pub url: String,
    pub port: u16,
}

// This response can contain a one-time credential. Keep it out of Debug output
// and ordinary backend status responses.
#[derive(Serialize)]
pub struct BackendNavigation {
    pub running: bool,
    pub url: String,
    pub port: u16,
}

pub struct BackendState {
    process: Mutex<BackendProcess>,
    desired_running: AtomicBool,
    exiting: AtomicBool,
}

impl Default for BackendState {
    fn default() -> Self {
        Self::new(DEFAULT_BACKEND_PORT)
    }
}

impl BackendState {
    pub fn new(port: u16) -> Self {
        Self {
            process: Mutex::new(BackendProcess::new(port)),
            desired_running: AtomicBool::new(false),
            exiting: AtomicBool::new(false),
        }
    }

    pub fn request_start(&self) {
        if !self.is_exiting() {
            self.desired_running.store(true, Ordering::SeqCst);
        }
    }

    pub fn should_run(&self) -> bool {
        !self.is_exiting() && self.desired_running.load(Ordering::SeqCst)
    }

    pub fn is_exiting(&self) -> bool {
        self.exiting.load(Ordering::SeqCst)
    }

    fn require_active(&self) -> Result<(), String> {
        if self.is_exiting() {
            Err("OmniShare desktop is exiting.".to_string())
        } else {
            Ok(())
        }
    }

    pub fn configure_port(&self, port: u16) -> Result<(), String> {
        let mut process = self.process.lock().map_err(|err| err.to_string())?;
        self.require_active()?;
        process.set_port(port)
    }

    pub fn attach(&self, port: u16) -> Result<BackendStatus, String> {
        let mut process = self.process.lock().map_err(|err| err.to_string())?;
        self.require_active()?;
        process.attach(port)?;
        self.desired_running.store(true, Ordering::SeqCst);
        Ok(BackendStatus {
            running: true,
            url: process.url(),
            port: process.port(),
        })
    }

    pub fn status(&self) -> BackendStatus {
        let mut process = self.process.lock().expect("backend process state poisoned");
        BackendStatus {
            running: process.is_running(),
            url: process.url(),
            port: process.port(),
        }
    }

    pub fn navigation(&self) -> Result<BackendNavigation, String> {
        let mut process = self.process.lock().map_err(|err| err.to_string())?;
        let running = process.is_running();
        let url = if running {
            bootstrap::navigation_url(&process.url(), process.managed_data_dir())
        } else {
            process.url()
        };
        Ok(BackendNavigation {
            running,
            url,
            port: process.port(),
        })
    }

    pub fn start(
        &self,
        executable: &Path,
        log_path: &Path,
        data_dir: &Path,
    ) -> Result<BackendStatus, String> {
        let mut process = self.process.lock().map_err(|err| err.to_string())?;
        // The check must share the process lock with shutdown. A watchdog that
        // was already waiting to start cannot resurrect a child after Exit.
        self.require_active()?;
        process
            .start(executable, log_path, data_dir)
            .map_err(|err| err.to_string())?;
        self.desired_running.store(true, Ordering::SeqCst);
        Ok(BackendStatus {
            running: process.is_running(),
            url: process.url(),
            port: process.port(),
        })
    }

    pub fn reset_unhealthy(&self) -> Result<(), String> {
        let mut process = self.process.lock().map_err(|err| err.to_string())?;
        process.stop();
        Ok(())
    }

    pub fn diagnostics(&self) -> String {
        let mut process = self.process.lock().expect("backend process state poisoned");
        process.diagnostics()
    }

    pub fn stop(&self) -> BackendStatus {
        self.desired_running.store(false, Ordering::SeqCst);
        let mut process = self.process.lock().expect("backend process state poisoned");
        process.stop();
        BackendStatus {
            running: false,
            url: process.url(),
            port: process.port(),
        }
    }

    pub fn shutdown(&self) {
        // Set this before waiting for a concurrent startup to release the lock.
        // Unlike an intentional service stop, application exit is irreversible.
        self.exiting.store(true, Ordering::SeqCst);
        self.desired_running.store(false, Ordering::SeqCst);
        let mut process = self
            .process
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        process.stop();
        self.desired_running.store(false, Ordering::SeqCst);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        sync::Arc,
        thread,
        time::{Duration, Instant},
    };

    #[test]
    fn application_shutdown_is_terminal_and_detaches_external_services() {
        let state = BackendState::default();
        state.attach(19081).unwrap();
        assert!(state.should_run());
        state.shutdown();
        state.shutdown(); // ExitRequested and Exit can both reach this hook.
        state.request_start();
        assert!(state.is_exiting());
        assert!(!state.should_run());
        assert!(!state.status().running);
        assert!(state.attach(19081).unwrap_err().contains("exiting"));
        assert!(state.configure_port(19082).unwrap_err().contains("exiting"));
        let absent = Path::new("must-not-be-created-during-exit");
        assert!(state
            .start(absent, absent, absent)
            .unwrap_err()
            .contains("exiting"));
    }

    #[test]
    fn shutdown_prevents_a_watchdog_start_already_waiting_for_the_process_lock() {
        let state = Arc::new(BackendState::default());
        let guard = state.process.lock().unwrap();
        let shutting_down = Arc::clone(&state);
        let exit = thread::spawn(move || shutting_down.shutdown());
        let deadline = Instant::now() + Duration::from_secs(5);
        while !state.is_exiting() {
            assert!(Instant::now() < deadline);
            thread::yield_now();
        }
        let recovering = Arc::clone(&state);
        let recovery = thread::spawn(move || {
            recovering.request_start();
            let absent = Path::new("must-not-be-created-by-watchdog");
            recovering.start(absent, absent, absent)
        });
        drop(guard);
        exit.join().unwrap();
        assert!(recovery.join().unwrap().unwrap_err().contains("exiting"));
        assert!(!state.should_run());
        assert!(!state.status().running);
    }

    #[test]
    fn intentional_service_stop_still_allows_a_later_explicit_start() {
        let state = BackendState::default();
        state.attach(19081).unwrap();
        state.stop();
        assert!(!state.should_run());
        assert!(!state.is_exiting());
        state.request_start();
        state.attach(19081).unwrap();
        assert!(state.should_run());
        state.shutdown();
    }
}
