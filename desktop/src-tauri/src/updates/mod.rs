//! Native, explicit-consent updates. LAN pages never receive an install command.
use std::time::Duration;

use reqwest::{redirect::Policy, Url};
use semver::Version;
use serde::{Deserialize, Serialize};
use tauri::{ipc::Channel, AppHandle, Manager, WebviewUrl, WebviewWindow, WebviewWindowBuilder};
use tauri_plugin_updater::{Update, UpdaterExt};

use crate::runtime::{manager::BackendState, startup};

const REPO: &str = "Azurefly/omnishare";
const API: &str = "https://api.github.com/repos/Azurefly/omnishare/releases?per_page=50";
const PUBLIC_KEY: &str = include_str!("../../updater-public-key.txt");
const MAX_METADATA: usize = 4 * 1024 * 1024;
const MAX_PACKAGE: usize = 512 * 1024 * 1024;

#[derive(Default)]
pub struct UpdateState {
    session: tokio::sync::Mutex<Session>,
}

#[derive(Default)]
struct Session {
    pending: Option<Update>,
    expected_size: usize,
    bytes: Option<Vec<u8>>,
}

#[derive(Debug, Clone, Serialize)]
pub struct UpdateView {
    current_version: String,
    version: Option<String>,
    notes: String,
    installable: bool,
    reason: String,
    preview: bool,
}

#[derive(Clone, Serialize)]
#[serde(tag = "event", rename_all = "camelCase")]
pub enum Progress {
    Downloading { received: usize, total: usize },
    Verifying,
    Ready,
}

#[derive(Debug, Clone, Deserialize)]
struct Release {
    tag_name: String,
    #[serde(default)]
    draft: bool,
    #[serde(default)]
    prerelease: bool,
    body: Option<String>,
    #[serde(default)]
    assets: Vec<Asset>,
}

#[derive(Debug, Clone, Deserialize)]
struct Asset {
    name: String,
    browser_download_url: String,
    size: usize,
}

fn parse_tag(tag: &str) -> Option<Version> {
    // Reject URL metacharacters, even if a future parser becomes more permissive.
    if !tag
        .bytes()
        .all(|b| b.is_ascii_alphanumeric() || b".-+".contains(&b))
    {
        return None;
    }
    Version::parse(tag.strip_prefix('v').unwrap_or(tag)).ok()
}

fn newest<'a>(releases: &'a [Release], current: &Version, preview: bool) -> Option<&'a Release> {
    releases
        .iter()
        .filter(|r| !r.draft)
        .filter_map(|r| {
            let version = parse_tag(&r.tag_name)?;
            if version <= *current || (!preview && (r.prerelease || !version.pre.is_empty())) {
                None
            } else {
                Some((version, r))
            }
        })
        .max_by(|(a, _), (b, _)| a.cmp(b))
        .map(|(_, release)| release)
}

fn release_asset(url: &Url, tag: &str) -> bool {
    let prefix = format!("/{REPO}/releases/download/{tag}/");
    url.scheme() == "https"
        && url.host_str() == Some("github.com")
        && url.port().is_none()
        && url.username().is_empty()
        && url.password().is_none()
        && url.query().is_none()
        && url.fragment().is_none()
        && url.path().strip_prefix(&prefix).is_some_and(|name| {
            !name.is_empty() && !name.contains('/') && !name.contains('%') && !name.contains('\\')
        })
}

fn redirect_policy() -> Policy {
    Policy::custom(|attempt| {
        let url = attempt.url();
        let trusted = matches!(
            url.host_str(),
            Some(
                "github.com"
                    | "api.github.com"
                    | "release-assets.githubusercontent.com"
                    | "objects.githubusercontent.com"
            )
        );
        if attempt.previous().len() >= 5
            || url.scheme() != "https"
            || !trusted
            || !url.username().is_empty()
            || url.password().is_some()
            || url.port().is_some()
        {
            attempt.error("update redirect is outside the official HTTPS release hosts")
        } else {
            attempt.follow()
        }
    })
}

