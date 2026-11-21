//! Shared by the native update commands and the transport regression tests.
//! This layer never installs or changes backend state.
use std::{future::Future, time::Duration};

pub(super) async fn with_deadline<T>(
    duration: Duration,
    work: impl Future<Output = Result<T, String>>,
) -> Result<T, String> {
    tokio::time::timeout(duration, work)
        .await
        .map_err(|_| "检查更新超时，请检查网络后重试；当前版本未改变".to_string())?
}

pub(super) async fn read_response(
    response: reqwest::Response,
    expected: Option<usize>,
    limit: usize,
    mut progress: impl FnMut(usize) -> Result<(), String>,
) -> Result<Vec<u8>, String> {
    let mut response = response
        .error_for_status()
        .map_err(|e| format!("更新请求失败：{e}"))?;
    if expected.is_some_and(|size| size == 0 || size > limit) {
        return Err("更新包声明大小异常".into());
    }
    if response
        .content_length()
        .is_some_and(|size| size > limit as u64 || expected.is_some_and(|want| size != want as u64))
    {
        return Err("更新响应长度与发布清单不一致或超出限制".into());
    }
    let maximum = expected.unwrap_or(limit);
    let mut bytes = Vec::new();
    while let Some(chunk) = response
        .chunk()
        .await
        .map_err(|e| format!("下载中断：{e}"))?
    {
        if bytes.len().saturating_add(chunk.len()) > maximum {
            return Err("更新响应超出声明大小或安全限制".into());
        }
        bytes.extend_from_slice(&chunk);
        progress(bytes.len())?;
    }
    if expected.is_some_and(|size| bytes.len() != size) {
        return Err("更新包下载不完整，请重试".into());
    }
    Ok(bytes)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        io::{Read, Write},
        net::TcpListener,
        thread,
    };

    // An actual HTTP connection; no mocked Response or signature verifier.
    fn serve(response: &'static [u8]) -> (String, thread::JoinHandle<()>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}/update", listener.local_addr().unwrap());
        let worker = thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            stream
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let mut request = [0; 2048];
            let _ = stream.read(&mut request);
            let _ = stream.write_all(response);
        });
        (url, worker)
    }

    fn read(
        response: &'static [u8],
        expected: Option<usize>,
        limit: usize,
    ) -> Result<Vec<u8>, String> {
        let (url, worker) = serve(response);
        let result = tauri::async_runtime::block_on(async {
            let response = super::super::client(Duration::from_secs(3))?
                .get(url)
                .send()
                .await
                .map_err(|e| e.to_string())?;
            read_response(response, expected, limit, |_| Ok(())).await
        });
        worker.join().unwrap();
        result
    }

    #[test]
    fn exact_package_is_accepted_without_installing() {
        assert_eq!(
            read(
                b"HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello",
                Some(5),
                8
            )
            .unwrap(),
            b"hello"
        );
    }
    #[test]
    fn wrong_content_length_fails_before_progress() {
        assert!(read(
            b"HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello",
            Some(4),
            8
        )
        .is_err());
    }
    #[test]
    fn truncated_download_never_becomes_ready() {
        assert!(read(
            b"HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhe",
            Some(5),
            8
        )
        .is_err());
    }
    #[test]
    fn chunked_metadata_cannot_bypass_limit() {
        assert!(read(
            b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n",
            None,
            4
        )
        .is_err());
    }
    #[test]
    fn chunked_package_must_match_declared_size() {
        const RESPONSE: &[u8] =
            b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n";
        assert!(read(RESPONSE, Some(6), 8).is_err());
        assert!(read(RESPONSE, Some(4), 8).is_err());
        assert_eq!(read(RESPONSE, Some(5), 8).unwrap(), b"hello");
    }
    #[test]
    fn http_failure_and_invalid_expected_sizes_are_errors() {
        const OK: &[u8] = b"HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello";
        assert!(read(
            b"HTTP/1.1 503 Unavailable\r\nContent-Length: 0\r\n\r\n",
            None,
            8
        )
        .is_err());
        assert!(read(OK, Some(0), 8).is_err());
        assert!(read(OK, Some(9), 8).is_err());
    }
    #[test]
    fn cancelled_progress_does_not_return_package_bytes() {
        let (url, worker) = serve(b"HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello");
        let result = tauri::async_runtime::block_on(async {
            let response = super::super::client(Duration::from_secs(3))
                .unwrap()
                .get(url)
                .send()
                .await
                .unwrap();
            read_response(response, Some(5), 8, |_| Err("closed".into())).await
        });
        worker.join().unwrap();
        assert_eq!(result.unwrap_err(), "closed");
    }
    #[test]
    fn complete_check_has_a_deadline_not_one_per_request() {
        let result = tauri::async_runtime::block_on(async {
            with_deadline(
                Duration::from_millis(5),
                std::future::pending::<Result<(), String>>(),
            )
            .await
        });
        assert!(result.unwrap_err().contains("超时"));
    }
}
