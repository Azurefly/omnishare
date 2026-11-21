//! Native, explicit-consent updates. LAN pages never receive an install command.
use std::{
    fs,
    future::Future,
    io::Write,
    path::{Path, PathBuf},
    pin::pin,
    process::Command,
    task::Poll,
    time::Duration,
};

use reqwest::{redirect::Policy, Url};
use semver::Version;
use serde::{Deserialize, Serialize};
use tauri::{ipc::Channel, AppHandle, Manager, WebviewUrl, WebviewWindow, WebviewWindowBuilder};
use tauri_plugin_updater::{Update, Updater, UpdaterExt};

use crate::runtime::{manager::BackendState, startup};

mod transfer;

const REPO: &str = "Azurefly/omnishare";
const API: &str = "https://api.github.com/repos/Azurefly/omnishare/releases?per_page=50";
const PUBLIC_KEY: &str = include_str!("../../updater-public-key.txt");
const MAX_METADATA: usize = 4 * 1024 * 1024;
const MAX_PACKAGE: usize = 512 * 1024 * 1024;

pub struct UpdateState {
    window_creation: std::sync::Mutex<()>,
    session: tokio::sync::Mutex<Session>,
    closed: tokio::sync::watch::Sender<u64>,
}

impl Default for UpdateState {
    fn default() -> Self {
        let (closed, _) = tokio::sync::watch::channel(0);
        Self {
            window_creation: std::sync::Mutex::new(()),
            session: tokio::sync::Mutex::new(Session::default()),
            closed,
        }
    }
}

impl UpdateState {
    /// A destroyed update window cancels only reads/downloads, not an already
    /// confirmed native installation. Never depend on a queued JS event failing.
    pub fn cancel_reads(&self) {
        self.closed
            .send_modify(|epoch| *epoch = epoch.wrapping_add(1));
        if let Ok(mut session) = self.session.try_lock() {
            *session = Session::default();
        }
    }
}

async fn while_window_open<T>(
    mut closed: tokio::sync::watch::Receiver<u64>,
    work: impl Future<Output = Result<T, String>>,
) -> Result<T, String> {
    let mut work = pin!(work);
    let mut cancellation = pin!(closed.changed());
    std::future::poll_fn(|cx| {
        // Prefer cancellation if a network result and destruction race. Dropping
        // the losing future drops the HTTP response/connection immediately.
        if cancellation.as_mut().poll(cx).is_ready() {
            return Poll::Ready(Err("更新窗口已关闭，操作已取消；当前版本未改变".into()));
        }
        let result = work.as_mut().poll(cx);
        if result.is_ready() && cancellation.as_mut().poll(cx).is_ready() {
            return Poll::Ready(Err("更新窗口已关闭，操作已取消；当前版本未改变".into()));
        }
        result
    })
    .await
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(dead_code)]
enum PackageKind {
    WindowsNsis,
    WindowsMsi,
    MacOsDmg,
    LinuxAppImage,
    LinuxDeb,
}

#[derive(Clone, Debug)]
struct ReleasePackage {
    name: String,
    download_url: Url,
    expected_size: usize,
    expected_sha256: String,
    kind: PackageKind,
}

enum PendingTarget {
    Signed(Box<Update>),
    Package(ReleasePackage),
}

#[derive(Default)]
struct Session {
    pending: Option<PendingTarget>,
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
            if !version.cmp_precedence(current).is_gt()
                || (!preview && (r.prerelease || !version.pre.is_empty()))
            {
                None
            } else {
                Some((version, r))
            }
        })
        .max_by(|(a, _), (b, _)| a.cmp_precedence(b))
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

// reqwest 0.13 with rustls-no-provider panics during client construction
// unless the process-wide provider is installed explicitly. Keep the chosen
// ring backend; do not disable TLS verification or depend on another plugin.
fn initialize_crypto_provider() -> Result<(), String> {
    if rustls::crypto::CryptoProvider::get_default().is_none() {
        // A concurrent caller may install it between these operations.
        let _ = rustls::crypto::ring::default_provider().install_default();
    }
    rustls::crypto::CryptoProvider::get_default()
        .map(|_| ())
        .ok_or_else(|| "无法初始化更新服务的 TLS 加密后端".to_string())
}

fn client(timeout: Duration) -> Result<reqwest::Client, String> {
    initialize_crypto_provider()?;
    reqwest::Client::builder()
        .user_agent("OmniShare-Desktop-Updater")
        .timeout(timeout)
        .connect_timeout(Duration::from_secs(15))
        .redirect(redirect_policy())
        .build()
        .map_err(|e| e.to_string())
}