fn client(timeout: Duration) -> Result<reqwest::Client, String> {
    reqwest::Client::builder()
        .user_agent("OmniShare-Desktop-Updater")
        .timeout(timeout)
        .connect_timeout(Duration::from_secs(15))
        .redirect(redirect_policy())
        .build()
        .map_err(|e| e.to_string())
}

async fn read_bounded(mut response: reqwest::Response, limit: usize) -> Result<Vec<u8>, String> {
    response = response
        .error_for_status()
        .map_err(|e| format!("更新服务请求失败：{e}"))?;
    if response.content_length().is_some_and(|n| n > limit as u64) {
        return Err("更新服务响应超出大小限制".into());
    }
    let mut bytes = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|e| e.to_string())? {
        if bytes.len().saturating_add(chunk.len()) > limit {
            return Err("更新服务响应超出大小限制".into());
        }
        bytes.extend_from_slice(&chunk);
    }
    Ok(bytes)
}

fn bundled_page(label: &str, url: &Url) -> bool {
    label == "updates"
        && url.path() == "/updates.html"
        && url.query().is_none()
        && url.fragment().is_none()
        && url.username().is_empty()
        && url.password().is_none()
        && ((url.scheme() == "tauri" && url.host_str() == Some("localhost"))
            || (url.scheme() == "http"
                && url.host_str() == Some("tauri.localhost")
                && url.port().is_none()))
}

fn require_update_window(window: &WebviewWindow) -> Result<(), String> {
    if !bundled_page(window.label(), &window.url().map_err(|e| e.to_string())?) {
        return Err("只有本机桌面更新窗口可以执行更新操作".into());
    }
    Ok(())
}

