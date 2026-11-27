use std::{
    io::{Read, Write},
    net::{SocketAddr, TcpStream},
    time::{Duration, Instant},
};

pub fn is_omnishare_backend(host: &str, port: u16) -> bool {
    let Ok(address) = format!("{host}:{port}").parse::<SocketAddr>() else {
        return false;
    };
    let Some((status, body)) = read_endpoint(address, "/api/v1/health") else {
        return false;
    };
    if status == 200 {
        if is_workbench_page(&body) {
            return true;
        }
        let Ok(reply) = serde_json::from_str::<serde_json::Value>(&body) else {
            return false;
        };
        let data = &reply["data"];
        if reply["code"].as_i64() != Some(0) || data["status"] != "ok" {
            return false;
        }
        if let Some(protocol) = data["workspace_protocol"].as_str() {
            return protocol.starts_with("omnishare/workspace/");
        }
        if data["version"].as_str().is_none_or(str::is_empty) {
            return false;
        }
    } else if status != 404 {
        return false;
    }
    // A legacy server can identify itself by its workbench. A generic health
    // response on an occupied port is insufficient to attach the desktop.
    read_endpoint(address, "/")
        .is_some_and(|(status, body)| status == 200 && is_workbench_page(&body))
}

fn is_workbench_page(body: &str) -> bool {
    body.trim_start().starts_with('<')
        && (body.contains("<title>OmniShare</title>")
            || body.contains("OmniShare 私有化跨设备速记、文件与协同工作台"))
}

fn read_endpoint(address: SocketAddr, path: &str) -> Option<(u16, String)> {
    let mut stream = TcpStream::connect_timeout(&address, Duration::from_millis(700)).ok()?;
    let _ = stream.set_read_timeout(Some(Duration::from_secs(2)));
    let _ = stream.set_write_timeout(Some(Duration::from_secs(2)));

    let request = format!(
        "GET {path} HTTP/1.0\r\nHost: {address}\r\nAccept: */*\r\nConnection: close\r\n\r\n"
    );
    stream.write_all(request.as_bytes()).ok()?;
    let mut data = Vec::new();
    stream.take(64 * 1024).read_to_end(&mut data).ok()?;
    let response = String::from_utf8_lossy(&data);
    let (headers, body) = response.split_once("\r\n\r\n")?;
    let mut fields = headers.lines().next()?.split_ascii_whitespace();
    if !fields.next()?.starts_with("HTTP/1.") {
        return None;
    }
    Some((fields.next()?.parse().ok()?, body.to_owned()))
}

pub fn wait_backend(host: &str, port: u16, timeout: Duration) -> bool {
    let started = Instant::now();
    while started.elapsed() < timeout {
        if is_omnishare_backend(host, port) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(300));
    }

    false
}

#[cfg(test)]
mod tests {
    use std::{
        io::{Read, Write},
        net::TcpListener,
        thread,
        time::Duration,
    };

    use super::{is_omnishare_backend, wait_backend};

    fn serve(replies: &[(u16, &'static str)]) -> (u16, thread::JoinHandle<()>) {
        let listener = TcpListener::bind(("127.0.0.1", 0)).expect("bind test server");
        let port = listener.local_addr().expect("test server address").port();
        let replies = replies.to_vec();
        let handle = thread::spawn(move || {
            for (status, body) in replies {
                let (mut stream, _) = listener.accept().expect("accept health probe");
                let mut request = [0_u8; 2048];
                let _ = stream.read(&mut request);
                let response = format!(
                    "HTTP/1.0 {status} Test\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                    body.len(),
                    body
                );
                stream
                    .write_all(response.as_bytes())
                    .expect("write health response");
            }
        });
        (port, handle)
    }

    #[test]
    fn recognizes_the_omnishare_page() {
        let (port, server) = serve(&[(200, "<!doctype html><title>OmniShare</title>")]);
        assert!(is_omnishare_backend("127.0.0.1", port));
        server.join().expect("join test server");
    }

    #[test]
    fn rejects_an_unrelated_http_service() {
        let (port, server) = serve(&[(200, r#"{"status":"ok"}"#)]);
        assert!(!is_omnishare_backend("127.0.0.1", port));
        server.join().expect("join test server");
    }

    #[test]
    fn recognizes_the_json_health_endpoint() {
        let (port, server) = serve(&[(
            200,
            r#"{"code":0,"data":{"status":"ok","workspace_protocol":"omnishare/workspace/1"}}"#,
        )]);
        assert!(is_omnishare_backend("127.0.0.1", port));
        server.join().expect("join test server");
    }

    #[test]
    fn checks_the_workbench_for_older_servers() {
        let legacy = r#"{"code":0,"data":{"status":"ok","version":"1.3.0"}}"#;
        for (root, expected) in [
            ("<title>OmniShare</title>", true),
            ("<title>Other</title>", false),
        ] {
            let (port, server) = serve(&[(200, legacy), (200, root)]);
            assert_eq!(is_omnishare_backend("127.0.0.1", port), expected);
            server.join().unwrap();
        }
    }

    #[test]
    fn an_error_response_is_not_a_ready_workbench() {
        let (port, server) = serve(&[(503, "<title>OmniShare</title>")]);
        assert!(!is_omnishare_backend("127.0.0.1", port));
        server.join().unwrap();
    }

    #[test]
    fn wait_backend_times_out_for_a_closed_port() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).expect("bind ephemeral port");
        let port = listener.local_addr().expect("ephemeral address").port();
        drop(listener);
        assert!(!wait_backend("127.0.0.1", port, Duration::from_millis(350)));
    }
}
