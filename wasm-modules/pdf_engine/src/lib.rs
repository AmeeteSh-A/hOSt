use lopdf::Document;
use serde::Deserialize;
use std::mem;
use std::convert::TryInto;
use std::io::Cursor;

#[derive(Deserialize)]
struct PayloadHeader {
    op_id: u32,
    pages: Vec<u32>,
    pdf_size: u32, // Rust now reads the boundary tracker
}

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
    if len < 4 { return 0; }

    let header_len = u32::from_le_bytes(slice[0..4].try_into().unwrap()) as usize;
    if 4 + header_len > len { return 0; }

    let header_bytes = &slice[4..4+header_len];
    let header: PayloadHeader = match serde_json::from_slice(header_bytes) {
        Ok(h) => h,
        Err(_) => return 0,
    };

    let p_size = header.pdf_size as usize;
    if 4 + header_len + p_size > len { return 0; }

    // THE FIX: We strictly isolate the valid PDF bytes and ignore the empty buffer
    let pdf_data = &slice[4+header_len .. 4+header_len+p_size];

    if header.op_id == 0 {
        let mut doc = match Document::load_from(Cursor::new(pdf_data)) {
            Ok(d) => d,
            Err(_) => return 0,
        };

        let current_pages = doc.get_pages();
        let mut p_to_keep = std::collections::HashSet::new();
        for p in &header.pages {
            if let Some(&obj_id) = current_pages.get(p) {
                p_to_keep.insert(obj_id);
            }
        }

        let mut p_to_delete = Vec::new();
        for (p_num, obj_id) in current_pages.iter() {
            if !p_to_keep.contains(obj_id) {
                p_to_delete.push(*p_num);
            }
        }

        doc.delete_pages(&p_to_delete);

        let mut out_buf = Vec::new();
        if doc.save_to(&mut out_buf).is_err() { return 0; }

        let out_len = out_buf.len();
        if out_len > len { return 0; }

        slice[0..out_len].copy_from_slice(&out_buf);
        return out_len;

    } else if header.op_id == 1 {
        let doc = match Document::load_from(Cursor::new(pdf_data)) {
            Ok(d) => d,
            Err(_) => return 0,
        };

        let pages_to_scan = if header.pages.is_empty() {
            doc.get_pages().keys().cloned().collect()
        } else {
            header.pages.clone()
        };

        let mut extracted = String::new();
        for p in pages_to_scan {
            if let Ok(text) = doc.extract_text(&[p]) {
                extracted.push_str(&text);
                extracted.push_str("\n\n--- PAGE BREAK ---\n\n");
            }
        }

        let out_bytes = extracted.as_bytes();
        let out_len = out_bytes.len();
        if out_len > len { return 0; }

        slice[0..out_len].copy_from_slice(out_bytes);
        return out_len;
    }

    0
}