async fn read_bounded(response: reqwest::Response, limit: usize) -> Result<Vec<u8>, String> {
    transfer::read_response(response, None, limit, |_| Ok(())).await
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

// WebView2 window creation must never block the synchronous UI command/menu
// dispatcher. Serialize creation on a worker, so repeated menu/button clicks
// also reuse a single window rather than racing to create the same label.
pub async fn show(app: AppHandle) -> Result<(), String> {
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<UpdateState>();
        let _creation = state
            .window_creation
            .lock()
            .map_err(|_| "更新窗口状态不可用，请重启应用")?;
        if let Some(window) = app.get_webview_window("updates") {
            window.show().map_err(|e| e.to_string())?;
            return window.set_focus().map_err(|e| e.to_string());
        }
        WebviewWindowBuilder::new(&app, "updates", WebviewUrl::App("updates.html".into()))
            .title("OmniShare · 软件更新")
            .inner_size(560.0, 620.0)
            .min_inner_size(460.0, 480.0)
            .center()
            .on_navigation(|url| bundled_page("updates", url))
            .build()
            .map(|_| ())
            .map_err(|e| e.to_string())
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub async fn open_updates(app: AppHandle, window: WebviewWindow) -> Result<(), String> {
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
    show(app).await
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
    let closed = state.closed.subscribe();
    let mut session = state
        .session
        .try_lock()
        .map_err(|_| "更新操作正在进行，请勿重复点击")?;
    *session = Session::default(); // A failed check cannot leave an earlier install target active.
    let result = while_window_open(closed, async {
        transfer::with_deadline(Duration::from_secs(30), async {
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
                reason: if !preview && newest(&releases, &current, true).is_some() {
                    "当前已是稳定版最新；勾选“接收候选版更新”可升级候选版".into()
                } else {
                    "当前已是所选通道的最新版本".into()
                },
                preview,
            };
            if let Some(release) = newest(&releases, &current, preview) {
                view.version = Some(
                    parse_tag(&release.tag_name)
                        .ok_or("发布版本号无效")?
                        .to_string(),
                );
                view.notes = release.body.clone().unwrap_or_default();
                let signed_asset = if PUBLIC_KEY.trim().is_empty() {
                    None
                } else {
                    release.assets.iter().find(|a| a.name == "latest.json")
                };
                if let Some(asset) = signed_asset {
                    let endpoint =
                        Url::parse(&asset.browser_download_url).map_err(|_| "更新清单地址无效")?;
                    if !release_asset(&endpoint, &release.tag_name) || asset.size > MAX_METADATA {
                        return Err("拒绝非官方或异常大小的更新清单".into());
                    }
                    let updater = app
                        .updater_builder()
                        .pubkey(PUBLIC_KEY.trim())
                        .endpoints(vec![endpoint.clone()])
                        .map_err(|e| e.to_string())?
                        .timeout(Duration::from_secs(25))
                        .configure_client(|c| c.redirect(redirect_policy()))
                        // Backend is paused by install_with_recovery. Do not destroy the UI
                        // before Windows has successfully launched its installer.
                        .on_before_exit(|| {})
                        .build()
                        .map_err(|e| e.to_string())?;
                    // Bound the actual manifest body too, not just GitHub's size field.
                    // restore_update uses the plugin's exact target/version selection and
                    // requires a cryptographically signed version without a second fetch.
                    let manifest = read_bounded(
                        client(Duration::from_secs(25))?
                            .get(endpoint)
                            .send()
                            .await
                            .map_err(|e| format!("读取更新清单失败：{e}"))?,
                        MAX_METADATA,
                    )
                    .await?;
                    if manifest.len() != asset.size {
                        return Err("更新清单长度与发布附件不一致".into());
                    }
                    let (update, expected_size) = restore_candidate(&updater, release, &manifest)?;
                    session.expected_size = expected_size;
                    session.pending = Some(PendingTarget::Signed(Box::new(update)));
                    view.installable = true;
                    view.reason = "发现新版本。下载并验证后，再由你确认退出安装。".into();
                } else if let Some(sums_asset) =
                    release.assets.iter().find(|a| a.name == "SHA256SUMS.txt")
                {
                    let raw_sums_url = &sums_asset.browser_download_url;
                    let sums_url = Url::parse(raw_sums_url).map_err(|_| "更新校验文件地址无效")?;
                    if !release_asset(&sums_url, &release.tag_name)
                        || sums_asset.size == 0
                        || sums_asset.size > MAX_METADATA
                    {
                        return Err("拒绝非官方或异常大小的校验清单".into());
                    }
                    let sums = read_bounded(
                        client(Duration::from_secs(25))?
                            .get(sums_url)
                            .send()
                            .await
                            .map_err(|e| format!("读取校验清单失败：{e}"))?,
                        MAX_METADATA,
                    )
                    .await?;
                    if sums.len() != sums_asset.size {
                        return Err("校验清单长度与发布附件不一致".into());
                    }
                    let candidates = preferred_package_candidates();
                    let pkg = resolve_release_package(release, &sums, &candidates)?;
                    session.expected_size = pkg.expected_size;
                    session.pending = Some(PendingTarget::Package(pkg));
                    view.installable = true;
                    view.reason = "发现新版本。点击下方按钮下载并验证安装包后即可升级。".into();
                } else {
                    view.reason = String::from("发现新版本，但缺少 SHA256SUMS.txt 校验清单。");
                }
            }
            Ok(view)
        })
        .await
    })
    .await;
    if result.is_err() {
        *session = Session::default();
    }
    result
}

#[tauri::command]
pub async fn download_update(
    app: AppHandle,
    window: WebviewWindow,
    on_event: Channel<Progress>,
) -> Result<(), String> {
    require_update_window(&window)?;
    let state = app.state::<UpdateState>();
    let closed = state.closed.subscribe();
    let mut session = state.session.try_lock().map_err(|_| "更新操作正在进行")?;
    session.bytes = None;
    let result = while_window_open(closed, async {
        let target = session.pending.as_ref().ok_or("请先检查更新")?;
        let http = client(Duration::from_secs(600))?;
        let bytes = match target {
            PendingTarget::Signed(update) => {
                let fut = download_verified(&http, update, session.expected_size, |event| {
                    on_event
                        .send(event)
                        .map_err(|_| "更新窗口已关闭，下载已取消".to_string())
                });
                fut.await?
            }
            PendingTarget::Package(pkg) => {
                let fut = download_package_verified(&http, pkg, |event| {
                    on_event
                        .send(event)
                        .map_err(|_| "更新窗口已关闭，下载已取消".to_string())
                });
                fut.await?
            }
        };
        session.bytes = Some(bytes);
        let _ = on_event.send(Progress::Ready);
        Ok(())
    })
    .await;
    if result.is_err() {
        *session = Session::default();
    }
    result
}

// Both native commands and signed-package integration tests use these paths.
fn restore_candidate(
    updater: &Updater,
    release: &Release,
    manifest: &[u8],
) -> Result<(Update, usize), String> {
    if manifest.len() > MAX_METADATA {
        return Err("更新清单超出大小限制".into());
    }
    let json = serde_json::from_slice(manifest).map_err(|_| "更新清单不是有效 JSON")?;
    let update = updater
        .restore_update(json)
        .map_err(|e| format!("当前安装类型暂无可用签名更新：{e}"))?
        .ok_or("更新清单与发布版本不一致")?;
    if parse_tag(&update.version) != parse_tag(&release.tag_name)
        || !release_asset(&update.download_url, &release.tag_name)
    {
        return Err("更新清单指向不同版本或非官方安装包，已拒绝".into());
    }
    let mut matches = release
        .assets
        .iter()
        .filter(|a| a.browser_download_url == update.download_url.as_str());
    let package = matches.next().ok_or("更新包不在本次发布附件中")?;
    if matches.next().is_some() || package.size == 0 || package.size > MAX_PACKAGE {
        return Err("更新包大小异常或发布附件重复".into());
    }
    Ok((update, package.size))
}

async fn download_verified(
    http: &reqwest::Client,
    update: &Update,
    expected: usize,
    mut report: impl FnMut(Progress) -> Result<(), String>,
) -> Result<Vec<u8>, String> {
    if expected == 0 || expected > MAX_PACKAGE {
        return Err("更新包大小异常".into());
    }
    let response = http
        .get(update.download_url.clone())
        .send()
        .await
        .map_err(|e| format!("下载失败，当前版本未改变：{e}"))?;
    let bytes = transfer::read_response(response, Some(expected), MAX_PACKAGE, |received| {
        report(Progress::Downloading {
            received,
            total: expected,
        })
    })
    .await?;
    report(Progress::Verifying)?;
    update
        .verify(&bytes)
        .map_err(|e| format!("更新签名或版本校验失败，未安装：{e}"))?;
    Ok(bytes)
}

/// Do not mark the application as exiting until installation has succeeded.
/// On Windows on_before_exit runs BEFORE ShellExecuteW, which can still fail
/// (e.g. UAC cancellation). A terminal shutdown there prevents recovery.
fn install_with_recovery(
    backend: &BackendState,
    verify: impl FnOnce() -> Result<(), String>,
    install: impl FnOnce() -> Result<(), String>,
    recover: impl FnOnce(u16) -> Result<(), String>,
) -> Result<(), String> {
    verify()?;
    let (port, resume) = backend.pause_for_update()?;
    let installed = std::panic::catch_unwind(std::panic::AssertUnwindSafe(install))
        .unwrap_or_else(|_| Err("安装器意外中断".to_string()));
    if let Err(error) = installed {
        backend.resume_after_update(resume);
        if resume {
            recover(port).map_err(|recovery| {
                format!("安装未完成：{error}；恢复服务失败：{recovery}，请重新启动应用")
            })?;
        }
        let state = if resume {
            "本机服务已恢复"
        } else {
            "本机服务保持原先的停止状态"
        };
        return Err(format!("安装未完成：{error}；{state}；数据目录未删除"));
    }
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
    if session.pending.is_none() || session.bytes.is_none() {
        return Err("请先下载并验证更新包".into());
    }
    let mut ready = std::mem::take(&mut *session);
    let target = ready.pending.take().ok_or("请先检查更新")?;
    let bytes = ready.bytes.take().ok_or("请先下载并验证更新包")?;
    let handle = app.clone();
    tauri::async_runtime::spawn_blocking(move || -> Result<(), String> {
        let backend = handle.state::<BackendState>();
        match target {
            PendingTarget::Signed(update) => install_with_recovery(
                backend.inner(),
                || {
                    update
                        .verify(&bytes)
                        .map_err(|e| format!("安装前签名复验失败：{e}"))
                },
                || update.install(&bytes).map_err(|e| e.to_string()),
                |port| startup::initialize(&handle, backend.inner(), port).map(|_| ()),
            )?,
            PendingTarget::Package(pkg) => install_with_recovery(
                backend.inner(),
                || pkg.verify(&bytes),
                || pkg.install(&bytes),
                |port| startup::initialize(&handle, backend.inner(), port).map(|_| ()),
            )?,
        }
        // Windows hands off to its installer and exits inside install(); Unix
        // returns here after replacement. Only now make shutdown terminal.
        backend.shutdown();
        handle.restart();
    })
    .await
    .map_err(|e| format!("安装任务异常，请检查应用状态：{e}"))?
}

fn sha256_hex(data: &[u8]) -> String {
    use std::fmt::Write as _;

    const K: [u32; 64] = [
        0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4,
        0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe,
        0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f,
        0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
        0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc,
        0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
        0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116,
        0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
        0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7,
        0xc67178f2,
    ];
    let mut h: [u32; 8] = [
        0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab,
        0x5be0cd19,
    ];
    let bit_len = (data.len() as u64).wrapping_mul(8);
    let pad_len = (119 - (data.len() % 64)) % 64;
    let total = data.len() + 1 + pad_len + 8;
    let mut padded = Vec::with_capacity(total);
    padded.extend_from_slice(data);
    padded.push(0x80);
    padded.resize(total - 8, 0);
    padded.extend_from_slice(&bit_len.to_be_bytes());
    for chunk in padded.as_chunks::<64>().0 {
        let mut w = [0u32; 64];
        for (slot, word) in w[..16].iter_mut().zip(chunk.as_chunks::<4>().0) {
            *slot = u32::from_be_bytes(*word);
        }
        for i in 16..64 {
            let s0 = w[i - 15].rotate_right(7) ^ w[i - 15].rotate_right(18) ^ (w[i - 15] >> 3);
            let s1 = w[i - 2].rotate_right(17) ^ w[i - 2].rotate_right(19) ^ (w[i - 2] >> 10);
            w[i] = w[i - 16]
                .wrapping_add(s0)
                .wrapping_add(w[i - 7])
                .wrapping_add(s1);
        }
        let [mut a, mut b, mut c, mut d, mut e, mut f, mut g, mut hh] = h;
        for (&k, &wi) in K.iter().zip(w.iter()) {
            let s1 = e.rotate_right(6) ^ e.rotate_right(11) ^ e.rotate_right(25);
            let ch = (e & f) ^ (!e & g);
            let t1 = hh.wrapping_add(s1).wrapping_add(ch);
            let temp1 = t1.wrapping_add(k).wrapping_add(wi);
            let s0 = a.rotate_right(2) ^ a.rotate_right(13) ^ a.rotate_right(22);
            let maj = (a & b) ^ (a & c) ^ (b & c);
            let temp2 = s0.wrapping_add(maj);
            hh = g;
            g = f;
            f = e;
            e = d.wrapping_add(temp1);
            d = c;
            c = b;
            b = a;
            a = temp1.wrapping_add(temp2);
        }
        for (slot, delta) in h.iter_mut().zip([a, b, c, d, e, f, g, hh]) {
            *slot = slot.wrapping_add(delta);
        }
    }
    let mut out = String::with_capacity(64);
    for word in h {
        let _ = write!(&mut out, "{word:08x}");
    }
    out
}

fn parse_sha256_for_asset(sums_text: &str, asset_name: &str) -> Option<String> {
    for line in sums_text.lines() {
        let trimmed = line.trim();
        let Some((hash, file)) = trimmed.split_once(char::is_whitespace) else {
            continue;
        };
        let file = file.trim().trim_start_matches('*');
        if file == asset_name && hash.len() == 64 && hash.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Some(hash.to_ascii_lowercase());
        }
    }
    None
}

