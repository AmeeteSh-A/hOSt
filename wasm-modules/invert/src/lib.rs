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
            slice[i] = 255 - slice[i];
            slice[i + 1] = 255 - slice[i + 1];
            slice[i + 2] = 255 - slice[i + 2];
        }
        i += 4;
    }
}