use pulldown_cmark::{html, Options, Parser};
use std::convert::TryInto;
use std::mem;

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

    // PROTOCOL: [OpID:4][TextLen:4][Text]
    let op_id = u32::from_le_bytes(slice[0..4].try_into().unwrap());
    let txt_len = u32::from_le_bytes(slice[4..8].try_into().unwrap()) as usize;
    
    if 8 + txt_len > len { return 0; }

    let text = match std::str::from_utf8(&slice[8..8 + txt_len]) {
        Ok(v) => v,
        Err(_) => return 0,
    };

    let result = if op_id == 0 {
        // Markdown -> HTML
        let mut options = Options::empty();
        options.insert(Options::ENABLE_TABLES);
        options.insert(Options::ENABLE_STRIKETHROUGH);
        options.insert(Options::ENABLE_TASKLISTS);
        
        let parser = Parser::new_ext(text, options);
        let mut out = String::new();
        html::push_html(&mut out, parser);
        out
    } else {
        // HTML -> Markdown
        html2md::parse_html(text)
    };

    let out_bytes = result.as_bytes();
    let out_len = out_bytes.len();
    
    if out_len > len { return 0; }
    slice[0..out_len].copy_from_slice(out_bytes);
    out_len
}