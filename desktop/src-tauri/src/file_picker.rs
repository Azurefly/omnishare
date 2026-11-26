#[cfg(target_os = "macos")]
mod macos {
    use std::cell::RefCell;

    use block2::RcBlock;
    use objc2::MainThreadMarker;
    use objc2_app_kit::{
        NSModalResponse, NSModalResponseCancel, NSModalResponseOK, NSOpenPanel, NSWindow,
    };
    use objc2_foundation::NSString;
    use tauri::{Manager, Url, WebviewWindow};
    use tokio::sync::oneshot;

    use crate::{file_drop::is_workbench, runtime::manager::BackendState};

    type Selection = Result<Option<String>, String>;

    fn workbench_url(window: &WebviewWindow) -> Result<Url, String> {
        let url = window.url().map_err(|error| error.to_string())?;
        if window.label() == "main"
            && is_workbench(&url, &window.state::<BackendState>().status().url)
        {
            Ok(url)
        } else {
            Err("请在本机桌面工作台选择文件".into())
        }
    }

    fn present(window: &WebviewWindow, reply: oneshot::Sender<Selection>) {
        let owner = window.ns_window().ok().and_then(|ptr| {
            // Tauri owns this NSWindow. Borrow it on the main thread only to attach the sheet.
            unsafe { ptr.cast::<NSWindow>().as_ref() }
        });
        let (Some(owner), Some(mtm)) = (owner, MainThreadMarker::new()) else {
            let _ = reply.send(Err("当前窗口无法打开文件选择器".into()));
            return;
        };
        if owner.attachedSheet().is_some() {
            let _ = reply.send(Err("请先关闭当前系统对话框".into()));
            return;
        }
        let panel = NSOpenPanel::openPanel(mtm);
        panel.setCanChooseFiles(true);
        panel.setCanChooseDirectories(false);
        panel.setAllowsMultipleSelection(false);
        panel.setMessage(Some(&NSString::from_str("选择要添加到 OmniShare 的文件")));
        panel.setPrompt(Some(&NSString::from_str("选择文件")));
        let selected = panel.clone();
        let reply = RefCell::new(Some(reply));
        let did_close = RcBlock::new(move |response: NSModalResponse| {
            let result = if response == NSModalResponseOK {
                selected
                    .URL()
                    .and_then(|url| url.path())
                    .map(|path| Some(path.to_string()))
                    .ok_or_else(|| "无法读取所选文件的本机路径".to_string())
            } else if response == NSModalResponseCancel {
                Ok(None)
            } else {
                Err("系统文件选择器未能打开".into())
            };
            if let Some(reply) = reply.borrow_mut().take() {
                let _ = reply.send(result);
            }
        });
        panel.beginSheetModalForWindow_completionHandler(owner, &did_close);
    }

    pub async fn pick(window: WebviewWindow) -> Selection {
        let origin = workbench_url(&window)?;
        let (reply, result) = oneshot::channel();
        let owner = window.clone();
        window
            .run_on_main_thread(move || present(&owner, reply))
            .map_err(|e| e.to_string())?;
        let selection = result
            .await
            .map_err(|_| "系统文件选择器已关闭".to_string())??;
        if workbench_url(&window)? != origin {
            return Err("工作台页面已切换，请重新选择文件".into());
        }
        Ok(selection)
    }
}

#[tauri::command]
pub async fn pick_local_file(window: tauri::WebviewWindow) -> Result<Option<String>, String> {
    #[cfg(target_os = "macos")]
    {
        macos::pick(window).await
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = window;
        Err("当前平台使用系统文件选择器".into())
    }
}
