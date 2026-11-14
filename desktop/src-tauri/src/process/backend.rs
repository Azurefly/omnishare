use std::{
    fs::{self, OpenOptions},
    io::{self, Write},
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    thread,
    time::{Duration, Instant, SystemTime},
};

pub const DEFAULT_BACKEND_PORT: u16 = 8081;
pub const DEFAULT_BACKEND_HOST: &str = "127.0.0.1";
const SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(15);

#[cfg(unix)]
unsafe extern "C" {
    fn kill(pid: i32, signal: i32) -> i32;
}

#[derive(Debug, PartialEq, Eq)]
enum ChildShutdown {
    Graceful,
    Forced,
}

fn stop_owned_child(child: &mut Child, timeout: Duration) -> io::Result<ChildShutdown> {
    stop_owned_child_inner(child, timeout, true)
}

fn stop_owned_child_inner(
    child: &mut Child,
    timeout: Duration,
    force: bool,
) -> io::Result<ChildShutdown> {
    if child.try_wait()?.is_some() {
        return Ok(ChildShutdown::Graceful);
    }

    #[cfg(unix)]
    {
        // This is an unreaped Child we own, so its PID cannot be reused by an
        // unrelated process between try_wait and this signal.
        const SIGTERM: i32 = 15;
        if unsafe { kill(child.id() as i32, SIGTERM) } != 0 {
            eprintln!(
                "Unable to signal the owned backend: {}",
                io::Error::last_os_error()
            );
        }
    }
    // Windows has no portable graceful console signal for a GUI parent's child.
    // Closing this private pipe requests the same Go shutdown path as SIGTERM.
    // It also gives Unix a fallback if delivering the signal failed.
    drop(child.stdin.take());

    let deadline = Instant::now() + timeout;
    loop {
        if child.try_wait()?.is_some() {
            return Ok(ChildShutdown::Graceful);
        }
        if Instant::now() >= deadline {
            break;
        }
        thread::sleep(Duration::from_millis(25));
    }
    if !force {
        return Err(io::Error::new(
            io::ErrorKind::TimedOut,
            "backend is still flushing; update cancelled without force-killing it",
        ));
    }
    child.kill()?;
    child.wait()?;
    Ok(ChildShutdown::Forced)
}

pub struct BackendProcess {
    child: Option<Child>,
    attached: bool,
    port: u16,
    log_path: Option<PathBuf>,
    managed_data_dir: Option<PathBuf>,
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
            managed_data_dir: None,
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
        self.managed_data_dir = None;
        self.last_exit = None;
        Ok(())
    }

    pub fn url(&self) -> String {
        format!("http://{}:{}", DEFAULT_BACKEND_HOST, self.port)
    }

    pub fn managed_data_dir(&self) -> Option<&Path> {
        if self.child.is_some() && !self.attached {
            self.managed_data_dir.as_deref()
        } else {
            None
        }
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

    pub fn start(&mut self, executable: &Path, log_path: &Path, data_dir: &Path) -> io::Result<()> {
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
            .arg("--shutdown-on-stdin-close")
            .arg("--data-dir")
            .arg(data_dir)
            .arg("--port")
            .arg(self.port.to_string())
            .env("OMNISHARE_DESKTOP", "1")
            .env("OMNISHARE_PORT", self.port.to_string())
            .stdin(Stdio::piped())
            .stdout(Stdio::from(stdout_log))
            .stderr(Stdio::from(stderr_log));

        #[cfg(windows)]
        {
            use std::os::windows::process::CommandExt;
            // The desktop owns the UI. The backend uses the private stdin pipe
            // for its lifecycle and does not need a separate console window.
            command.creation_flags(0x08000000); // CREATE_NO_WINDOW
        }

        if let Some(parent) = executable.parent() {
            command.current_dir(parent);
        }

        let child = command.spawn()?;
        self.child = Some(child);
        self.attached = false;
        self.log_path = Some(log_path.to_path_buf());
        self.managed_data_dir = Some(data_dir.to_path_buf());
        self.last_exit = None;
        Ok(())
    }

    pub fn diagnostics(&mut self) -> String {
        let running = self.is_running();
        let mut sections = Vec::new();
        sections.push(format!("Backend process running: {running}"));
        sections.push(format!(
            "Attached to pre-existing backend: {}",
            self.attached
        ));
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

    /// Only stop a backend owned by this desktop; never select processes by
    /// name/port or terminate another deployment. No forced kill on update.
    pub fn prepare_update(&mut self) -> io::Result<()> {
        if self.attached {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "当前桌面连接独立启动的后端，请先退出独立后端后重启桌面应用再升级",
            ));
        }
        if let Some(child) = self.child.as_mut() {
            stop_owned_child_inner(child, SHUTDOWN_TIMEOUT, false)?;
        }
        self.child = None;
        self.managed_data_dir = None;
        Ok(())
    }

    pub fn stop(&mut self) {
        if let Some(mut child) = self.child.take() {
            match stop_owned_child(&mut child, SHUTDOWN_TIMEOUT) {
                Ok(ChildShutdown::Graceful) => {}
                Ok(ChildShutdown::Forced) => {
                    self.last_exit = Some(
                        "Backend exceeded the graceful shutdown deadline and was terminated."
                            .to_string(),
                    );
                    eprintln!("{}", self.last_exit.as_deref().unwrap_or_default());
                }
                Err(error) => {
                    self.last_exit =
                        Some(format!("Unable to shut down the owned backend: {error}"));
                    eprintln!("{}", self.last_exit.as_deref().unwrap_or_default());
                    // Never select a process by name or port. Cleanup is limited
                    // to the Child handle created by this desktop instance.
                    if child.kill().is_ok() {
                        let _ = child.wait();
                    }
                }
            }
        }
        self.attached = false;
        self.managed_data_dir = None;
    }
}