fn preferred_package_candidates() -> Vec<(&'static str, PackageKind)> {
    #[cfg(target_os = "windows")]
    {
        let has_nsis = std::env::current_exe()
            .ok()
            .and_then(|p| p.parent().map(|dir| dir.join("uninstall.exe").is_file()))
            .unwrap_or(true);
        if has_nsis {
            vec![
                ("-windows-x64-setup.exe", PackageKind::WindowsNsis),
                ("-windows-x64.msi", PackageKind::WindowsMsi),
            ]
        } else {
            vec![
                ("-windows-x64.msi", PackageKind::WindowsMsi),
                ("-windows-x64-setup.exe", PackageKind::WindowsNsis),
            ]
        }
    }
    #[cfg(target_os = "macos")]
    {
        vec![("-macos-universal.dmg", PackageKind::MacOsDmg)]
    }
    #[cfg(target_os = "linux")]
    {
        let exe = std::env::current_exe().ok();
        let exe_name = exe.as_deref().and_then(Path::file_name);
        let is_appimage = std::env::var_os("APPIMAGE").is_some()
            || exe_name.is_some_and(|n| n.to_string_lossy().ends_with(".AppImage"));
        if is_appimage {
            vec![
                ("-linux-amd64.AppImage", PackageKind::LinuxAppImage),
                ("-ubuntu-amd64.deb", PackageKind::LinuxDeb),
            ]
        } else {
            vec![
                ("-ubuntu-amd64.deb", PackageKind::LinuxDeb),
                ("-linux-amd64.AppImage", PackageKind::LinuxAppImage),
            ]
        }
    }
    #[cfg(not(any(target_os = "windows", target_os = "macos", target_os = "linux")))]
    {
        Vec::new()
    }
}

