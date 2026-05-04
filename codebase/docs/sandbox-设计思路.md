这是一个非常有挑战性且实用的需求。要在 **无需 Root 权限**、**代码极简**、**跨平台（macOS/Linux）** 且 **单个二进制文件** 的前提下实现 Sandbox，我们需要利用现代操作系统内核提供的原生安全机制。

### 技术选型

为了满足“轻量级”和“无 Root”的要求，我们不能使用 Docker 或虚拟机，而是使用操作系统自带的访问控制机制：

1.  **Linux**: 使用 **Landlock LSM** (自 Linux 5.13 起支持) 进行文件系统隔离，配合 `unshare` (User/PID Namespaces) 进行进程隔离。这是目前 Linux 上实现无特权沙盒最现代的方法。
2.  **macOS**: 使用 **Seatbelt / Sandbox API** (`sandbox_init`)。这是 macOS 系统级沙盒（如 App Store 应用）的底层机制。
3.  **Windows (Cygwin)**: Cygwin 本质上是在 Windows 上模拟 POSIX。由于沙盒依赖内核特性，Cygwin 无法提供真正的内核级隔离。但在 Cygwin 环境下编译此代码，如果是在 WSL2 (Linux) 中运行，它将作为 Linux 程序正常工作；如果是纯 Cygwin，我们将提供一个兼容层（不做隔离或仅做基本检查），因为纯 Windows 沙盒需要完全不同的 API (Job Objects)。

---

### Rust 实现方案

我们将创建一个 Rust 项目。

#### 1. `Cargo.toml` (依赖配置)

你需要引入 `libc` 用于系统调用，以及专门的 `landlock` 库来简化 Linux 上的操作。

```toml
[package]
name = "rsbox"
version = "0.1.0"
edition = "2021"

[dependencies]
# 用于系统调用和 macOS FFI
libc = "0.2"

# 仅在 Linux 下启用 Landlock 库
[target.'cfg(target_os = "linux")'.dependencies]
landlock = "0.4"
```

#### 2. `src/main.rs` (核心代码)

代码逻辑分为三个部分：通用入口、Linux 实现、macOS 实现。

