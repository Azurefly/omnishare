#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![health])
        .run(tauri::generate_context!())
        .expect("error while running OmniShare desktop");
}

#[tauri::command]
fn health() -> String {
    "omnishare-desktop-ok".to_string()
}