fn resolve_release_package(
    release: &Release,
    sums_bytes: &[u8],
    candidates: &[(&str, PackageKind)],
) -> Result<ReleasePackage, String> {
    let sums_text = std::str::from_utf8(sums_bytes).map_err(|_| "校验清单编码无效".to_string())?;
    let prefix = format!("OmniShare-{}-", release.tag_name);
    for (suffix, kind) in candidates {
        let mut matches = release
            .assets
            .iter()
            .filter(|a| a.name.starts_with(&prefix) && a.name.ends_with(suffix));
        let Some(asset) = matches.next() else {
            continue;
        };
        if matches.next().is_some() || asset.size == 0 || asset.size > MAX_PACKAGE {
            return Err("发布安装包附件重复或大小异常，已拒绝".into());
        }
        let download_url = Url::parse(&asset.browser_download_url).map_err(|_| "安装包地址无效")?;
        if !release_asset(&download_url, &release.tag_name) {
            return Err("安装包地址不属于官方 Release 附件，已拒绝".into());
        }
        let expected_sha256 = parse_sha256_for_asset(sums_text, &asset.name)
            .ok_or_else(|| format!("校验清单缺少 {} 的 SHA-256 记录", asset.name))?;
        return Ok(ReleasePackage {
            name: asset.name.clone(),
            download_url,
            expected_size: asset.size,
            expected_sha256,
            kind: *kind,
        });
    }
    Err("该版本尚未包含适用于当前系统的桌面安装包".into())
}

