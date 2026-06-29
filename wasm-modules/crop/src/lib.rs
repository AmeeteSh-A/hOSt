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
    
    let img_width = u32::from_le_bytes(slice[0..4].try_into().unwrap()) as usize;
    let img_height = u32::from_le_bytes(slice[4..8].try_into().unwrap()) as usize;
    let crop_x = u32::from_le_bytes(slice[8..12].try_into().unwrap()) as usize;
    let crop_y = u32::from_le_bytes(slice[12..16].try_into().unwrap()) as usize;
    let crop_w = u32::from_le_bytes(slice[16..20].try_into().unwrap()) as usize;
    let crop_h = u32::from_le_bytes(slice[20..24].try_into().unwrap()) as usize;
    
    let header_offset = 24;
    let mut cropped = Vec::with_capacity(crop_w * crop_h * 4);
    
    for y in 0..crop_h {
        let target_y = crop_y + y;
        if target_y >= img_height {
            break;
        }
        for x in 0..crop_w {
            let target_x = crop_x + x;
            if target_x >= img_width {
                break;
            }
            
            let idx = header_offset + (target_y * img_width + target_x) * 4;
            if idx + 3 < len {
                cropped.push(slice[idx]);
                cropped.push(slice[idx + 1]);
                cropped.push(slice[idx + 2]);
                cropped.push(slice[idx + 3]);
            }
        }
    }
    
    for i in 0..cropped.len() {
        slice[i] = cropped[i];
    }
}