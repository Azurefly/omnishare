//! Copied into a disposable CI source tree ONLY by native-upgrade-test.py.
//! Never referenced by production src/main.rs or a production Cargo feature.
use std::{fs, io::Write, path::PathBuf, time::{Duration, Instant}};
use tauri::{AppHandle, Manager};
use super::{check_update, download_update, install_update, show};

fn root() -> PathBuf {
    let config: serde_json::Value = serde_json::from_str(include_str!("fixture-config.json")).unwrap();
    PathBuf::from(config["root"].as_str().unwrap())
}

pub fn configure() {
    let config: serde_json::Value = serde_json::from_str(include_str!("fixture-config.json")).unwrap();
    std::env::set_var("OMNISHARE_DATA_DIR", root().join("data"));
    std::env::set_var("OMNISHARE_DESKTOP_LOG_DIR", root().join("logs"));
    std::env::set_var("OMNISHARE_PORT", config["port"].as_u64().unwrap().to_string());
}

fn record(name: &str, detail: serde_json::Value) {
    let mut file = fs::OpenOptions::new().create(true).append(true).open(root().join("events.jsonl")).unwrap();
    writeln!(file, "{}", serde_json::json!({"event":name,"detail":detail})).unwrap();
}
fn signal(name: &str) -> Result<(), String> {
    let deadline = Instant::now() + Duration::from_secs(180);
    while !root().join(name).exists() {
        if Instant::now() > deadline { return Err(format!("fixture signal timed out: {name}")); }
        std::thread::sleep(Duration::from_millis(100));
    }
    Ok(())
}
fn mode(name: &str) { fs::write(root().join("mode"), name).unwrap(); }
fn running(app: &AppHandle) -> Result<(), String> {
    let backend = app.state::<crate::runtime::manager::BackendState>();
    let status = backend.status();
    if status.running && backend.should_run() && crate::runtime::health::is_omnishare_backend("127.0.0.1", status.port) {
        Ok(())
    } else { Err("backend stopped before explicit install".into()) }
}
async fn ready_window(app: &AppHandle) -> Result<tauri::WebviewWindow, String> {
    let deadline = Instant::now() + Duration::from_secs(20);
    loop {
        if let Some(window) = app.get_webview_window("updates") {
            if super::require_update_window(&window).is_ok() { return Ok(window); }
        }
        if Instant::now() > deadline { return Err("bundled update page did not finish navigating".into()); }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
}

async fn run(app: AppHandle) -> Result<(), String> {
    // Wait for the real managed SQLite backend, rather than seeding a mock.
    let deadline = Instant::now() + Duration::from_secs(45);
    while running(&app).is_err() {
        if Instant::now() > deadline { return Err("managed backend not ready".into()); }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    let version = app.package_info().version.to_string();
    let status = app.state::<crate::runtime::manager::BackendState>().status();
    record("started", serde_json::json!({"version":version,"port":status.port,"pid":std::process::id()}));
    fs::write(root().join(if version.ends_with("rc.2") {"restarted.json"} else {"started.json"}),
        serde_json::to_vec(&serde_json::json!({"version":version,"port":status.port,"pid":std::process::id()})).unwrap()).unwrap();
    if version.ends_with("rc.2") {
        tauri::async_runtime::spawn_blocking(|| signal("finish")).await.map_err(|e|e.to_string())??;
        app.exit(0);
        return Ok(());
    }
    tauri::async_runtime::spawn_blocking(|| signal("begin")).await.map_err(|e|e.to_string())??;
    show(app.clone()).await?;
    let window = ready_window(&app).await?;
    // These execute the production Rust commands and real verifier/installer.
    // No mock Update, synthetic verifier or production signing key is used.
    for failure in ["tampered", "wrong-key", "wrong-signed-version", "truncated"] {
        mode(failure);
        let view = check_update(app.clone(), window.clone(), true).await?;
        if !view.installable { return Err(format!("fixture is not installable: {}",view.reason)); }
        let result = download_update(app.clone(), window.clone(), tauri::ipc::Channel::new(|_| Ok(()))).await;
        if result.is_ok() { return Err(format!("accepted invalid update: {failure}")); }
        if install_update(app.clone(),window.clone(),true).await.is_ok() {return Err("invalid package reached install".into());}
        running(&app)?;
        record("rejected", serde_json::json!({"case":failure,"reason":result.unwrap_err(),"backend_running":true}));
    }
    mode("invalid-installer");
    check_update(app.clone(),window.clone(),true).await?;
    download_update(app.clone(),window.clone(),tauri::ipc::Channel::new(|_|Ok(()))).await?;
    if install_update(app.clone(),window.clone(),true).await.is_ok() {return Err("accepted invalid installer".into());}
    running(&app)?;
    record("recovered",serde_json::json!({"case":"signed-invalid-installer","backend_running":true}));
    mode("oversized-manifest");
    if check_update(app.clone(),window.clone(),true).await.is_ok() {return Err("accepted oversized manifest".into());}
    running(&app)?;
    record("rejected", serde_json::json!({"case":"oversized-manifest","backend_running":true}));

    mode("slow");
    check_update(app.clone(),window.clone(),true).await?;
    let closing = window.clone();
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(Duration::from_millis(300)).await;
        let _ = closing.close();
    });
    let result=download_update(app.clone(),window.clone(),tauri::ipc::Channel::new(|_| Ok(()))).await;
    if result.is_ok() {return Err("closed window accepted download".into());}
    running(&app)?;
    record("cancelled",serde_json::json!({"backend_running":true}));
    let deadline = Instant::now()+Duration::from_secs(10);
    while app.get_webview_window("updates").is_some() {
        if Instant::now()>deadline {return Err("window not destroyed".into());}
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    mode("normal");
    show(app.clone()).await?;
    let window=ready_window(&app).await?;
    let view=check_update(app.clone(),window.clone(),true).await?;
    if view.version.as_deref()!=Some("0.0.0-rc.2") || !view.installable {return Err("wrong upgrade candidate".into());}
    download_update(app.clone(),window.clone(),tauri::ipc::Channel::new(|_|Ok(()))).await?;
    if install_update(app.clone(),window.clone(),false).await.is_ok() {return Err("installed without confirmation".into());}
    running(&app)?;
    record("verified-awaiting-confirmation",serde_json::json!({"running_version":version,"backend_running":true}));
    fs::write(root().join("ready"), b"verified").unwrap();
    tauri::async_runtime::spawn_blocking(||signal("confirm")).await.map_err(|e|e.to_string())??;
    record("installing",serde_json::json!({"confirmed":true}));
    install_update(app.clone(),window,true).await?;
    Err("native install returned without restarting".into())
}

pub fn start(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        if let Err(error) = run(app.clone()).await {
            record("error",serde_json::json!({"message":error}));
            let _=fs::write(root().join("failure.txt"),error);
            app.exit(2);
        }
    });
}
