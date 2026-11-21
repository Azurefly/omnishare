//! Test-only fixtures: no alternate endpoint or trusted test key is compiled into the app.
use super::*;
use serde_json::json;
use std::{
    cell::Cell,
    fs,
    io::{Read, Write},
    net::TcpListener,
    path::{Path, PathBuf},
    thread,
};

#[test]
fn installer_launch_failure_restores_service_without_terminal_shutdown() {
    let backend = BackendState::new(19081);
    backend.request_start();
    let recovered = Cell::new(false);
    let error = install_with_recovery(
        &backend,
        || Ok(()),
        || {
            assert!(!backend.should_run());
            assert!(!backend.is_exiting());
            Err("installer launch cancelled".into())
        },
        |port| {
            assert_eq!(port, 19081);
            assert!(backend.should_run());
            assert!(!backend.is_exiting());
            assert!(backend.startup_guard().is_ok());
            recovered.set(true);
            Ok(())
        },
    )
    .unwrap_err();
    assert!(recovered.get());
    assert!(error.contains("installer launch cancelled"));
    assert!(backend.should_run());
}

#[test]
fn installer_panic_restores_service_without_terminal_shutdown() {
    let backend = BackendState::new(19081);
    backend.request_start();
    let recovered = Cell::new(false);
    let error = install_with_recovery(
        &backend,
        || Ok(()),
        || panic!("test installer panic"),
        |_| {
            recovered.set(true);
            Ok(())
        },
    )
    .unwrap_err();
    assert!(recovered.get());
    assert!(backend.should_run());
    assert!(!backend.is_exiting());
    assert!(error.contains("安装器意外中断"));
}

#[test]
fn failed_verification_never_stops_backend_or_runs_installer() {
    let backend = BackendState::new(19081);
    backend.request_start();
    let error = install_with_recovery(
        &backend,
        || Err("bad signature".into()),
        || panic!("must not install"),
        |_| panic!("must not recover"),
    )
    .unwrap_err();
    assert_eq!(error, "bad signature");
    assert!(backend.should_run());
    assert!(!backend.is_exiting());
}

#[test]
fn attached_external_backend_is_not_stopped_or_updated() {
    let backend = BackendState::new(19081);
    backend.attach(19081).unwrap();
    assert!(install_with_recovery(
        &backend,
        || Ok(()),
        || panic!("must not install"),
        |_| panic!("must not recover")
    )
    .is_err());
    assert!(backend.status().running);
    assert!(backend.should_run());
    backend.shutdown();
}

#[test]
fn intentionally_stopped_service_remains_stopped_after_failure() {
    let backend = BackendState::new(19081);
    let error = install_with_recovery(
        &backend,
        || Ok(()),
        || Err("denied".into()),
        |_| panic!("must not start a stopped service"),
    )
    .unwrap_err();
    assert!(error.contains("停止状态"));
    assert!(!backend.should_run());
    assert!(!backend.is_exiting());
    assert!(backend.startup_guard().is_ok());
}

#[test]
fn recovery_failure_is_reported_instead_of_claiming_success() {
    let backend = BackendState::new(19081);
    backend.request_start();
    let error = install_with_recovery(
        &backend,
        || Ok(()),
        || Err("denied".into()),
        |_| Err("port unavailable".into()),
    )
    .unwrap_err();
    assert!(error.contains("恢复服务失败"));
    assert!(error.contains("port unavailable"));
    assert!(!backend.is_exiting());
}

#[test]
fn successful_install_keeps_watchdog_paused_until_explicit_restart() {
    let backend = BackendState::new(19081);
    backend.request_start();
    install_with_recovery(
        &backend,
        || Ok(()),
        || Ok(()),
        |_| panic!("must not recover"),
    )
    .unwrap();
    assert!(!backend.should_run());
    assert!(!backend.is_exiting());
    backend.shutdown();
    assert!(backend.is_exiting());
}

fn fixture_root() -> PathBuf {
    let path = PathBuf::from(
        std::env::var("OMNISHARE_UPDATER_TEST_FIXTURES")
            .expect("run scripts/test-native-updates.py to create isolated, signed test fixtures"),
    );
    assert!(path.is_absolute());
    assert_eq!(
        fs::read_to_string(path.join("TEST-ONLY")).unwrap(),
        "OmniShare ephemeral updater fixtures\n"
    );
    path
}

