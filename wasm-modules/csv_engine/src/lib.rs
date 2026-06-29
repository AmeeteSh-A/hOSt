use std::mem;
use std::str;

#[no_mangle]
pub extern "C" fn alloc(size: usize) -> *mut u8 {
    let mut buf = Vec::with_capacity(size);
    let ptr = buf.as_mut_ptr();
    mem::forget(buf);
    ptr
}

fn evaluate_row(query: &str, headers: &[&str], fields: &[&str]) -> bool {
    let conditions: Vec<&str> = query.split("&&").collect();
    for cond in conditions {
        let cond = cond.trim();
        if cond.is_empty() { continue; }
        let (op, op_idx) = if let Some(i) = cond.find("==") { ("==", i) }
        else if let Some(i) = cond.find("!=") { ("!=", i) }
        else if let Some(i) = cond.find(">=") { (">=", i) }
        else if let Some(i) = cond.find("<=") { ("<=", i) }
        else if let Some(i) = cond.find(">") { (">", i) }
        else if let Some(i) = cond.find("<") { ("<", i) }
        else { continue; }; 
        let col_name = cond[..op_idx].trim();
        let mut val_str = cond[op_idx + op.len()..].trim();
        if val_str.starts_with('"') && val_str.ends_with('"') || val_str.starts_with('\'') && val_str.ends_with('\'') {
            val_str = &val_str[1..val_str.len()-1];
        }
        let col_idx = headers.iter().position(|&h| h.trim() == col_name);
        if let Some(idx) = col_idx {
            let field_val = fields.get(idx).unwrap_or(&"").trim();
            let is_num = field_val.parse::<f64>().is_ok() && val_str.parse::<f64>().is_ok();
            let matched = if is_num {
                let f1: f64 = field_val.parse().unwrap();
                let f2: f64 = val_str.parse().unwrap();
                match op {
                    "==" => f1 == f2,
                    "!=" => f1 != f2,
                    ">" => f1 > f2,
                    "<" => f1 < f2,
                    ">=" => f1 >= f2,
                    "<=" => f1 <= f2,
                    _ => false,
                }
            } else {
                match op {
                    "==" => field_val == val_str,
                    "!=" => field_val != val_str,
                    _ => false,
                }
            };
            if !matched { return false; }
        } else {
            return false;
        }
    }
    true
}

