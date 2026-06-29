use aes_gcm::{
    aead::{Aead, KeyInit, Payload},
    Aes256Gcm, Nonce,
};
use pbkdf2::pbkdf2;
use sha2::Sha256;
use hmac::Hmac;
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
    if len < 8 { return 0; }

    let op_id = u32::from_le_bytes(slice[0..4].try_into().unwrap());
    let pass_len = u32::from_le_bytes(slice[4..8].try_into().unwrap()) as usize;
    
    if 8 + pass_len + 16 + 12 > len { return 0; } // Bounds check

    let password = &slice[8 .. 8+pass_len];
    let salt = &slice[8+pass_len .. 8+pass_len+16];
    let nonce_bytes = &slice[8+pass_len+16 .. 8+pass_len+16+12];
    let file_data = &slice[8+pass_len+16+12 .. len];

    // Derive a 256-bit key from the password using PBKDF2 (100,000 iterations)
    let mut key = [0u8; 32];
    pbkdf2::<Hmac<Sha256>>(password, salt, 100_000, &mut key).unwrap();

    let cipher = Aes256Gcm::new(key.as_ref().into());
    let nonce = Nonce::from_slice(nonce_bytes);

    if op_id == 0 {
        // ENCRYPT
        let encrypted_data = match cipher.encrypt(nonce, Payload { msg: file_data, aad: &[] }) {
            Ok(d) => d,
            Err(_) => return 0,
        };
        let out_len = encrypted_data.len();
        if out_len > len { return 0; }
        slice[0..out_len].copy_from_slice(&encrypted_data);
        return out_len;
        
    } else if op_id == 1 {
        // DECRYPT
        let decrypted_data = match cipher.decrypt(nonce, Payload { msg: file_data, aad: &[] }) {
            Ok(d) => d,
            Err(_) => return 0,
        };
        let out_len = decrypted_data.len();
        if out_len > len { return 0; }
        slice[0..out_len].copy_from_slice(&decrypted_data);
        return out_len;
    }
    
    0
}