fn updater(key: &str, executable: &Path) -> (tauri::App<tauri::test::MockRuntime>, Updater) {
    let mut context = tauri::test::mock_context(tauri::test::noop_assets());
    context.config_mut().plugins.0.insert(
        "updater".into(),
        json!({
            "pubkey": key.trim(),
            "requireSignedVersion": true,
            "windows": { "installMode": "quiet" }
        }),
    );
    let app = tauri::test::mock_builder()
        .plugin(tauri_plugin_updater::Builder::new().build())
        .build(context)
        .unwrap();
    let updater = app
        .updater_builder()
        .pubkey(key.trim())
        .executable_path(executable)
        .endpoints(vec![Url::parse(
            "https://github.com/Azurefly/omnishare/releases/download/v1.0.1/latest.json",
        )
        .unwrap()])
        .unwrap()
        .target("fixture")
        .on_before_exit(|| {})
        .build()
        .unwrap();
    (app, updater)
}

fn metadata(signature: &str, version: &str) -> serde_json::Value {
    json!({"version":version,"url":format!("https://github.com/Azurefly/omnishare/releases/download/v{version}/fixture.bin"),"signature":signature.trim()})
}

// A real, bounded TCP fixture, not a mocked reqwest response. The production
// download function below still applies length limits and cryptographic checks.
fn response_server(body: Vec<u8>, mode: &str) -> (Url, thread::JoinHandle<()>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let url = Url::parse(&format!(
        "http://{}/package",
        listener.local_addr().unwrap()
    ))
    .unwrap();
    let mode = mode.to_owned();
    let worker = thread::spawn(move || {
        listener.set_nonblocking(true).unwrap();
        let deadline = std::time::Instant::now() + Duration::from_secs(10);
        let mut socket = loop {
            match listener.accept() {
                Ok((socket, _)) => break socket,
                Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                    assert!(
                        std::time::Instant::now() < deadline,
                        "test request never arrived"
                    );
                    thread::sleep(Duration::from_millis(5));
                }
                Err(error) => panic!("{error}"),
            }
        };
        socket
            .set_read_timeout(Some(Duration::from_secs(2)))
            .unwrap();
        let _ = socket.read(&mut [0; 8192]);
        let wire = if mode == "chunked" || mode == "oversized" {
            let mut b = body;
            if mode == "oversized" {
                b.push(0);
            }
            let mut wire = format!("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n{:x}\r\n", b.len()).into_bytes();
            wire.extend(b);
            wire.extend(b"\r\n0\r\n\r\n");
            wire
        } else if mode == "http-error" {
            b"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n".to_vec()
        } else {
            let size = if mode == "length" {
                body.len() + 1
            } else {
                body.len()
            };
            let mut wire =
                format!("HTTP/1.1 200 OK\r\nContent-Length: {size}\r\nConnection: close\r\n\r\n")
                    .into_bytes();
            if mode == "truncated" {
                wire.extend(&body[..body.len() / 2]);
            } else {
                wire.extend(body);
            }
            wire
        };
        let _ = socket.write_all(&wire);
    });
    (url, worker)
}

