#[cfg(not(target_os = "windows"))]
use tauri::menu::Submenu;
use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    AppHandle, Manager,
};

#[cfg(not(target_os = "windows"))]
fn setup_application_menu(app: &AppHandle) -> tauri::Result<()> {
    let app_show = MenuItem::with_id(app, "app-show", "Open OmniShare", true, None::<&str>)?;
    let app_update = MenuItem::with_id(app, "app-update", "检查更新…", true, None::<&str>)?;
    let app_quit = MenuItem::with_id(app, "app-quit", "Quit OmniShare", true, Some("CmdOrCtrl+Q"))?;
    let submenu =
        Submenu::with_items(app, "OmniShare", true, &[&app_show, &app_update, &app_quit])?;
    app.set_menu(Menu::with_items(app, &[&submenu])?)?;
    Ok(())
}

pub fn setup(app: &AppHandle) -> tauri::Result<()> {
    // A visible application menu makes Quit available even on desktops without
    // a visible tray. Native accelerators keep working after WebView navigation.
    #[cfg(not(target_os = "windows"))]
    setup_application_menu(app)?;
    app.on_menu_event(|app, event| match event.id().as_ref() {
        "app-show" => show_main_window(app),
        "app-update" => {
            open_updates_from_menu(app);
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
                open_updates_from_menu(app);
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

    #[cfg(target_os = "macos")]
    {
        let pixels = include_bytes!("../../icons/tray-template.rgba").to_vec();
        builder = builder
            .icon(tauri::image::Image::new_owned(pixels, 44, 44))
            .icon_as_template(true);
    }
    #[cfg(not(target_os = "macos"))]
    if let Some(icon) = app.default_window_icon() {
        builder = builder.icon(icon.clone());
    }

    builder.build(app)?;
    Ok(())
}

fn open_updates_from_menu(app: &AppHandle) {
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        if let Err(error) = crate::updates::show(app).await {
            eprintln!("Unable to open updates: {error}");
        }
    });
}

fn quit_application(app: &AppHandle) {
    // RunEvent::ExitRequested owns the shared, orderly backend shutdown path.
    app.exit(0);
}

pub fn show_main_window(app: &AppHandle) {
    // A late Dock/menu event must not bring a quitting application back.
    if let Some(state) = app.try_state::<crate::runtime::manager::BackendState>() {
        if state.is_exiting() {
            return;
        }
    }

    // NSApplication can be hidden independently of its window (Cmd+H).
    // Showing only the WebView does not clear that application-level state.
    #[cfg(target_os = "macos")]
    if let Err(error) = app.show() {
        eprintln!("Unable to unhide OmniShare: {error}");
    }

    // Reuse the hidden WebView so drafts, navigation and the backend survive.
    if let Some(window) = app.get_webview_window("main") {
        if let Err(error) = window.show() {
            eprintln!("Unable to show OmniShare window: {error}");
        }
        if let Err(error) = window.unminimize() {
            eprintln!("Unable to unminimize OmniShare window: {error}");
        }
        if let Err(error) = window.set_focus() {
            eprintln!("Unable to focus OmniShare window: {error}");
        }
    }
}

pub fn hide_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.hide();
    }
}
