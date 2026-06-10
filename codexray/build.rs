fn main() {
    println!("cargo:rerun-if-changed=build.rs");
    // tree-sitter crate 自己处理了 C 代码的编译和链接
    // 不需要额外指定 -ltree-sitter
} 