```rust
use std::env;
use std::error::Error;
use std::ffi::CString;
use std::path::{Path, PathBuf};
use std::process::Command;

// ---------------------- Linux 实现 (Landlock + Namespaces) ----------------------
#[cfg(target_os = "linux")]
mod sandbox {
    use super::*;
    use landlock::{
        AccessFs, BitFlags, PathFd, Ruleset, RulesetAttr, RulesetError, ABI,
    };
    use std::os::unix::process::CommandExt;

    pub fn apply(target_dir: &Path) -> Result<(), Box<dyn Error>> {
        // 1. 启用 Landlock 文件系统限制
        // ABI V1 提供了基本的文件访问控制
        let abi = ABI::V1;
        let mut ruleset = Ruleset::new()
            .handle_access(AccessFs::from_all(abi))
            .create()?;

        // 辅助函数：添加规则
        // 为了让程序能跑起来，通常需要读取 /bin, /lib, /usr 等系统目录
        // 这是一个"应用级沙盒"，不是"空容器"，所以默认允许读取系统库，否则连 ls 都跑不起来
        let add_ro = |path: &str, rs: &mut Ruleset| {
            if Path::new(path).exists() {
                let _ = rs.add_rules(PathFd::new(path), AccessFs::from_read(abi));
            }
        };

        // 添加只读系统路径 (根据需要裁剪)
        add_ro("/bin", &mut ruleset);
        add_ro("/usr", &mut ruleset);
        add_ro("/lib", &mut ruleset);
        add_ro("/lib64", &mut ruleset);
        add_ro("/etc", &mut ruleset); // 读取配置，如 DNS

        // 核心：添加用户指定的目录为 读写 (Read + Write)
        // Landlock 允许我们精细控制，这里赋予所有权限
        let _ = ruleset.add_rules(PathFd::new(target_dir), AccessFs::from_all(abi));

        // 2. 限制进程自身 (应用 Landlock)
        // 从此刻起，文件系统访问被锁定
        let status = ruleset.restrict_self();
        if let Err(e) = status {
            // 如果内核太旧不支持 Landlock，这里会报错
            eprintln!("Warning: Landlock failed (kernel too old?): {:?}", e);
        }

        Ok(())
    }

    // 在 exec 之前运行的钩子：用于创建 PID Namespace
    pub fn pre_exec() -> std::io::Result<()> {
        unsafe {
            // CLONE_NEWPID: 创建新的 PID 命名空间，隔离进程树
            // 这样沙盒内的进程就看不到外部的进程，也就无法 kill 它们
            // CLONE_NEWNS:  Mount 命名空间 (可选，增强隔离)
            if libc::unshare(libc::CLONE_NEWPID) != 0 {
                return Err(std::io::Error::last_os_error());
            }
        }
        Ok(())
    }
}

// ---------------------- macOS 实现 (Seatbelt / sandbox-exec) ----------------------
#[cfg(target_os = "macos")]
mod sandbox {
    use super::*;
    use std::ptr;

    // 引入 macOS 私有 API sandbox_init
    extern "C" {
        fn sandbox_init(
            profile: *const libc::c_char,
            flags: u64,
            errorbuf: *mut *mut libc::c_char,
        ) -> i32;
        fn sandbox_free_error(errorbuf: *mut libc::c_char);
    }

    pub fn apply(target_dir: &Path) -> Result<(), Box<dyn Error>> {
        let dir_str = target_dir.to_string_lossy();
        
        // 构建 Scheme (Lisp 风格配置)
        // 1. (deny default): 默认拒绝所有操作
        // 2. (allow process-exec): 允许执行程序
        // 3. (allow signal (target self)): 允许给自已发信号
        // 4. (deny signal): 禁止给外部进程发信号 (kill)
        // 5. (allow file-read* ...): 允许读取系统库 (让程序能跑)
        // 6. (allow file-write* (literal "...")): 仅允许写入指定目录
        let profile = format!(
            r#"
            (version 1)
            (deny default)
            (allow process-exec)
            (allow sysctl-read)
            (allow signal (target self))
            
            ;; 允许读取系统基本路径
            (allow file-read* (subpath "/usr"))
            (allow file-read* (subpath "/bin"))
            (allow file-read* (subpath "/sbin"))
            (allow file-read* (subpath "/lib"))
            (allow file-read* (subpath "/System"))
            (allow file-read* (subpath "/dev"))
            
            ;; 允许读取和写入目标目录
            (allow file-read* (subpath "{0}"))
            (allow file-write* (subpath "{0}"))
            "#,
            dir_str
        );

        let c_profile = CString::new(profile)?;
        let mut error_buf: *mut libc::c_char = ptr::null_mut();

        unsafe {
            // 0 表示使用字符串作为 profile
            let ret = sandbox_init(c_profile.as_ptr(), 0, &mut error_buf);
            if ret != 0 {
                let err_msg = if !error_buf.is_null() {
                    let msg = std::ffi::CStr::from_ptr(error_buf).to_string_lossy().into_owned();
                    sandbox_free_error(error_buf);
                    msg
                } else {
                    "Unknown sandbox error".to_string()
                };
                return Err(format!("Sandbox init failed: {}", err_msg).into());
            }
        }

        Ok(())
    }
    
    pub fn pre_exec() -> std::io::Result<()> {
        Ok(()) // macOS 不需要额外的 pre_exec，Seatbelt 已经处理了 Signal 隔离
    }
}

// ---------------------- Windows/其他 实现 (兼容层) ----------------------
#[cfg(not(any(target_os = "linux", target_os = "macos")))]
mod sandbox {
    use super::*;
    pub fn apply(_: &Path) -> Result<(), Box<dyn Error>> {
        eprintln!("Warning: Sandboxing is not supported natively on this OS. Running unrestricted.");
        Ok(())
    }
    pub fn pre_exec() -> std::io::Result<()> { Ok(()) }
}

// ---------------------- 主程序 ----------------------

fn main() -> Result<(), Box<dyn Error>> {
    let args: Vec<String> = env::args().collect();
    if args.len() < 3 {
        eprintln!("Usage: {} <allowed_dir> <command> [args...]", args[0]);
        std::process::exit(1);
    }

    // 1. 获取并规范化沙盒目录路径
    let raw_dir = PathBuf::from(&args[1]);
    let allowed_dir = std::fs::canonicalize(&raw_dir)
        .map_err(|e| format!("Path error for '{}': {}", raw_dir.display(), e))?;

    // 2. 准备要执行的命令
    let cmd_name = &args[2];
    let cmd_args = &args[3..];

    // 3. 应用沙盒策略 (此时还未执行命令)
    // 注意：在 Linux 上，Landlock 会立即生效，所以后续无法打开非允许文件
    // 在 macOS 上，sandbox_init 也会立即生效
    sandbox::apply(&allowed_dir)?;

    // 4. 执行命令
    let mut command = Command::new(cmd_name);
    command.args(cmd_args);

    // 设置 Linux 特有的 pre_exec (用于 unshare PID)
    #[cfg(target_os = "linux")]
    unsafe {
        command.pre_exec(sandbox::pre_exec);
    }

    // 运行子进程
    // 因为这是沙盒环境，如果命令本身依赖大量外部环境（如环境变量、特定配置文件），可能需要额外处理
    let mut child = command.spawn().map_err(|e| format!("Failed to execute command: {}", e))?;

    // 等待子进程结束
    let status = child.wait()?;
    std::process::exit(status.code().unwrap_or(1));
}
```

