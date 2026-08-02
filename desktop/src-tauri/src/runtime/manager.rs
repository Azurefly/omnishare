//! OmniShare desktop runtime manager.
//!
//! Coordinates desktop startup lifecycle and backend readiness.

use std::{path::Path, sync::Mutex};

use serde::Serialize;

use crate::process::backend::{BackendProcess, DEFAULT_BACKEND_PORT};

#[derive(Debug, Clone, Serialize)]
pub struct BackendStatus {
    pub running: bool,
    pub url: String,
    pub port: u16,
}

pub struct BackendState {
    process: Mutex<BackendProcess>,
}

impl Default for BackendState {
    fn default() -> Self {
        Self {
            process: Mutex::new(BackendProcess::new(DEFAULT_BACKEND_PORT)),
        }
    }
}

impl BackendState {
    pub fn status(&self) -> BackendStatus {
        let mut process = self.process.lock().expect("backend process state poisoned");
        BackendStatus {
            running: process.is_running(),
            url: process.url(),
            port: process.port(),
        }
    }

    pub fn start(&self, executable: &Path) -> Result<BackendStatus, String> {
        let mut process = self.process.lock().map_err(|err| err.to_string())?;
        process.start(executable).map_err(|err| err.to_string())?;
        Ok(BackendStatus {
            running: process.is_running(),
            url: process.url(),
            port: process.port(),
        })
    }

    pub fn stop(&self) -> BackendStatus {
        let mut process = self.process.lock().expect("backend process state poisoned");
        process.stop();
        BackendStatus {
            running: false,
            url: process.url(),
            port: process.port(),
        }
    }
}
