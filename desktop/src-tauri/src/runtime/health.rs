use std::{
    net::{SocketAddr, TcpStream},
    time::{Duration, Instant},
};

pub fn wait_backend(host: &str, port: u16, timeout: Duration) -> bool {
    let address = match format!("{}:{}", host, port).parse::<SocketAddr>() {
        Ok(address) => address,
        Err(_) => return false,
    };

    let started = Instant::now();
    while started.elapsed() < timeout {
        if TcpStream::connect_timeout(&address, Duration::from_millis(500)).is_ok() {
            return true;
        }
        std::thread::sleep(Duration::from_millis(300));
    }

    false
}