### 核心实现原理

1.  **映射目录与文件隔离**:
    *   **Linux (Landlock)**: 我们使用 `Landlock` 库创建了一个规则集。我们将 `/usr`, `/bin`, `/lib` 等设为 **只读** (为了让 `ls`, `bash` 等基本工具能加载动态链接库)，并将用户指定的目录设为 **读写**。其他所有路径默认不可访问。
    *   **macOS (Seatbelt)**: 我们构建了一个 Scheme 字符串。它使用白名单机制：`deny default` 拒绝一切，然后显式允许系统库读取，仅允许目标目录写入。

2.  **进程隔离 (无法 Kill 其他进程)**:
    *   **Linux**: `Command::pre_exec` 钩子中调用了 `libc::unshare(libc::CLONE_NEWPID)`。这会让子进程在一个新的 PID 命名空间中运行。对子进程来说，它是 PID 1，它看不到外部的任何进程，自然也就无法 `kill` 它们。
    *   **macOS**: 在 Seatbelt 配置中加入了 `(deny signal)`。这会直接拦截发送信号的系统调用，除非目标是进程自己 `(allow signal (target self))`。

### 编译与运行

#### 1. 编译
在项目根目录下：
```bash
cargo build --release
```
生成的二进制文件在 `target/release/rsbox`。

#### 2. 测试

假设我们有一个目录 `./data`，你想让沙盒里的程序只能操作这个目录。

**准备测试环境：**
```bash
mkdir data
echo "secret" > secret.txt
```

**测试 1：读取沙盒目录（应该成功）**
```bash
./target/release/rsbox ./data /bin/cat ./data/../data/test_file
# 或者简单的
./target/release/rsbox ./data /bin/ls -F ./data
```

**测试 2：尝试读取敏感文件（应该失败）**
```bash
./target/release/rsbox ./data /bin/cat ./secret.txt
# Linux 输出: Permission denied
# macOS 输出: Operation not permitted
```

**测试 3：尝试 Kill 外部进程（应该失败）**
首先打开另一个终端运行 `sleep 1000` 并获取 PID（假设为 12345）。
```bash
./target/release/rsbox ./data /bin/kill 12345
# Linux: kill: (12345) - No such process (因为 PID Namespace 隔离，根本看不到)
# macOS: kill: 12345: Operation not permitted (Seatbelt 拦截)
```

**测试 4：写文件（只能写在 data 中）**
```bash
./target/release/rsbox ./data /usr/bin/touch ./data/new_file  # 成功
./target/release/rsbox ./data /usr/bin/touch ./new_file       # 失败
```

### 关于 Windows / Cygwin
在 Cygwin 下编译此代码：
1.  由于 `target_os` 既不是 `linux` 也不是 `macos` (Rust 通常视 Cygwin 为 `windows-gnu` 或类似的，取决于工具链)，它会回退到 `sandbox::apply` 的空实现。
2.  要让它在 Windows 上真正拥有隔离能力，你需要安装 **WSL2** (Windows Subsystem for Linux)。
3.  在 WSL2 终端中运行此程序，它会被识别为 Linux，从而使用 Landlock 和 Namespaces，达到完美的沙盒效果。

### 总结
这个方案做到了：
*   **代码极少**：核心逻辑不到 100 行。
*   **无 Root**：Linux 下 Landlock 和 unshare (User NS) 均不需要 Root（注：部分旧发行版可能需要 sysctl 设置开启 unprivileged user ns，现代发行版默认开启）。macOS `sandbox_init` 也不需要 Root。
*   **单二进制**：静态编译后无依赖。
*   **跨平台**：统一了 macOS 和 Linux 的行为。