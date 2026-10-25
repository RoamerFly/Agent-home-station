// Offline diagnostic only: no credentials, account access or message sends.
fn main() {
    for seconds in [1, 15, 40] {
        let pcm = (0..seconds*16000).map(|i| ((i as f64*440.0*std::f64::consts::TAU/16000.0).sin()*4095.0)as i16).collect::<Vec<_>>();
        let encoded=wechat_protocol::silk::encode(&pcm).unwrap();
        println!("seconds={seconds} encoded_bytes={} upload_ranges={}",encoded.len(),encoded.len().div_ceil(wechat_protocol::media_send::VOICE_UPLOAD_CHUNK_BYTES));
    }
}
