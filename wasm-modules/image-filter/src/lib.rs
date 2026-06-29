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
    let mut i = 0;
    while i < len {
        if i + 3 < len {
            let r = slice[i] as u32;
            let g = slice[i + 1] as u32;
            let b = slice[i + 2] as u32;
            let gray = ((r + g + b) / 3) as u8;
            slice[i] = gray;
            slice[i + 1] = gray;
            slice[i + 2] = gray;
        }
        i += 4;
    }
}