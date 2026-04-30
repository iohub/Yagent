#!/bin/bash
set -e
REPO="/tmp/repos/rust_02_json_parser"
rm -rf "$REPO"
mkdir -p "$REPO"
cd "$REPO"
cargo init --lib --name json_parser 2>/dev/null

mkdir -p tests
cat > tests/json_test.rs << 'TESTEOF'
use json_parser::{parse_json, JsonValue};

#[test]
fn test_parse_null() { assert_eq!(parse_json("null").unwrap(), JsonValue::Null); }
#[test]
fn test_parse_bool_true() { assert_eq!(parse_json("true").unwrap(), JsonValue::Bool(true)); }
#[test]
fn test_parse_bool_false() { assert_eq!(parse_json("false").unwrap(), JsonValue::Bool(false)); }
#[test]
fn test_parse_integer() { assert_eq!(parse_json("42").unwrap(), JsonValue::Number(42.0)); }
#[test]
fn test_parse_negative() { assert_eq!(parse_json("-17").unwrap(), JsonValue::Number(-17.0)); }
#[test]
fn test_parse_float() { let v = parse_json("3.14").unwrap(); if let JsonValue::Number(n) = v { assert!((n - 3.14).abs() < 0.001); } else { panic!(); } }
#[test]
fn test_parse_string() { assert_eq!(parse_json("\"hello\"").unwrap(), JsonValue::String("hello".into())); }
#[test]
fn test_parse_string_escape() { assert_eq!(parse_json("\"hello\\nworld\"").unwrap(), JsonValue::String("hello\nworld".into())); }
#[test]
fn test_parse_empty_array() { assert_eq!(parse_json("[]").unwrap(), JsonValue::Array(vec![])); }
#[test]
fn test_parse_array() { assert_eq!(parse_json("[1,2,3]").unwrap(), JsonValue::Array(vec![JsonValue::Number(1.0), JsonValue::Number(2.0), JsonValue::Number(3.0)])); }
#[test]
fn test_parse_empty_object() { assert_eq!(parse_json("{}").unwrap(), JsonValue::Object(std::collections::HashMap::new())); }
#[test]
fn test_parse_object() {
    let v = parse_json("{\"name\":\"Alice\",\"age\":30}").unwrap();
    if let JsonValue::Object(map) = v {
        assert_eq!(map.get("name").unwrap(), &JsonValue::String("Alice".into()));
        assert_eq!(map.get("age").unwrap(), &JsonValue::Number(30.0));
    } else { panic!(); }
}
#[test]
fn test_parse_nested() {
    let v = parse_json("{\"items\":[1,{\"x\":true}]}").unwrap();
    if let JsonValue::Object(map) = v {
        if let JsonValue::Array(arr) = &map["items"] {
            assert_eq!(arr[0], JsonValue::Number(1.0));
        } else { panic!(); }
    } else { panic!(); }
}
#[test]
fn test_parse_unicode_escape() { assert_eq!(parse_json("\"\\u0048\"").unwrap(), JsonValue::String("H".into())); }
#[test]
fn test_parse_array_nested_empty() { assert_eq!(parse_json("[[]]").unwrap(), JsonValue::Array(vec![JsonValue::Array(vec![])])); }
TESTEOF

cat > src/lib.rs << 'LIBEOF'
use std::collections::HashMap;

#[derive(Debug, PartialEq, Clone)]
pub enum JsonValue {
    Null,
    Bool(bool),
    Number(f64),
    String(String),
    Array(Vec<JsonValue>),
    Object(HashMap<String, JsonValue>),
}

pub fn parse_json(input: &str) -> Result<JsonValue, String> { todo!() }
LIBEOF

echo "rust_02_json_parser setup done"
