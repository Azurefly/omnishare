//! Backend data paths and token-free WebView navigation.

use std::{
    env,
    ffi::OsStr,
    path::{Path, PathBuf},
};

pub fn backend_data_dir(executable: &Path) -> Result<PathBuf, String> {
    let cwd = env::current_dir()
        .map_err(|error| format!("Unable to resolve backend working directory: {error}"))?;
    let working_dir = cwd.join(executable.parent().unwrap_or_else(|| Path::new("")));
    let override_dir = env::var_os("OMNISHARE_DATA_DIR");
    let home_dir = env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" });
    Ok(resolve_data_dir(
        override_dir.as_deref(),
        home_dir.as_deref(),
        &working_dir,
    ))
}

fn resolve_data_dir(
    override_dir: Option<&OsStr>,
    home_dir: Option<&OsStr>,
    working_dir: &Path,
) -> PathBuf {
    let data_dir = if let Some(value) = override_dir.filter(|value| !value.is_empty()) {
        PathBuf::from(value)
    } else if let Some(value) = home_dir.filter(|value| !value.is_empty()) {
        PathBuf::from(value).join(".omnishare")
    } else {
        PathBuf::from("data")
    };
    working_dir.join(data_dir)
}

pub fn navigation_url(base_url: &str, _managed_data_dir: Option<&Path>) -> String {
    base_url.to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::sync::atomic::{AtomicUsize, Ordering};

    struct TestDir(PathBuf);

    impl TestDir {
        fn new() -> Self {
            static NEXT_ID: AtomicUsize = AtomicUsize::new(0);
            let path = env::temp_dir().join(format!(
                "omnishare-bootstrap-{}-{}",
                std::process::id(),
                NEXT_ID.fetch_add(1, Ordering::Relaxed)
            ));
            fs::create_dir_all(&path).unwrap();
            Self(path)
        }

        fn write_token(&self, token: &str) {
            let path = self.0.join("init-token");
            fs::write(&path, token).unwrap();
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                fs::set_permissions(path, fs::Permissions::from_mode(0o600)).unwrap();
            }
        }
    }

    impl Drop for TestDir {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    #[test]
    fn data_directory_matches_backend_environment_and_working_directory() {
        let cwd = env::temp_dir().join("backend-bin");
        let home = env::temp_dir().join("account-home");
        let custom = env::temp_dir().join("custom-data");
        assert_eq!(
            resolve_data_dir(Some(custom.as_os_str()), Some(home.as_os_str()), &cwd),
            custom
        );
        assert_eq!(
            resolve_data_dir(
                Some(OsStr::new("relative-data")),
                Some(home.as_os_str()),
                &cwd,
            ),
            cwd.join("relative-data")
        );
        assert_eq!(
            resolve_data_dir(Some(OsStr::new("")), Some(home.as_os_str()), &cwd),
            home.join(".omnishare")
        );
        assert_eq!(resolve_data_dir(None, None, &cwd), cwd.join("data"));
    }

    #[test]
    fn first_navigation_ignores_obsolete_tokens() {
        let data = TestDir::new();
        data.write_token("  abcdefghijklmnopqrstuvwxyz012345+&=?/#\n");
        assert_eq!(
            navigation_url("http://127.0.0.1:8081", Some(&data.0)),
            "http://127.0.0.1:8081"
        );
    }

    #[test]
    fn configured_missing_invalid_and_unmanaged_tokens_are_not_forwarded() {
        let data = TestDir::new();
        let base = "http://127.0.0.1:8081";
        assert_eq!(navigation_url(base, Some(&data.0)), base);
        data.write_token("too-short");
        assert_eq!(navigation_url(base, Some(&data.0)), base);
        data.write_token("abcdefghijklmnopqrstuvwxyz012345");
        assert_eq!(navigation_url(base, None), base);
        fs::remove_file(data.0.join("init-token")).unwrap();
        assert_eq!(navigation_url(base, Some(&data.0)), base);
    }

    #[cfg(unix)]
    #[test]
    fn tokens_readable_by_other_accounts_are_not_forwarded() {
        use std::os::unix::fs::PermissionsExt;
        let data = TestDir::new();
        data.write_token("abcdefghijklmnopqrstuvwxyz012345");
        fs::set_permissions(data.0.join("init-token"), fs::Permissions::from_mode(0o644)).unwrap();
        let base = "http://127.0.0.1:8081";
        assert_eq!(navigation_url(base, Some(&data.0)), base);
    }
}
