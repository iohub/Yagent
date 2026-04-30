#!/bin/bash
set -e
REPO="/tmp/repos/rust_05_command_pipeline"
rm -rf "$REPO"
mkdir -p "$REPO"
cd "$REPO"
cargo init --name command_pipeline 2>/dev/null

cat > tests/pipeline_test.rs << 'TESTEOF'
use command_pipeline::Pipeline;
use std::process::Command;

#[test]
fn test_simple_echo() {
    let output = Pipeline::new()
        .add(Command::new("echo").arg("hello"))
        .execute()
        .unwrap();
    assert_eq!(output.stdout.trim(), "hello");
}

#[test]
fn test_pipe_two_commands() {
    let output = Pipeline::new()
        .add(Command::new("echo").arg("hello\nworld\nhello"))
        .add(Command::new("grep").arg("hello"))
        .execute()
        .unwrap();
    assert_eq!(output.stdout.lines().count(), 2);
}

#[test]
fn test_pipe_three_commands() {
    let output = Pipeline::new()
        .add(Command::new("echo").arg("hello world"))
        .add(Command::new("grep").arg("hello"))
        .add(Command::new("wc").arg("-c"))
        .execute()
        .unwrap();
    let count: usize = output.stdout.trim().parse().unwrap();
    assert!(count > 0);
}

#[test]
fn test_stderr_capture() {
    let output = Pipeline::new()
        .add(Command::new("ls").arg("/nonexistent_path_xyz"))
        .execute()
        .unwrap();
    assert!(!output.stderr.is_empty());
}

#[test]
fn test_timeout() {
    let result = Pipeline::new()
        .add(Command::new("sleep").arg("10"))
        .timeout_ms(500)
        .execute();
    assert!(result.is_err());
}

#[test]
fn test_env_var() {
    let output = Pipeline::new()
        .add(Command::new("sh").arg("-c").arg("echo $TEST_VAR"))
        .env("TEST_VAR", "benchmark_value")
        .execute()
        .unwrap();
    assert_eq!(output.stdout.trim(), "benchmark_value");
}

#[test]
fn test_working_dir() {
    let output = Pipeline::new()
        .add(Command::new("pwd"))
        .cwd("/tmp")
        .execute()
        .unwrap();
    assert_eq!(output.stdout.trim(), "/tmp");
}

#[test]
fn test_exit_code() {
    let output = Pipeline::new()
        .add(Command::new("sh").arg("-c").arg("exit 42"))
        .execute()
        .unwrap();
    assert_eq!(output.exit_code, Some(42));
}

#[test]
fn test_input_string() {
    let output = Pipeline::new()
        .add(Command::new("grep").arg("hello"))
        .input_string("hello world\nfoo bar\nhello again")
        .execute()
        .unwrap();
    assert_eq!(output.stdout.lines().count(), 2);
}
TESTEOF

cat > src/main.rs << 'MAINEOF'
pub struct PipelineOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: Option<i32>,
}

pub struct Pipeline { /* implement */ }
impl Pipeline {
    pub fn new() -> Self { todo!() }
    pub fn add(&mut self, cmd: std::process::Command) -> &mut Self { todo!() }
    pub fn execute(&self) -> Result<PipelineOutput, String> { todo!() }
    pub fn timeout_ms(&mut self, ms: u64) -> &mut Self { todo!() }
    pub fn env(&mut self, key: &str, val: &str) -> &mut Self { todo!() }
    pub fn cwd(&mut self, dir: &str) -> &mut Self { todo!() }
    pub fn input_string(&mut self, input: &str) -> &mut Self { todo!() }
}
fn main() {}
MAINEOF

echo "rust_05_command_pipeline setup done"
