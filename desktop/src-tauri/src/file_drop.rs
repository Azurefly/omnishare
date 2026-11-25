use std::path::PathBuf;

use tauri::{Manager, Url, Window};

use crate::runtime::manager::BackendState;

fn accepts_paths(url: &Url, backend_url: &str) -> bool {
    let bootstrap = (url.scheme() == "tauri" && url.host_str() == Some("localhost"))
        || (url.scheme() == "http" && url.host_str() == Some("tauri.localhost"));
    if !matches!(url.path(), "/" | "/index.html") {
        return false;
    }
    bootstrap || Url::parse(backend_url).is_ok_and(|base| base.origin() == url.origin())
}

pub fn deliver(window: &Window, paths: &[PathBuf]) {
    let Some(webview) = window.get_webview_window("main") else {
        return;
    };
    let Ok(url) = webview.url() else { return };
    if !accepts_paths(&url, &window.state::<BackendState>().status().url) {
        return;
    }
    // Recheck the document at execution time in case navigation raced the drop.
    // Only user-dropped paths enter the page. No filesystem IPC is exposed.
    let href = serde_json::json!(url.as_str());
    let detail = if paths.len() > 100 {
        serde_json::json!({"error": "每次最多可拖入 100 项"})
    } else if let Ok(paths) = serde_json::to_value(paths) {
        serde_json::json!({"paths": paths})
    } else {
        serde_json::json!({"error": "文件路径无法显示，请使用文件选择器"})
    };
    let _ = webview.eval(format!(
        "if(location.href === {href}) window.dispatchEvent(new CustomEvent('omnishare:file-drop', {{detail:{detail}}}));"
    ));
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn drop_only_reaches_the_workbench_and_setup() {
        let base = "http://127.0.0.1:8081";
        for (url, allowed) in [
            ("tauri://localhost/index.html", true),
            ("http://tauri.localhost/", true),
            ("http://127.0.0.1:8081/", true),
            ("http://127.0.0.1:8082/", false),
            ("http://127.0.0.1:8081/s/public", false),
            ("https://example.org/", false),
        ] {
            assert_eq!(accepts_paths(&Url::parse(url).unwrap(), base), allowed);
        }
    }
}
