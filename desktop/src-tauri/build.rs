fn main() {
    // Every application command participates in the ACL. The loopback page
    // may only open the bundled update window, never download or install.
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
            "updater_info",
            "check_update",
            "download_update",
            "install_update",
        ]),
    ))
    .expect("failed to generate desktop command permissions");
}