async fn download_package_verified(
    http: &reqwest::Client,
    pkg: &ReleasePackage,
    mut report: impl FnMut(Progress) -> Result<(), String>,
) -> Result<Vec<u8>, String> {
    if pkg.expected_size == 0 || pkg.expected_size > MAX_PACKAGE {
        return Err("更新包大小异常".into());
    }
    let response = http
        .get(pkg.download_url.clone())
        .send()
        .await
        .map_err(|e| format!("下载失败，当前版本未改变：{e}"))?;
    let total = pkg.expected_size;
    let bytes = transfer::read_response(response, Some(total), MAX_PACKAGE, |received| {
        report(Progress::Downloading { received, total })
    })
    .await?;
    report(Progress::Verifying)?;
    pkg.verify(&bytes)?;
    Ok(bytes)
}

#[cfg(any(target_os = "windows", target_os = "macos", target_os = "linux"))]
fn write_temp_file(name: &str, bytes: &[u8]) -> Result<PathBuf, String> {
    let file_name = Path::new(name).file_name();
    let safe_name = file_name.ok_or_else(|| "安装包文件名无效".to_string())?;
    let pid = std::process::id();
    let lossy = safe_name.to_string_lossy();
    let path = std::env::temp_dir().join(format!("omnishare-update-{pid}-{lossy}"));
    let created = fs::File::create(&path);
    let mut file = created.map_err(|e| format!("无法写入安装包：{e}"))?;
    file.write_all(bytes)
        .map_err(|e| format!("写入安装包失败：{e}"))?;
    file.sync_all()
        .map_err(|e| format!("刷新安装包磁盘缓存失败：{e}"))?;
    Ok(path)
}

