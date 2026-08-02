use std::process::{Child, Command};

pub struct BackendProcess {
    child: Option<Child>,
}

impl BackendProcess {
    pub fn new() -> Self {
        Self { child: None }
    }

    pub fn is_running(&self) -> bool {
        self.child.is_some()
    }

    pub fn start(&mut self, executable: &str) -> std::io::Result<()> {
        if self.child.is_some() {
            return Ok(());
        }

        let child = Command::new(executable).spawn()?;
        self.child = Some(child);
        Ok(())
    }

    pub fn stop(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.kill();
        }
    }
}