#[no_mangle]
pub extern "C" fn execute(ptr: *mut u8, len: usize) -> usize {
    let slice = unsafe { std::slice::from_raw_parts_mut(ptr, len) };
    if len < 8 { return len; }
    let op_id = u32::from_le_bytes(slice[0..4].try_into().unwrap()) as usize;
    if op_id == 3 {
        let q_len = u32::from_le_bytes(slice[4..8].try_into().unwrap()) as usize;
        if 8 + q_len >= len { return len; }
        let query_str = match str::from_utf8(&slice[8..8+q_len]) {
            Ok(v) => v,
            Err(_) => return len,
        };
        let csv_data = &slice[8+q_len..len];
        let text = match str::from_utf8(csv_data) {
            Ok(v) => v,
            Err(_) => return len,
        };
        let mut lines: Vec<&str> = text.lines().collect();
        if lines.is_empty() { return len; }
        let header_line = lines.remove(0);
        let headers: Vec<&str> = header_line.split(',').collect();
        let mut new_lines = vec![header_line.to_string()];
        for l in lines {
            let fields: Vec<&str> = l.split(',').collect();
            if evaluate_row(query_str, &headers, &fields) {
                new_lines.push(l.to_string());
            }
        }
        let final_csv = new_lines.join("\n");
        let result_bytes = final_csv.as_bytes();
        let out_len = std::cmp::min(result_bytes.len(), len);
        slice[0..out_len].copy_from_slice(&result_bytes[0..out_len]);
        return out_len;
    }
    let col_idx = u32::from_le_bytes(slice[4..8].try_into().unwrap()) as usize;
    let csv_data = &slice[8..len];
    let text = match str::from_utf8(csv_data) {
        Ok(v) => v,
        Err(_) => return len,
    };
    if op_id == 4 {
        let mut values = Vec::new();
        let mut labels = Vec::new();
        let mut max_val = 0.0;
        let mut lines = text.lines();
        if let Some(_) = lines.next() {
            for line in lines {
                let parts: Vec<&str> = line.split(',').collect();
                if parts.len() > 0 {
                    let label = parts.get(0).unwrap_or(&"").trim().to_string();
                    if col_idx < parts.len() {
                        if let Ok(val) = parts[col_idx].trim().parse::<f64>() {
                            values.push(val);
                            labels.push(label);
                            if val > max_val { max_val = val; }
                        }
                    }
                }
            }
        }
        if max_val == 0.0 { max_val = 1.0; }
        let mut json_parts = Vec::new();
        for i in 0..values.len() {
            let ratio = values[i] / max_val;
            let clean_label = labels[i].replace("\"", "\\\"");
            json_parts.push(format!("{{\"label\":\"{}\",\"value\":{},\"ratio\":{}}}", clean_label, values[i], ratio));
        }
        let final_json = format!("[{}]", json_parts.join(","));
        let result_bytes = final_json.as_bytes();
        let out_len = std::cmp::min(result_bytes.len(), len);
        slice[0..out_len].copy_from_slice(&result_bytes[0..out_len]);
        return out_len;
    }
    if op_id == 0 {
        let mut sum = 0.0;
        let mut count = 0;
        for line in text.lines() {
            let parts: Vec<&str> = line.split(',').collect();
            if col_idx < parts.len() {
                if let Ok(val) = parts[col_idx].trim().parse::<f64>() {
                    sum += val;
                    count += 1;
                }
            }
        }
        let result_str = format!("COMPUTED SUM: {:.2} (Processed {} rows)", sum, count);
        let result_bytes = result_str.as_bytes();
        let out_len = std::cmp::min(result_bytes.len(), len);
        slice[0..out_len].copy_from_slice(&result_bytes[0..out_len]);
        return out_len;
    }
    let mut lines: Vec<String> = text.lines().map(|s| s.to_string()).collect();
    if lines.is_empty() { return len; }
    let header = lines.remove(0);
    if op_id == 1 {
        lines.sort_by(|a, b| {
            let a_parts: Vec<&str> = a.split(',').collect();
            let b_parts: Vec<&str> = b.split(',').collect();
            let a_val = a_parts.get(col_idx).unwrap_or(&"").trim().parse::<f64>().unwrap_or(0.0);
            let b_val = b_parts.get(col_idx).unwrap_or(&"").trim().parse::<f64>().unwrap_or(0.0);
            a_val.partial_cmp(&b_val).unwrap_or(std::cmp::Ordering::Equal)
        });
    } else if op_id == 2 {
        let mut new_lines = Vec::new();
        let filter_line = |l: &str| -> String {
            let parts: Vec<&str> = l.split(',').collect();
            let mut kept = Vec::new();
            for (i, p) in parts.iter().enumerate() {
                if i != col_idx { kept.push(*p); }
            }
            kept.join(",")
        };
        new_lines.push(filter_line(&header));
        for l in lines { new_lines.push(filter_line(&l)); }
        let final_csv = new_lines.join("\n");
        let result_bytes = final_csv.as_bytes();
        let out_len = std::cmp::min(result_bytes.len(), len);
        slice[0..out_len].copy_from_slice(&result_bytes[0..out_len]);
        return out_len;
    }
    let mut final_lines = vec![header];
    final_lines.extend(lines);
    let final_csv = final_lines.join("\n");
    let result_bytes = final_csv.as_bytes();
    let out_len = std::cmp::min(result_bytes.len(), len);
    slice[0..out_len].copy_from_slice(&result_bytes[0..out_len]);
    return out_len;
}