#!/bin/bash
set -e
REPO="/tmp/repos/rust_04_template_engine"
rm -rf "$REPO"
mkdir -p "$REPO"
cd "$REPO"
cargo init --lib --name template_engine 2>/dev/null

mkdir -p tests
cat > tests/template_test.rs << 'TESTEOF'
use template_engine::{Template, Context};
use std::collections::HashMap;

fn ctx(data: Vec<(&str, &str)>) -> Context {
    let mut m = HashMap::new();
    for (k, v) in data { m.insert(k.to_string(), v.to_string()); }
    Context { vars: m }
}

#[test]
fn test_plain_text() {
    let t = Template::new("Hello World").unwrap();
    assert_eq!(t.render(&ctx(vec![])).unwrap(), "Hello World");
}

#[test]
fn test_variable() {
    let t = Template::new("Hello {{name}}!").unwrap();
    assert_eq!(t.render(&ctx(vec![("name", "Alice")])).unwrap(), "Hello Alice!");
}

#[test]
fn test_html_escape() {
    let t = Template::new("{{html_content}}").unwrap();
    assert_eq!(t.render(&ctx(vec![("html_content", "<script>alert(1)</script>")])).unwrap(), "&lt;script&gt;alert(1)&lt;/script&gt;");
}

#[test]
fn test_if_true() {
    let t = Template::new("{% if show %}visible{% endif %}").unwrap();
    assert_eq!(t.render(&ctx(vec![("show", "true")])).unwrap(), "visible");
}

#[test]
fn test_if_false() {
    let t = Template::new("{% if show %}visible{% endif %}").unwrap();
    assert_eq!(t.render(&ctx(vec![("show", "false")])).unwrap(), "");
}

#[test]
fn test_if_else() {
    let t = Template::new("{% if x %}yes{% else %}no{% endif %}").unwrap();
    assert_eq!(t.render(&ctx(vec![("x", "false")])).unwrap(), "no");
}

#[test]
fn test_for_loop() {
    let t = Template::new("{% for item in items %}[{{item}}]{% endfor %}").unwrap();
    let mut c = Context { vars: HashMap::new() };
    c.set_list("items", vec!["a".to_string(), "b".to_string(), "c".to_string()]);
    assert_eq!(t.render(&c).unwrap(), "[a][b][c]");
}

#[test]
fn test_nested_structure() {
    let t = Template::new("{% if logged_in %}Welcome {{user}}!{% for item in items %}- {{item}}\n{% endfor %}{% else %}Please login{% endif %}").unwrap();
    let mut c = ctx(vec![("logged_in", "true"), ("user", "Bob")]);
    c.set_list("items", vec!["one".to_string(), "two".to_string()]);
    let result = t.render(&c).unwrap();
    assert!(result.contains("Welcome Bob!"));
    assert!(result.contains("- one"));
    assert!(result.contains("- two"));
}
TESTEOF

cat > src/lib.rs << 'LIBEOF'
use std::collections::HashMap;

pub struct Context { pub vars: HashMap<String, String> }
impl Context {
    pub fn set_list(&mut self, key: &str, items: Vec<String>) { todo!() }
}

pub struct Template;
impl Template {
    pub fn new(template: &str) -> Result<Self, String> { todo!() }
    pub fn render(&self, ctx: &Context) -> Result<String, String> { todo!() }
}
LIBEOF

echo "rust_04_template_engine setup done"
