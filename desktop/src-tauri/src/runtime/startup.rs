pub struct DesktopStartup {
    pub backend_url: String,
}

impl DesktopStartup {
    pub fn new(backend_url: String) -> Self {
        Self { backend_url }
    }

    pub async fn initialize(&self) -> bool {
        crate::runtime::health::wait_backend(&self.backend_url, 20).await
    }
}
