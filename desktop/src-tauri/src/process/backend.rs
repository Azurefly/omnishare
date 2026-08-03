use std::{
    fs::{self, OpenOptions},
    io::{self, Write},
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    time::SystemTime,
};

pub const DEFAULT_BACKEND_PORT: u16 = 8081;
pub const DEFAULT_BACKEND_HOST: &str = "127.0.0.1";

pub struct BackendProcess {
    child: Option<Child>,
    attached: bool,
    port: u16,
    log_path: Option<PathBuf>,
    last_exit: Option<String>,
}

impl Default for BackendProcess {
    fn default() -> Self {
        Self::new(DEFAULT_BACKEND_PORT)
    }
}

impl BackendProcess {
    pub fn new(port: u16) -> Self {
        Self {
            child: None,
            attached: false,
            port,
            log_path: None,
            last_exit: None,
        }
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

    pub fn attach(&mut self, port: u16) -> Result<(), String> {
        if self.child.is_some() && self.is_running() {
            return Err("A managed OmniShare backend is already running.".to_string());
        }
        self.port = port;
        self.attached = true;
        self.last_exit = None;
        Ok(())
    }

    pub fn url(&self) -> String {
        format!("http://{}:{}", DEFAULT_BACKEND_HOST, self.port)
    }

    pub fn is_running(&mut self) -> bool {
        if self.attached {
            return true;
        }

        match self.child.as_mut() {
            Some(child) => match child.try_wait() {
                Ok(Some(status)) => {
                    self.last_exit = Some(format!("Backend process exited with status {status}."));
                    self.child = None;
                    false
                }
                Ok(None) => true,
                Err(error) => {
                    self.last_exit = Some(format!("Unable to inspect backend process: {error}"));
                    false
                }
            },
            None => false,
        }
    }

    pub fn start(&mut self, executable: &Path, log_path: &Path) -> io::Result<()> {
        if self.is_running() {
            return Ok(());
        }

        if let Some(parent) = log_path.parent() {
            fs::create_dir_all(parent)?;
        }

        let mut stdout_log = OpenOptions::new()
            .create(true)
            .append(true)
            .open(log_path)?;
        writeln!(
            stdout_log,
            "\n=== OmniShare backend start {:?} | executable={} | port={} ===",
            SystemTime::now(),
            executable.display(),
            self.port
        )?;
        stdout_log.flush()?;
        let stderr_log = stdout_log.try_clone()?;

        let mut command = Command::new(executable);
        command
            .arg("--no-browser")
            .arg("--listen")
            .arg(DEFAULT_BACKEND_HOST)
            .arg("--port")
            .arg(self.port.to_string())
            .env("OMNISHARE_DESKTOP", "1")
            .env("OMNISHARE_PORT", self.port.to_string())
            .stdin(Stdio::null())
            .stdout(Stdio::from(stdout_log))
            .stderr(Stdio::from(stderr_log));

        if let Some(parent) = executable.parent() {
            command.current_dir(parent);
        }

        let child = command.spawn()?;
        self.child = Some(child);
        self.attached = false;
        self.log_path = Some(log_path.to_path_buf());
        self.last_exit = None;
        Ok(())
    }

    pub fn diagnostics(&mut self) -> String {
        let running = self.is_running();
        let mut sections = Vec::new();
        sections.push(format!("Backend process running: {running}"));
        sections.push(format!("Attached to pre-existing backend: {}", self.attached));
        if let Some(last_exit) = &self.last_exit {
            sections.push(last_exit.clone());
        }

        if let Some(log_path) = &self.log_path {
            sections.push(format!("Backend log: {}", log_path.display()));
            match fs::read_to_string(log_path) {
                Ok(content) => {
                    let tail = content
                        .lines()
                        .rev()
                        .take(80)
                        .collect::<Vec<_>>()
                        .into_iter()
                        .rev()
                        .collect::<Vec<_>>()
                        .join("\n");
                    if !tail.trim().is_empty() {
                        sections.push(format!("Recent backend output:\n{tail}"));
                    }
                }
                Err(error) => sections.push(format!("Unable to read backend log: {error}")),
            }
        } else {
            sections.push("Backend log was not initialized.".to_string());
        }

        sections.join("\n")
    }

    pub fn stop(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.kill();
            let _ = child.wait();
        }
        self.attached = false;
    }
}

impl Drop for BackendProcess {
    fn drop(&mut self) {
        self.stop();
    }
}
