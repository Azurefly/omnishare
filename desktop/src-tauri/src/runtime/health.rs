use std::{
    io::{Read, Write},
    net::{SocketAddr, TcpStream},
    time::{Duration, Instant},
};

pub fn is_omnishare_backend(host: &str, port: u16) -> bool {
    let address = match format!("{}:{}", host, port).parse::<SocketAddr>() {
        Ok(address) => address,
        Err(_) => return false,
    };

    let mut stream = match TcpStream::connect_timeout(&address, Duration::from_millis(700)) {
        Ok(stream) => stream,
        Err(_) => return false,
    };
    let _ = stream.set_read_timeout(Some(Duration::from_secs(2)));
    let _ = stream.set_write_timeout(Some(Duration::from_secs(2)));

    let request = format!(
        "GET / HTTP/1.0\r\nHost: {}:{}\r\nConnection: close\r\n\r\n",
        host, port
    );
    if stream.write_all(request.as_bytes()).is_err() {
        return false;
    }

    let mut response = String::new();
    if stream
        .take(64 * 1024)
        .read_to_string(&mut response)
        .is_err()
    {
        return false;
    }

    response.contains("<title>OmniShare</title>")
        || response.contains("OmniShare 私有化跨设备速记、文件与协同工作台")
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

    fn serve_once(body: &'static str) -> (u16, thread::JoinHandle<()>) {
        let listener = TcpListener::bind(("127.0.0.1", 0)).expect("bind test server");
        let port = listener.local_addr().expect("test server address").port();
        let handle = thread::spawn(move || {
            let (mut stream, _) = listener.accept().expect("accept health probe");
            let mut request = [0_u8; 2048];
            let _ = stream.read(&mut request);
            let response = format!(
                "HTTP/1.0 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                body.len(),
                body
            );
            stream
                .write_all(response.as_bytes())
                .expect("write health response");
        });
        (port, handle)
    }

    #[test]
    fn recognizes_the_omnishare_page() {
        let (port, server) = serve_once("<!doctype html><title>OmniShare</title>");
        assert!(is_omnishare_backend("127.0.0.1", port));
        server.join().expect("join test server");
    }

    #[test]
    fn rejects_an_unrelated_http_service() {
        let (port, server) = serve_once("<!doctype html><title>Other Service</title>");
        assert!(!is_omnishare_backend("127.0.0.1", port));
        server.join().expect("join test server");
    }

    #[test]
    fn wait_backend_times_out_for_a_closed_port() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).expect("bind ephemeral port");
        let port = listener.local_addr().expect("ephemeral address").port();
        drop(listener);
        assert!(!wait_backend("127.0.0.1", port, Duration::from_millis(350)));
    }
}