impl ReleasePackage {
    fn verify(&self, bytes: &[u8]) -> Result<(), String> {
        if bytes.len() != self.expected_size {
            return Err("更新包大小与校验清单不一致，未安装".into());
        }
        if sha256_hex(bytes) != self.expected_sha256 {
            return Err("更新包 SHA-256 校验失败，未安装".into());
        }
        Ok(())
    }

    fn install(&self, bytes: &[u8]) -> Result<(), String> {
        self.verify(bytes)?;
        #[cfg(target_os = "windows")]
        {
            install_windows_package(self, bytes)
        }
        #[cfg(target_os = "macos")]
        {
            install_macos_package(self, bytes)
        }
        #[cfg(target_os = "linux")]
        {
            install_linux_package(self, bytes)
        }
        #[cfg(not(any(target_os = "windows", target_os = "macos", target_os = "linux")))]
        {
            let _ = bytes;
            Err("当前操作系统暂不支持自动安装更新包".into())
        }
    }
}

#[cfg(target_os = "windows")]
fn install_windows_package(pkg: &ReleasePackage, bytes: &[u8]) -> Result<(), String> {
    let installer = write_temp_file(&pkg.name, bytes)?;
    let exe_res = std::env::current_exe();
    let current_exe = exe_res.map_err(|e| format!("无法定位当前程序路径：{e}"))?;
    let parent_dir = current_exe.parent();
    let install_dir = parent_dir.ok_or_else(|| "无法定位安装目录".to_string())?;
    let backup_exe = current_exe.with_extension("exe.old");
    let _ = fs::remove_file(&backup_exe);
    let renamed = fs::rename(&current_exe, &backup_exe).is_ok();
    let status = match pkg.kind {
        PackageKind::WindowsNsis => {
            let mut cmd = Command::new(&installer);
            cmd.arg("/S").arg(format!("/D={}", install_dir.display()));
            cmd.status()
        }
        PackageKind::WindowsMsi => {
            let mut cmd = Command::new("msiexec.exe");
            cmd.arg("/i").arg(&installer).args(["/qn", "/norestart"]);
            cmd.status()
        }
        _ => {
            let _ = fs::remove_file(&installer);
            if renamed {
                let _ = fs::rename(&backup_exe, &current_exe);
            }
            return Err("不支持的 Windows 安装包格式".into());
        }
    };
    let _ = fs::remove_file(&installer);
    match status {
        Ok(exit) if exit.success() && current_exe.is_file() => Ok(()),
        Ok(exit) => {
            if renamed && !current_exe.is_file() {
                let _ = fs::rename(&backup_exe, &current_exe);
            }
            Err(format!("安装程序返回退出码 {}", exit.code().unwrap_or(-1)))
        }
        Err(err) => {
            if renamed && !current_exe.is_file() {
                let _ = fs::rename(&backup_exe, &current_exe);
            }
            Err(format!("启动安装程序失败：{err}"))
        }
    }
}

