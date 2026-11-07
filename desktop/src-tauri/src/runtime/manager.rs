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
        }
    }

    pub fn request_start(&self) {
        self.desired_running.store(true, Ordering::SeqCst);
    }

    pub fn should_run(&self) -> bool {
        self.desired_running.load(Ordering::SeqCst)
    }

    pub fn configure_port(&self, port: u16) -> Result<(), String> {
        let mut process = self.process.lock().map_err(|err| err.to_string())?;
        process.set_port(port)
    }

    pub fn attach(&self, port: u16) -> Result<BackendStatus, String> {
        let mut process = self.process.lock().map_err(|err| err.to_string())?;
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
}
