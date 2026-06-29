use flate2::write::GzEncoder;
use flate2::read::GzDecoder;
use flate2::Compression;
use std::io::{Write, Read};
use std::mem;
use std::convert::TryInto;

#[no_mangle]
pub extern "C" fn alloc(size: usize) -> *mut u8 {
    let mut buf = Vec::with_capacity(size);
    let ptr = buf.as_mut_ptr();
    mem::forget(buf);
    ptr
}

#[no_mangle]
pub extern "C" fn execute(ptr: *mut u8, len: usize) -> usize {
    let slice = unsafe { std::slice::from_raw_parts_mut(ptr, len) };
    if len < 8 { return len; }

    let data_len = u32::from_le_bytes(slice[0..4].try_into().unwrap()) as usize;
    let op_id = u32::from_le_bytes(slice[4..8].try_into().unwrap()) as usize;
    
    if 8 + data_len > len { return len; }
    let actual_data = &slice[8..8 + data_len];

    if op_id == 0 {
        // Compress (Encode)
        let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
        if encoder.write_all(actual_data).is_err() { return 0; }
        
        let compressed_data = match encoder.finish() {
            Ok(data) => data,
            Err(_) => return 0,
        };
        
        let out_len = compressed_data.len();
        if out_len > len { return 0; }
        
        slice[0..out_len].copy_from_slice(&compressed_data);
        return out_len;
        
    } else if op_id == 1 {
        // Extract (Decode)
        let mut decoder = GzDecoder::new(actual_data);
        let mut decompressed_data = Vec::new();
        
        if decoder.read_to_end(&mut decompressed_data).is_err() { return 0; }
        
        let out_len = decompressed_data.len();
        if out_len > len { return 0; } // Engine needs more memory
        
        slice[0..out_len].copy_from_slice(&decompressed_data);
        return out_len;
    }

    0
}