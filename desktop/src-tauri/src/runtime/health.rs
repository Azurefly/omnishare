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