#[cfg(target_os = "macos")]
fn install_macos_package(pkg: &ReleasePackage, bytes: &[u8]) -> Result<(), String> {
    if pkg.kind != PackageKind::MacOsDmg {
        return Err("不支持的 macOS 安装包格式".into());
    }
    let exe_res = std::env::current_exe();
    let current_exe = exe_res.map_err(|e| format!("无法定位当前应用路径：{e}"))?;
    let app_path = current_exe
        .ancestors()
        .find(|p| p.extension().is_some_and(|ext| ext == "app"));
    let app_dir = app_path.ok_or("请先将 OmniShare.app 放入“应用程序”目录后再在线升级")?;
    let target_app = app_dir.to_path_buf();
    let dmg = write_temp_file(&pkg.name, bytes)?;
    let pid = std::process::id();
    let mount_dir = std::env::temp_dir().join(format!("omnishare-mount-{pid}"));
    let _ = fs::create_dir_all(&mount_dir);
    let mut attach_cmd = Command::new("hdiutil");
    attach_cmd.args(["attach", "-readonly", "-nobrowse", "-mountpoint"]);
    attach_cmd.arg(&mount_dir).arg(&dmg);
    let attach = attach_cmd
        .status()
        .map_err(|e| format!("挂载 DMG 失败：{e}"))?;
    if !attach.success() {
        let _ = fs::remove_dir_all(&mount_dir);
        let _ = fs::remove_file(&dmg);
        return Err("挂载更新镜像失败".into());
    }
    let install_result = (|| -> Result<(), String> {
        let source_app = mount_dir.join("OmniShare.app");
        let meta = fs::symlink_metadata(&source_app)
            .map_err(|_| "更新镜像中缺少 OmniShare.app".to_string())?;
        if meta.file_type().is_symlink() || !meta.is_dir() {
            return Err("更新镜像中的 OmniShare.app 结构无效".into());
        }
        let mut verify_cmd = Command::new("codesign");
        verify_cmd
            .args(["--verify", "--deep", "--strict"])
            .arg(&source_app);
        let verify = verify_cmd
            .status()
            .map_err(|e| format!("签名校验失败：{e}"))?;
        if !verify.success() {
            return Err("更新镜像内的应用完整性校验失败".into());
        }
        let mut copy_cmd = Command::new("ditto");
        copy_cmd.arg(&source_app).arg(&target_app);
        let copy_res = copy_cmd.status();
        let copy = copy_res.map_err(|e| format!("覆盖更新 OmniShare.app 失败：{e}"))?;
        if !copy.success() {
            return Err(String::from("覆盖更新应用失败（请确认未在只读镜像中运行）"));
        }
        Ok(())
    })();
    let mut detach_cmd = Command::new("hdiutil");
    detach_cmd.arg("detach").arg(&mount_dir).arg("-force");
    let _ = detach_cmd.status();
    let _ = fs::remove_dir_all(&mount_dir);
    let _ = fs::remove_file(&dmg);
    install_result
}

#[cfg(target_os = "linux")]
fn install_linux_package(pkg: &ReleasePackage, bytes: &[u8]) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;

    match pkg.kind {
        PackageKind::LinuxAppImage => {
            let env_target = std::env::var_os("APPIMAGE").map(PathBuf::from);
            let target = env_target
                .or_else(|| std::env::current_exe().ok())
                .ok_or_else(|| "无法定位当前 AppImage 路径".to_string())?;
            let parent_dir = target.parent();
            let parent = parent_dir.ok_or_else(|| "无法定位 AppImage 目录".to_string())?;
            let pid = std::process::id();
            let staged = parent.join(format!(".omnishare-update-{pid}.AppImage"));
            let created = fs::File::create(&staged);
            let mut file = created.map_err(|e| format!("无法写入更新文件：{e}"))?;
            file.write_all(bytes)
                .map_err(|e| format!("写入更新文件失败：{e}"))?;
            file.sync_all()
                .map_err(|e| format!("刷新更新文件失败：{e}"))?;
            let perms = fs::Permissions::from_mode(0o755);
            fs::set_permissions(&staged, perms).map_err(|e| format!("设置可执行权限失败：{e}"))?;
            fs::rename(&staged, &target).map_err(|e| {
                let _ = fs::remove_file(&staged);
                format!("替换 AppImage 失败：{e}")
            })
        }
        PackageKind::LinuxDeb => {
            let deb = write_temp_file(&pkg.name, bytes)?;
            let mut sudo_cmd = Command::new("sudo");
            sudo_cmd.args(["-n", "dpkg", "--install"]).arg(&deb);
            let status = match sudo_cmd.status() {
                Ok(exit) if exit.success() => Ok(exit),
                _ => {
                    let mut pkexec_cmd = Command::new("pkexec");
                    pkexec_cmd.args(["dpkg", "--install"]).arg(&deb);
                    pkexec_cmd.status()
                }
            };
            let _ = fs::remove_file(&deb);
            match status {
                Ok(exit) if exit.success() => Ok(()),
                Ok(exit) => Err(format!(
                    "DEB 安装返回退出码 {}（可能需要管理员权限）",
                    exit.code().unwrap_or(-1)
                )),
                Err(err) => Err(format!("无法启动 dpkg 安装器：{err}")),
            }
        }
        _ => Err("不支持的 Linux 安装包格式".into()),
    }
}

