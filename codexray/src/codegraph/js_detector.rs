//! JavaScript 混淆/编译代码检测器。
//!
//! 实现多级短路检测策略：
//! - L1: 零开销指纹匹配（webpack、OB混淆等特征字符串）
//! - L2: 结构密度分析（平均行长）
//! - L3: 标识符语义分析（短变量名占比）
//!
//! 参考: docs/js-detector.md

use std::path::Path;

/// 检测结果分类
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DetectionResult {
    /// 正常源码 — 应该被索引
    SourceCode,
    /// 混淆/编译/打包代码 — 应该被过滤
    CompiledCode,
    /// 空文件或仅空白字符
    Empty,
}

impl std::fmt::Display for DetectionResult {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            DetectionResult::SourceCode => write!(f, "SourceCode"),
            DetectionResult::CompiledCode => write!(f, "CompiledCode"),
            DetectionResult::Empty => write!(f, "Empty"),
        }
    }
}

/// 检测器配置阈值
#[derive(Debug, Clone)]
pub struct JsDetectorConfig {
    /// L2 触发阈值：平均行长超过此值判定为编译代码。默认 400
    pub avg_line_length_threshold: usize,
    /// L3 触发阈值：短标识符比例超过此值判定为编译代码。默认 0.55
    pub short_ident_ratio_threshold: f64,
    /// 短标识符的最大字符长度。默认 2
    pub short_ident_max_length: usize,
    /// L3 分析所需的最小标识符数量。默认 20
    pub min_ident_count_for_l3: usize,
}

impl Default for JsDetectorConfig {
    fn default() -> Self {
        Self {
            avg_line_length_threshold: 400,
            short_ident_ratio_threshold: 0.55,
            short_ident_max_length: 2,
            min_ident_count_for_l3: 20,
        }
    }
}

/// 多级 JavaScript 混淆检测器
pub struct JsDetector {
    config: JsDetectorConfig,
    /// L3 预编译正则：匹配 JavaScript 标识符
    ident_re: regex::Regex,
}