impl Drop for BackendProcess {
    fn drop(&mut self) {
        self.stop();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[cfg(unix)]
    use std::sync::atomic::AtomicBool;
    use std::{
        io::Read,
        sync::atomic::{AtomicUsize, Ordering},
    };

    static NEXT_FIXTURE: AtomicUsize = AtomicUsize::new(0);
    #[cfg(unix)]
    static TERMINATED: AtomicBool = AtomicBool::new(false);

    #[cfg(unix)]
    unsafe extern "C" {
        fn signal(number: i32, handler: usize) -> usize;
    }

    #[cfg(unix)]
    extern "C" fn received_sigterm(_signal: i32) {
        TERMINATED.store(true, Ordering::SeqCst);
    }

    // Run this test alone in a child test executable, so every platform can
    // exercise real process/pipe cleanup without Python, a shell or a mock PID.
    #[test]
    fn owned_child_fixture() {
        let Ok(mode) = std::env::var("OMNISHARE_TEST_CHILD_MODE") else {
            return;
        };
        let root = PathBuf::from(std::env::var_os("OMNISHARE_TEST_CHILD_ROOT").unwrap());
        #[cfg(unix)]
        {
            let handler = if mode == "term" {
                received_sigterm as *const () as usize
            } else {
                1 // SIG_IGN: stdin/timeout fixtures isolate their own mechanism.
            };
            assert_ne!(unsafe { signal(15, handler) }, usize::MAX);
        }
        fs::write(root.join("ready"), b"ready").unwrap();
        if mode == "stdin" {
            let mut ignored = Vec::new();
            std::io::stdin().read_to_end(&mut ignored).unwrap();
            fs::write(root.join("flushed"), b"stdin EOF").unwrap();
            return;
        }
        #[cfg(unix)]
        if mode == "term" {
            while !TERMINATED.load(Ordering::SeqCst) {
                thread::sleep(Duration::from_millis(5));
            }
            fs::write(root.join("flushed"), b"SIGTERM").unwrap();
            return;
        }
        loop {
            thread::sleep(Duration::from_millis(50));
        }
    }

    struct Fixture {
        child: Option<Child>,
        root: PathBuf,
    }

    impl Fixture {
        fn start(mode: &str) -> Self {
            let root = std::env::temp_dir().join(format!(
                "omnishare-process-test-{}-{}",
                std::process::id(),
                NEXT_FIXTURE.fetch_add(1, Ordering::Relaxed)
            ));
            fs::create_dir(&root).unwrap();
            let child = Command::new(std::env::current_exe().unwrap())
                .args([
                    "--exact",
                    "process::backend::tests::owned_child_fixture",
                    "--nocapture",
                ])
                .env("OMNISHARE_TEST_CHILD_MODE", mode)
                .env("OMNISHARE_TEST_CHILD_ROOT", &root)
                .stdin(Stdio::piped())
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .spawn()
                .unwrap();
            let mut fixture = Self {
                child: Some(child),
                root,
            };
            let deadline = Instant::now() + Duration::from_secs(10);
            while !fixture.root.join("ready").exists() {
                assert!(
                    fixture
                        .child
                        .as_mut()
                        .unwrap()
                        .try_wait()
                        .unwrap()
                        .is_none(),
                    "fixture exited during startup"
                );
                assert!(Instant::now() < deadline, "fixture did not become ready");
                thread::sleep(Duration::from_millis(10));
            }
            fixture
        }
    }

    impl Drop for Fixture {
        fn drop(&mut self) {
            if let Some(mut child) = self.child.take() {
                let _ = child.kill();
                let _ = child.wait();
            }
            let _ = fs::remove_dir_all(&self.root);
        }
    }

    #[test]
    fn normal_stop_closes_private_stdin_and_waits_for_cleanup() {
        let mut fixture = Fixture::start("stdin");
        let mut backend = BackendProcess::default();
        backend.child = fixture.child.take();
        backend.stop();
        assert_eq!(
            fs::read(fixture.root.join("flushed")).unwrap(),
            b"stdin EOF"
        );
        assert!(!backend.is_running());
        assert!(backend.last_exit.is_none());
    }

    #[cfg(unix)]
    #[test]
    fn unix_stop_requests_sigterm_before_force() {
        let mut fixture = Fixture::start("term");
        let mut backend = BackendProcess::default();
        backend.child = fixture.child.take();
        backend.stop();
        assert_eq!(fs::read(fixture.root.join("flushed")).unwrap(), b"SIGTERM");
        assert!(backend.last_exit.is_none());
    }

    #[test]
    fn unresponsive_owned_child_is_forced_only_after_a_bounded_grace_period() {
        let mut fixture = Fixture::start("hang");
        let started = Instant::now();
        let child = fixture.child.as_mut().unwrap();
        assert_eq!(
            stop_owned_child(child, Duration::from_millis(150)).unwrap(),
            ChildShutdown::Forced
        );
        assert!(started.elapsed() < Duration::from_secs(5));
        assert!(child.try_wait().unwrap().is_some());
        assert!(!fixture.root.join("flushed").exists());
    }

    #[test]
    fn stopping_an_attachment_leaves_the_external_process_running() {
        let mut fixture = Fixture::start("stdin");
        let mut backend = BackendProcess::default();
        backend.attach(19081).unwrap();
        backend.stop();
        let child = fixture.child.as_mut().unwrap();
        assert!(child.try_wait().unwrap().is_none());
        assert!(!backend.is_running());
        drop(child.stdin.take());
        assert!(child.wait().unwrap().success());
        assert_eq!(
            fs::read(fixture.root.join("flushed")).unwrap(),
            b"stdin EOF"
        );
    }
}