#[test]
#[ignore = "real signer fixtures are created by the explicit native-updater workflow"]
fn signed_download_and_manifest_acceptance() {
    let root = fixture_root();
    let key = fs::read_to_string(root.join("updater.key.pub")).unwrap();
    let signature = fs::read_to_string(root.join("payload.bin.sig")).unwrap();
    let bytes = fs::read(root.join("payload.bin")).unwrap();
    let executable = PathBuf::from(fs::read_to_string(root.join("executable.txt")).unwrap());
    let (_app, good) = updater(&key, &executable);
    let raw = metadata(&signature, "1.0.1");
    let update = good.restore_update(raw.clone()).unwrap().unwrap();
    update.verify(&bytes).unwrap();
    let mut cases = vec!["valid-signature"];
    let mut corrupted = bytes.clone();
    corrupted[0] ^= 1;
    assert!(update.verify(&corrupted).is_err());
    cases.push("tampered-payload");
    assert!(update.verify(&bytes[..bytes.len() - 1]).is_err());
    cases.push("truncated-payload");
    let mismatch = good
        .restore_update(metadata(&signature, "1.0.2"))
        .unwrap()
        .unwrap();
    assert!(mismatch.verify(&bytes).is_err());
    cases.push("signed-version-mismatch");
    let no_version = fs::read_to_string(root.join("unversioned.bin.sig")).unwrap();
    assert!(good
        .restore_update(metadata(&no_version, "1.0.1"))
        .unwrap()
        .unwrap()
        .verify(&bytes)
        .is_err());
    cases.push("missing-signed-version");
    let wrong_key = fs::read_to_string(root.join("wrong.key.pub")).unwrap();
    let (_wrong_app, wrong) = updater(&wrong_key, &executable);
    assert!(wrong
        .restore_update(raw.clone())
        .unwrap()
        .unwrap()
        .verify(&bytes)
        .is_err());
    cases.push("wrong-key");
    let release = Release {
        tag_name: "v1.0.1".into(),
        draft: false,
        prerelease: false,
        body: None,
        assets: vec![Asset {
            name: "fixture.bin".into(),
            browser_download_url: update.download_url.to_string(),
            size: bytes.len(),
        }],
    };
    restore_candidate(&good, &release, &serde_json::to_vec(&raw).unwrap()).unwrap();
    cases.push("exact-release-manifest");
    let mut foreign = raw.clone();
    foreign["url"] = json!("https://evil.invalid/package");
    assert!(restore_candidate(&good, &release, &serde_json::to_vec(&foreign).unwrap()).is_err());
    cases.push("foreign-package-rejected");
    assert!(restore_candidate(&good, &release, &vec![b' '; MAX_METADATA + 1]).is_err());
    cases.push("oversized-manifest");
    let mut duplicate = release.clone();
    duplicate.assets.push(duplicate.assets[0].clone());
    assert!(restore_candidate(&good, &duplicate, &serde_json::to_vec(&raw).unwrap()).is_err());
    cases.push("duplicate-asset");
    assert!(restore_candidate(
        &good,
        &release,
        &serde_json::to_vec(&metadata(&signature, "1.0.2")).unwrap()
    )
    .is_err());
    cases.push("release-version-mismatch");
    let mut empty = release.clone();
    empty.assets[0].size = 0;
    assert!(restore_candidate(&good, &empty, &serde_json::to_vec(&raw).unwrap()).is_err());
    cases.push("zero-size-package");
    initialize_crypto_provider().unwrap();
    let client = reqwest::Client::builder()
        .no_proxy()
        .timeout(Duration::from_secs(3))
        .build()
        .unwrap();
    for (mode, success) in [
        ("normal", true),
        ("chunked", true),
        ("length", false),
        ("truncated", false),
        ("oversized", false),
        ("http-error", false),
        ("tampered", false),
    ] {
        let content = if mode == "tampered" {
            corrupted.clone()
        } else {
            bytes.clone()
        };
        let (url, server) = response_server(content, mode);
        let mut local = update.clone();
        local.download_url = url;
        let result =
            tauri::async_runtime::block_on(download_verified(&client, &local, bytes.len(), |_| {
                Ok(())
            }));
        server.join().unwrap();
        assert_eq!(
            result.is_ok(),
            success,
            "HTTP fixture {mode}: {:?}",
            result.as_ref().err()
        );
        if let Ok(downloaded) = result {
            assert_eq!(downloaded, bytes);
        }
        cases.push(mode);
    }
    fs::write(root.join("signed-acceptance.json"),serde_json::to_vec_pretty(&json!({"passed":true,"cases":cases,"uses_real_signatures":true,"production_key_changed":false})).unwrap()).unwrap();
}

#[test]
#[ignore = "runs in a child process because the real Windows installer exits its caller"]
fn native_install_probe() {
    let root = fixture_root();
    let config: serde_json::Value =
        serde_json::from_slice(&fs::read(root.join("install.json")).unwrap()).unwrap();
    let executable = PathBuf::from(config["executable"].as_str().unwrap());
    assert!(
        executable.starts_with(&root),
        "fixture may not replace an installed user application"
    );
    let key = fs::read_to_string(root.join("updater.key.pub")).unwrap();
    let package = PathBuf::from(config["package"].as_str().unwrap());
    assert!(package.starts_with(&root));
    let signature = fs::read_to_string(format!("{}.sig", package.display())).unwrap();
    let bytes = fs::read(&package).unwrap();
    let (_app, builder) = updater(&key, &executable);
    let update = builder
        .restore_update(metadata(&signature, "1.0.1"))
        .unwrap()
        .unwrap();
    let backend = BackendState::new(19081);
    // Simulate the native app's desired-running state, NOT a fake verifier or
    // fake installer: the exact library install call below replaces real files.
    backend.request_start();
    fs::write(root.join("installer-entered"), b"pending").unwrap();
    install_with_recovery(
        &backend,
        || update.verify(&bytes).map_err(|e| e.to_string()),
        || update.install(&bytes).map_err(|e| e.to_string()),
        |_| Ok(()),
    )
    .unwrap();
    fs::write(root.join("installer-returned"), b"complete").unwrap();
    backend.shutdown();
}
