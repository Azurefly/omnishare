use std::{
    io,
    path::Path,
    process::{Child, Command, Stdio},
};

pub const DEFAULT_BACKEND_PORT: u16 = 8081;
pub const DEFAULT_BACKEND_HOST: &str = "127.0.0.1";

pub struct BackendProcess {
    child: Option<Child>,
    port: u16,
}

impl Default for BackendProcess {
    fn default() -> Self {
        Self::new(DEFAULT_BACKEND_PORT)
    }
}

impl BackendProcess {
    pub fn new(port: u16) -> Self {
        Self { child: None, port }
    }

    pub fn port(&self) -> u16 {
        self.port
    }

    pub fn set_port(&mut self, port: u16) -> Result<(), String> {
        if self.is_running() {
            return Err("Stop the OmniShare backend before changing its port.".to_string());
        }
        self.port = port;
        Ok(())
    }

    pub fn url(&self) -> String {
        format!("http://{}:{}", DEFAULT_BACKEND_HOST, self.port)
    }

    pub fn is_running(&mut self) -> bool {
        match self.child.as_mut() {
            Some(child) => match child.try_wait() {
                Ok(Some(_status)) => {
                    self.child = None;
                    false
                }
                Ok(None) => true,
                Err(_) => false,
            },
            None => false,
        }
    }

    pub fn start(&mut self, executable: &Path) -> io::Result<()> {
        if self.is_running() {
            return Ok(());
        }

        let child = Command::new(executable)
            .arg("--no-browser")
            .arg("--listen")
            .arg(DEFAULT_BACKEND_HOST)
            .arg("--port")
            .arg(self.port.to_string())
            .env("OMNISHARE_DESKTOP", "1")
            .env("OMNISHARE_PORT", self.port.to_string())
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()?;

        self.child = Some(child);
        Ok(())
    }

    pub fn stop(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

impl Drop for BackendProcess {
    fn drop(&mut self) {
        self.stop();
    }
}