#[cfg(test)]
mod integration_tests;

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
    fn destroyed_window_cancels_pending_network_work_without_an_event_callback() {
        let state = UpdateState::default();
        let closed = state.closed.subscribe();
        state.cancel_reads();
        let result = tauri::async_runtime::block_on(while_window_open(
            closed,
            std::future::pending::<Result<(), String>>(),
        ));
        assert!(result.unwrap_err().contains("已取消"));
    }

    #[test]
    fn old_window_cannot_publish_ready_result_after_close_and_reopen() {
        let state = UpdateState::default();
        let old = state.closed.subscribe();
        state.cancel_reads();
        let reopened = state.closed.subscribe();
        assert!(tauri::async_runtime::block_on(while_window_open(
            old,
            std::future::ready(Ok("stale")),
        ))
        .is_err());
        assert_eq!(
            tauri::async_runtime::block_on(while_window_open(
                reopened,
                std::future::ready(Ok("new")),
            ))
            .unwrap(),
            "new"
        );
    }

    #[test]
    fn closing_idle_window_releases_verified_package_and_does_not_block_install() {
        let state = UpdateState::default();
        {
            let mut session = state.session.try_lock().unwrap();
            session.bytes = Some(vec![1, 2, 3]);
        }
        state.cancel_reads();
        assert!(state.session.try_lock().unwrap().bytes.is_none());
        // Installation owns the session lock and is intentionally not wrapped
        // in while_window_open. A UI close must never cancel the installer.
        let _installing = state.session.try_lock().unwrap();
        state.cancel_reads();
    }

    #[test]
    fn https_client_installs_the_required_process_crypto_provider() {
        // This must build without callers or the test harness initializing TLS.
        // Without initialize_crypto_provider(), reqwest panics before a request.
        let first = client(Duration::from_secs(3));
        assert!(first.is_ok());
        assert!(rustls::crypto::CryptoProvider::get_default().is_some());
        assert!(client(Duration::from_secs(5)).is_ok());
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
    fn build_metadata_alone_never_offers_a_reinstall() {
        let list = vec![release("v1.2.3+newbuild"), release("v1.2.3+zzz")];
        assert!(newest(&list, &Version::parse("1.2.3+oldbuild").unwrap(), true).is_none());
        let versions = vec![release("v1.2.4+aaa"), release("v1.2.3+zzz")];
        assert_eq!(
            newest(&versions, &Version::parse("1.2.3").unwrap(), true)
                .unwrap()
                .tag_name,
            "v1.2.4+aaa"
        );
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

    #[test]
    fn sha256_matches_standard_vectors_and_parses_sums() {
        assert_eq!(
            sha256_hex(b""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
        assert_eq!(
            sha256_hex(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
        let exe = "OmniShare-v0.2.0-rc.6-windows-x64-setup.exe";
        let digest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad";
        let sums = format!("{}  {exe}\n", digest.to_ascii_uppercase());
        assert_eq!(parse_sha256_for_asset(&sums, exe).as_deref(), Some(digest));
        assert!(parse_sha256_for_asset(&sums, "other.exe").is_none());
    }

    #[test]
    fn release_package_resolves_multiplatform_assets_and_verifies_sha256() {
        let payload = b"abc";
        let hash = sha256_hex(payload);
        let sums = format!(
            "{hash}  OmniShare-v0.2.0-rc.6-windows-x64-setup.exe\n\
             {hash}  OmniShare-v0.2.0-rc.6-macos-universal.dmg\n\
             {hash}  OmniShare-v0.2.0-rc.6-linux-amd64.AppImage\n"
        );
        let tag = "v0.2.0-rc.6";
        let asset = |name: &str| Asset {
            name: name.into(),
            browser_download_url: format!(
                "https://github.com/Azurefly/omnishare/releases/download/{tag}/{name}"
            ),
            size: payload.len(),
        };
        let mut r = release(tag);
        r.assets = vec![
            asset("OmniShare-v0.2.0-rc.6-windows-x64-setup.exe"),
            asset("OmniShare-v0.2.0-rc.6-macos-universal.dmg"),
            asset("OmniShare-v0.2.0-rc.6-linux-amd64.AppImage"),
        ];
        for (suffix, kind) in [
            ("-windows-x64-setup.exe", PackageKind::WindowsNsis),
            ("-macos-universal.dmg", PackageKind::MacOsDmg),
            ("-linux-amd64.AppImage", PackageKind::LinuxAppImage),
        ] {
            let pkg = resolve_release_package(&r, sums.as_bytes(), &[(suffix, kind)]).unwrap();
            assert_eq!(pkg.kind, kind);
            pkg.verify(payload).unwrap();
            assert!(pkg.verify(b"abd").is_err());
        }
        let mut bad_url = r.clone();
        bad_url.assets[0].browser_download_url = "https://evil.example/OmniShare.exe".into();
        let candidates = [("-windows-x64-setup.exe", PackageKind::WindowsNsis)];
        assert!(resolve_release_package(&bad_url, sums.as_bytes(), &candidates).is_err());
    }
}
