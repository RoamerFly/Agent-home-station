// Diagnostic-only QR creation. No account authentication or message send.
use wechat_protocol::{DeviceProfile, NativeClient, NativeOptions};
fn main() {
    let result = (|| -> anyhow::Result<usize> {
        let mut options = NativeOptions::default();
        options.timeout = std::time::Duration::from_secs(8);
        if let Some(host) = std::env::args().nth(1) { options.long_host = host; }
        if let Some(host) = std::env::args().nth(2) { options.short_host = host; }
        let started = std::time::Instant::now();
        match wechat_protocol::mmtls::Session::connect(&options.long_host,options.timeout,options.receive_budget) {
            Ok(_) => eprintln!("initial_handshake=ok elapsed_ms={}", started.elapsed().as_millis()),
            Err(error) => eprintln!("initial_handshake=failed elapsed_ms={} error={error:#}",started.elapsed().as_millis()),
        }
        let client = NativeClient::new(options)?;
        let device = DeviceProfile::generate("UnifiedPCLinux x86_64".into())?;
        let attempt = client.start_qr_login(device, "WechatBot")?;
        Ok(attempt.qr_png.len())
    })();
    match result {
        Ok(size) => println!("qr_created=true bytes={size}"),
        Err(error) => { eprintln!("prelogin diagnostic: {error:#}"); std::process::exit(1); }
    }
}