pub fn show(app: &AppHandle) -> Result<(), String> {
    if let Some(window) = app.get_webview_window("updates") {
        window.show().map_err(|e| e.to_string())?;
        return window.set_focus().map_err(|e| e.to_string());
    }
    WebviewWindowBuilder::new(app, "updates", WebviewUrl::App("updates.html".into()))
        .title("OmniShare · 软件更新")
        .inner_size(560.0, 620.0)
        .min_inner_size(460.0, 480.0)
        .center()
        .build()
        .map(|_| ())
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub fn open_updates(app: AppHandle, window: WebviewWindow) -> Result<(), String> {
    let url = window.url().map_err(|e| e.to_string())?;
    let backend = app.state::<BackendState>().status();
    let local = Url::parse(&backend.url).map_err(|e| e.to_string())?;
    let entry = window.label() == "main"
        && ((url.origin() == local.origin() && url.path() == "/")
            || (url.scheme() == "tauri" && url.host_str() == Some("localhost"))
            || (url.scheme() == "http" && url.host_str() == Some("tauri.localhost")));
    if !entry {
        return Err("更新入口只对本机桌面应用开放".into());
    }
    show(&app)
}

#[tauri::command]
pub fn updater_info(app: AppHandle, window: WebviewWindow) -> Result<UpdateView, String> {
    require_update_window(&window)?;
    let current = app.package_info().version.clone();
    Ok(UpdateView {
        current_version: current.to_string(),
        version: None,
        notes: String::new(),
        installable: false,
        reason: String::new(),
        preview: !current.pre.is_empty(),
    })
}

#[tauri::command]
pub async fn check_update(
    app: AppHandle,
    window: WebviewWindow,
    preview: bool,
) -> Result<UpdateView, String> {
    require_update_window(&window)?;
    let state = app.state::<UpdateState>();
    let mut session = state
        .session
        .try_lock()
        .map_err(|_| "更新操作正在进行，请勿重复点击")?;
    *session = Session::default(); // A failed check cannot leave an earlier install target active.
    let current = app.package_info().version.clone();
    let bytes = read_bounded(
        client(Duration::from_secs(25))?
            .get(API)
            .send()
            .await
            .map_err(|e| format!("检查更新失败，请检查网络后重试：{e}"))?,
        MAX_METADATA,
    )
    .await?;
    let releases: Vec<Release> =
        serde_json::from_slice(&bytes).map_err(|_| "更新服务返回的数据无效")?;
    let mut view = UpdateView {
        current_version: current.to_string(),
        version: None,
        notes: String::new(),
        installable: false,
        reason: "当前已是所选通道的最新版本".into(),
        preview,
    };
    if let Some(release) = newest(&releases, &current, preview) {
        let version = parse_tag(&release.tag_name).ok_or("发布版本号无效")?;
        view.version = Some(version.to_string());
        view.notes = release.body.clone().unwrap_or_default();
        if PUBLIC_KEY.trim().is_empty() {
            view.reason =
                "此安装包尚未配置可信更新公钥，不能执行自动安装。请安装发布者提供的签名更新版本。"
                    .into();
        } else if let Some(asset) = release.assets.iter().find(|a| a.name == "latest.json") {
            let endpoint =
                Url::parse(&asset.browser_download_url).map_err(|_| "更新清单地址无效")?;
            if !release_asset(&endpoint, &release.tag_name) || asset.size > MAX_METADATA {
                return Err("拒绝非官方或异常大小的更新清单".into());
            }
            let exit_app = app.clone();
            let updater = app
                .updater_builder()
                .pubkey(PUBLIC_KEY.trim())
                .endpoints(vec![endpoint])
                .map_err(|e| e.to_string())?
                .timeout(Duration::from_secs(25))
                .configure_client(|c| c.redirect(redirect_policy()))
                .on_before_exit(move || {
                    exit_app.state::<BackendState>().shutdown();
                })
                .build()
                .map_err(|e| e.to_string())?;
            let update = updater
                .check()
                .await
                .map_err(|e| format!("当前安装类型暂无可用签名更新：{e}"))?
                .ok_or("更新清单与发布版本不一致，请稍后重试")?;
            if parse_tag(&update.version) != Some(version)
                || !release_asset(&update.download_url, &release.tag_name)
            {
                return Err("更新清单指向不同版本或非官方安装包，已拒绝".into());
            }
            let package = release
                .assets
                .iter()
                .find(|a| a.browser_download_url == update.download_url.as_str())
                .ok_or("更新包不在本次发布附件中")?;
            if package.size == 0 || package.size > MAX_PACKAGE {
                return Err("更新包大小异常".into());
            }
            session.expected_size = package.size;
            session.pending = Some(update);
            view.installable = true;
            view.reason = "发现新版本。下载并验证后，再由你确认退出安装。".into();
        } else {
            view.reason =
                "发现新版本，但该发布尚未提供签名更新包；不会使用普通安装包绕过校验。".into();
        }
    }
    Ok(view)
}

#[tauri::command]
pub async fn download_update(
    app: AppHandle,
    window: WebviewWindow,
    on_event: Channel<Progress>,
) -> Result<(), String> {
    require_update_window(&window)?;
    let state = app.state::<UpdateState>();
    let mut session = state.session.try_lock().map_err(|_| "更新操作正在进行")?;
    session.bytes = None;
    let update = session.pending.as_ref().ok_or("请先检查更新")?;
    let mut response = client(Duration::from_secs(600))?
        .get(update.download_url.clone())
        .send()
        .await
        .map_err(|e| format!("下载失败，当前版本未改变：{e}"))?
        .error_for_status()
        .map_err(|e| e.to_string())?;
    let expected = session.expected_size;
    if response
        .content_length()
        .is_some_and(|n| n != expected as u64)
    {
        return Err("更新包长度与发布清单不一致".into());
    }
    let mut bytes = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|e| e.to_string())? {
        if bytes.len().saturating_add(chunk.len()) > expected {
            return Err("更新包超出声明大小".into());
        }
        bytes.extend_from_slice(&chunk);
        // Closing the native window cancels the download, never installs it.
        on_event
            .send(Progress::Downloading {
                received: bytes.len(),
                total: expected,
            })
            .map_err(|_| "更新窗口已关闭，下载已取消")?;
    }
    if bytes.len() != expected {
        return Err("更新包下载不完整，请重试".into());
    }
    let _ = on_event.send(Progress::Verifying);
    update
        .verify(&bytes)
        .map_err(|e| format!("更新签名或版本校验失败，未安装：{e}"))?;
    session.bytes = Some(bytes);
    let _ = on_event.send(Progress::Ready);
    Ok(())
}

