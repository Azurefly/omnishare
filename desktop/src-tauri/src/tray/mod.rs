use tauri::{
    menu::{Menu, MenuItem, Submenu},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    AppHandle, Manager,
};

pub fn setup(app: &AppHandle) -> tauri::Result<()> {
    // A visible application menu makes Quit available even on desktops without
    // a visible tray. Native accelerators keep working after WebView navigation.
    let app_show = MenuItem::with_id(app, "app-show", "Open OmniShare", true, None::<&str>)?;
    let app_update = MenuItem::with_id(app, "app-update", "检查更新…", true, None::<&str>)?;
    let app_quit = MenuItem::with_id(app, "app-quit", "Quit OmniShare", true, Some("CmdOrCtrl+Q"))?;
    let submenu =
        Submenu::with_items(app, "OmniShare", true, &[&app_show, &app_update, &app_quit])?;
    app.set_menu(Menu::with_items(app, &[&submenu])?)?;
    app.on_menu_event(|app, event| match event.id().as_ref() {
        "app-show" => show_main_window(app),
        "app-update" => {
            if let Err(error) = crate::updates::show(app) {
                eprintln!("Unable to open updates: {error}");
            }
        }
        "app-quit" => quit_application(app),
        _ => {}
    });

    let show = MenuItem::with_id(app, "show", "Open OmniShare", true, None::<&str>)?;
    let hide = MenuItem::with_id(app, "hide", "Hide Window", true, None::<&str>)?;
    let update = MenuItem::with_id(app, "update", "检查更新…", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "Quit OmniShare", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &hide, &update, &quit])?;

    let mut builder = TrayIconBuilder::with_id("omnishare-tray")
        .tooltip("OmniShare")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id().as_ref() {
            "show" => show_main_window(app),
            "hide" => hide_main_window(app),
            "update" => {
                if let Err(error) = crate::updates::show(app) {
                    eprintln!("Unable to open updates: {error}");
                }
            }
            "quit" => quit_application(app),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                show_main_window(tray.app_handle());
            }
        });

    if let Some(icon) = app.default_window_icon() {
        builder = builder.icon(icon.clone());
    }

    builder.build(app)?;
    Ok(())
}

fn quit_application(app: &AppHandle) {
    // RunEvent::ExitRequested owns the shared, orderly backend shutdown path.
    app.exit(0);
}

pub fn show_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

pub fn hide_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.hide();
    }
}
