# 任务：实现 Shell 命令管道

请实现一个 Shell 命令管道构建器，支持将多个命令通过管道连接执行。

## 功能要求

1. **Pipeline 构建器** — 链式 API 添加命令
2. **管道传递** — 前一个命令的 stdout 作为下一个命令的 stdin
3. **超时控制** — `timeout_ms(ms)` 设置管道总超时时间
4. **环境变量** — `env(key, val)` 设置所有命令共享的环境变量
5. **工作目录** — `cwd(dir)` 设置执行目录
6. **输入重定向** — `input_string(str)` 将字符串作为第一个命令的 stdin
7. **输出捕获** — 捕获最后一个命令的 stdout、stderr 和 exit_code

## 数据结构

```rust
pub struct PipelineOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: Option<i32>,
}

pub struct Pipeline;
impl Pipeline {
    pub fn new() -> Self;
    pub fn add(&mut self, cmd: Command) -> &mut Self;
    pub fn execute(&self) -> Result<PipelineOutput, String>;
    pub fn timeout_ms(&mut self, ms: u64) -> &mut Self;
    pub fn env(&mut self, key: &str, val: &str) -> &mut Self;
    pub fn cwd(&mut self, dir: &str) -> &mut Self;
    pub fn input_string(&mut self, input: &str) -> &mut Self;
}
```

## 技术约束

- 使用 `std::process::Command` + `Stdio::piped()`
- 正确处理子进程等待和清理
- 超时后应终止所有子进程

## 验收标准

运行 `cargo test` 所有测试通过即完成任务。
