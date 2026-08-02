use std::time::Duration;

pub async fn wait_backend(url: &str, timeout_seconds: u64) -> bool {
    let client = reqwest::Client::new();
    let mut attempts = timeout_seconds * 2;

    while attempts > 0 {
        if client.get(url).timeout(Duration::from_secs(2)).send().await.is_ok() {
            return true;
        }

        attempts -= 1;
        tokio::time::sleep(Duration::from_millis(500)).await;
    }

    false
}
