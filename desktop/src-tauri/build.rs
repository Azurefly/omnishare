fn main() {
    // Capabilities grant individual commands to the bootstrap, workbench and update windows.
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        tauri_build::AppManifest::new().commands(&[
            "health",
            "quit_desktop",
            "desktop_settings",
            "desktop_boot",
            "backend_status",
            "backend_start",
            "backend_stop",
            "open_updates",
            "pick_local_file",
            "updater_info",
            "check_update",
            "download_update",
            "install_update",
        ]),
    ))
    .expect("failed to generate desktop command permissions");
}