impl JsDetector {
    pub fn new(config: JsDetectorConfig) -> Self {
        let ident_re = regex::Regex::new(r#"\b[a-zA-Z_$][a-zA-Z0-9_$]*\b"#)
            .expect("Valid static regex pattern");
        Self { config, ident_re }
    }

    /// 使用默认配置创建检测器
    pub fn default_detector() -> Self {
        Self::new(JsDetectorConfig::default())
    }

    /// 对文件内容运行完整的多级检测管道
    pub fn detect(&self, content: &str) -> DetectionResult {
        // 前置检查：空文件
        if content.trim().is_empty() {
            return DetectionResult::Empty;
        }

        // L1: 指纹匹配（零开销，O(n) 字符串搜索）
        if self.l1_fingerprint_match(content) {
            return DetectionResult::CompiledCode;
        }

        // L2: 结构密度分析
        if self.l2_structural_density(content) {
            return DetectionResult::CompiledCode;
        }

        // L3: 标识符语义分析
        if self.l3_identifier_semantics(content) {
            return DetectionResult::CompiledCode;
        }

        DetectionResult::SourceCode
    }

    /// 便捷方法：从文件路径检测（读取文件后检测）
    pub fn detect_file(&self, path: &Path) -> std::io::Result<DetectionResult> {
        let content = std::fs::read_to_string(path)?;
        Ok(self.detect(&content))
    }

    // ── L1: 指纹匹配 ──────────────────────────────────────────────

    fn l1_fingerprint_match(&self, content: &str) -> bool {
        // 仅扫描前 N 字节 — 签名通常出现在文件头部
        const SCAN_WINDOW: usize = 4096;
        let scan_area = if content.len() > SCAN_WINDOW {
            &content[..SCAN_WINDOW]
        } else {
            content
        };

        // Webpack 打包签名
        if scan_area.contains("!function(") && scan_area.contains("webpack") {
            return true;
        }
        if scan_area.contains("__webpack_require__") {
            return true;
        }
        if scan_area.contains("webpackJsonp") {
            return true;
        }
        if scan_area.contains("webpackChunk") {
            return true;
        }

        // 常见混淆器签名
        if scan_area.contains("javascript-obfuscator") {
            return true;
        }

        // Dean Edwards packer: eval(function(p,a,c,k,e,d)
        if scan_area.contains("eval(function(p,a,c,k,e,d)") {
            return true;
        }

        // 大量 hex 转义：\x41\x42\x43 模式
        let hex_escape_count = scan_area.matches("\\x").count();
        if hex_escape_count > 30 {
            return true;
        }

        // 大量 unicode 转义：\u0041 模式
        let unicode_escape_count = scan_area.matches("\\u00").count();
        if unicode_escape_count > 30 {
            return true;
        }

        // 常见混淆器变量模式：_0x 后跟 hex
        if scan_area.contains("_0x") {
            return true;
        }

        // 文件尾部 sourceMappingURL（表示编译输出）
        if content.len() > 100 {
            let tail_start = content.len().saturating_sub(500);
            let tail = &content[tail_start..];
            if tail.contains("sourceMappingURL") {
                return true;
            }
        }

        false
    }

    // ── L2: 结构密度分析 ──────────────────────────────────────────

    fn l2_structural_density(&self, content: &str) -> bool {
        let lines: Vec<&str> = content.lines().collect();
        if lines.is_empty() {
            return false;
        }

        // 极短文件不触发 L2（可能是合法的单行代码）
        if lines.len() < 5 {
            return false;
        }

        let total_chars: usize = lines.iter().map(|l| l.len()).sum();
        let avg_line_length = total_chars as f64 / lines.len() as f64;

        avg_line_length > self.config.avg_line_length_threshold as f64
    }

    // ── L3: 标识符语义分析 ────────────────────────────────────────

    fn l3_identifier_semantics(&self, content: &str) -> bool {
        // 超大文件跳过 L3（性能考虑）— L1 或 L2 应该已捕获
        const L3_MAX_CONTENT_SIZE: usize = 1_000_000; // 1MB
        if content.len() > L3_MAX_CONTENT_SIZE {
            return false;
        }

        // 使用正则提取标识符
        let identifiers: Vec<&str> = self.ident_re.find_iter(content)
            .map(|m| m.as_str())
            .collect();

        if identifiers.len() < self.config.min_ident_count_for_l3 {
            return false;
        }

        // JS 关键字（2字符长度，合法的短标识符）
        let js_keywords = [
            "if", "do", "in", "as", "of",
        ];

        let short_count = identifiers.iter()
            .filter(|id| {
                id.len() <= self.config.short_ident_max_length
                    && !js_keywords.contains(id)
            })
            .count();

        let short_ratio = short_count as f64 / identifiers.len() as f64;
        short_ratio > self.config.short_ident_ratio_threshold
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_normal_source() {
        let detector = JsDetector::default_detector();
        let code = r#"
function handleClick(event) {
    const button = event.currentTarget;
    const label = button.textContent;
    console.log("Clicked:", label);
}
"#;
        assert_eq!(detector.detect(code), DetectionResult::SourceCode);
    }

    #[test]
    fn test_webpack_bundle() {
        let detector = JsDetector::default_detector();
        let code = r#"!function(e,t){var n=__webpack_require__(0)}(this,function(e){"use strict"})"#;
        assert_eq!(detector.detect(code), DetectionResult::CompiledCode);
    }

    #[test]
    fn test_obfuscator_pattern() {
        let detector = JsDetector::default_detector();
        let code = r#"var _0x4a2b=function(_0x3c4d){return _0x3c4d[_0x5e6f]}"#;
        assert_eq!(detector.detect(code), DetectionResult::CompiledCode);
    }

    #[test]
    fn test_sourcemap_tail() {
        let detector = JsDetector::default_detector();
        let code = "var a=1;\n" .repeat(20) + "//# sourceMappingURL=bundle.js.map";
        assert_eq!(detector.detect(&code), DetectionResult::CompiledCode);
    }

    #[test]
    fn test_detect_obfuscated_file() {
        let detector = JsDetector::default_detector();
        let manifest_dir = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"));
        let file_path = manifest_dir.join("tests/main.9d1c33d4.js");
        assert!(file_path.exists(), "Test file not found: {:?}", file_path);
        let result = detector.detect_file(&file_path).expect("Failed to read file");
        assert_eq!(
            result,
            DetectionResult::CompiledCode,
            "main.9d1c33d4.js should be detected as compiled/obfuscated code, got: {}",
            result
        );
    }
}