#[tauri::command]
pub async fn install_update(
    app: AppHandle,
    window: WebviewWindow,
    confirmed: bool,
) -> Result<(), String> {
    require_update_window(&window)?;
    if !confirmed {
        return Err("安装前需要确认已保存编辑内容".into());
    }
    let state = app.state::<UpdateState>();
    let mut session = state.session.try_lock().map_err(|_| "更新操作正在进行")?;
    let update = session.pending.as_ref().ok_or("请先检查更新")?.clone();
    let bytes = session.bytes.take().ok_or("请先下载并验证更新包")?;
    let handle = app.clone();
    let result = tauri::async_runtime::spawn_blocking(move || -> Result<(), String> {
        // Re-verify immediately before any process or file changes.
        update.verify(&bytes).map_err(|e| e.to_string())?;
        let backend = handle.state::<BackendState>();
        let (port, resume) = backend.pause_for_update()?;
        match update.install(&bytes) {
            Ok(()) => {
                backend.shutdown();
                handle.restart();
            }
            Err(error) => {
                backend.resume_after_update(resume);
                if resume {
                    if let Err(recovery) = startup::initialize(&handle, backend.inner(), port) {
                        return Err(format!(
                            "更新未完成：{error}；恢复服务失败：{recovery}，请重新启动应用"
                        ));
                    }
                }
                Err(format!("安装未完成，原服务已恢复；数据目录未删除：{error}"))
            }
        }
    })
    .await
    .map_err(|e| e.to_string())?;
    session.pending = None;
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    fn release(tag: &str) -> Release {
        Release {
            tag_name: tag.into(),
            draft: false,
            prerelease: false,
            body: None,
            assets: vec![],
        }
    }
    #[test]
    fn semver_channels_do_not_downgrade_or_sort_lexically() {
        let mut draft = release("v99.0.0");
        draft.draft = true;
        let list = vec![release("v0.2.0-rc.10"), release("v0.2.0-rc.9"), draft];
        let current = Version::parse("0.2.0-rc.2").unwrap();
        assert_eq!(
            newest(&list, &current, true).unwrap().tag_name,
            "v0.2.0-rc.10"
        );
        assert!(newest(&list, &current, false).is_none());
        let stable = vec![release("v0.2.0"), release("v0.1.1")];
        assert_eq!(newest(&stable, &current, false).unwrap().tag_name, "v0.2.0");
        assert!(newest(&stable, &Version::parse("0.3.0").unwrap(), true).is_none());
    }
    #[test]
    fn only_bundled_update_window_can_install() {
        for url in [
            "tauri://localhost/updates.html",
            "http://tauri.localhost/updates.html",
        ] {
            assert!(bundled_page("updates", &Url::parse(url).unwrap()));
            assert!(!bundled_page("main", &Url::parse(url).unwrap()));
        }
        for url in [
            "http://127.0.0.1:8081/updates.html",
            "https://evil.example/updates.html",
            "http://tauri.localhost.evil/updates.html",
            "tauri://localhost/updates.html?url=x",
            "http://tauri.localhost:8081/updates.html",
        ] {
            assert!(!bundled_page("updates", &Url::parse(url).unwrap()));
        }
    }
    #[test]
    fn packages_are_bound_to_exact_official_release() {
        let tag = "v0.2.0-rc.3";
        assert!(release_asset(
            &Url::parse(
                "https://github.com/Azurefly/omnishare/releases/download/v0.2.0-rc.3/OmniShare.msi"
            )
            .unwrap(),
            tag
        ));
        for url in [
            "http://github.com/Azurefly/omnishare/releases/download/v0.2.0-rc.3/x",
            "https://github.com/Azurefly/other/releases/download/v0.2.0-rc.3/x",
            "https://github.com/Azurefly/omnishare/releases/download/v0.1.0/x",
            "https://github.com/Azurefly/omnishare/releases/download/v0.2.0-rc.3/x?token=a",
            "https://github.com/Azurefly/omnishare/releases/download/v0.2.0-rc.3/%2e%2e/x",
        ] {
            assert!(!release_asset(&Url::parse(url).unwrap(), tag));
        }
        assert!(parse_tag("v0.2.0/path").is_none());
        assert!(parse_tag("v0.2.0?evil").is_none());
    }
}
