//! OmniShare desktop runtime manager.
//!
//! Coordinates desktop startup lifecycle and backend readiness.

use std::time::Duration;

pub struct RuntimeManager {
    backend_url: String,
}

impl RuntimeManager {
    pub fn new(backend_url: impl Into<String>) -> Self {
        Self {
            backend_url: backend_url.into(),
        }
    }

    pub fn backend_url(&self) -> &str {
        &self.backend_url
    }

    pub async fn wait_until_ready(&self) -> bool {
        // Placeholder for health probing implementation.
        // Production implementation will call backend health endpoint.
        tokio::time::sleep(Duration::from_millis(100)).await;
        true
    }
}
