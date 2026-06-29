use std::mem;

#[no_mangle]
pub extern "C" fn alloc(size: usize) -> *mut u8 {
    let mut buf = Vec::with_capacity(size);
    let ptr = buf.as_mut_ptr();
    mem::forget(buf);
    ptr
}

#[no_mangle]
pub extern "C" fn execute(ptr: *mut u8, len: usize) {
    let slice = unsafe { std::slice::from_raw_parts_mut(ptr, len) };
    
    let orig_w = u32::from_le_bytes(slice[0..4].try_into().unwrap()) as usize;
    let orig_h = u32::from_le_bytes(slice[4..8].try_into().unwrap()) as usize;
    let new_w = u32::from_le_bytes(slice[16..20].try_into().unwrap()) as usize;
    let new_h = u32::from_le_bytes(slice[20..24].try_into().unwrap()) as usize;
    
    let header_offset = 24;
    let mut resized = Vec::with_capacity(new_w * new_h * 4);
    
    for y in 0..new_h {
        for x in 0..new_w {
            let src_x = (x * orig_w) / new_w;
            let src_y = (y * orig_h) / new_h;
            
            let src_idx = header_offset + (src_y * orig_w + src_x) * 4;
            
            if src_idx + 3 < len {
                resized.push(slice[src_idx]);
                resized.push(slice[src_idx + 1]);
                resized.push(slice[src_idx + 2]);
                resized.push(slice[src_idx + 3]);
            } else {
                resized.extend_from_slice(&[0, 0, 0, 0]);
            }
        }
    }
    
    for i in 0..resized.len() {
        slice[i] = resized[i];
    }
}