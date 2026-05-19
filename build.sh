#!/usr/bin/env bash
# =============================================================================
# codeactor-build
# 项目构建脚本 - 并行构建 Rust 和 Go 子项目
# =============================================================================
# 用法: ./build.sh [选项] [命令]
# 命令: (默认=build), clean, release, help
# 选项:
#   -h, --help    显示帮助信息
#   -j N          设置构建并行度（传递给 cargo build -j N）
#
# 环境变量:
#   SKIP_RUST     设置为 true 时跳过 Rust 构建
#   SKIP_GO       设置为 true 时跳过 Go 构建
#   BUILD_TYPE    设置为 release 或 debug（默认 release）
#   RUSTFLAGS     传递给 rustc 的额外标志
#   GOFLAGS       传递给 go build 的额外标志
# =============================================================================

set -euo pipefail

# =============================================================================
# 常量定义
# =============================================================================
readonly SCRIPT_NAME="$(basename "$0")"
readonly SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
readonly DIST_DIR="${SCRIPT_DIR}/dist/bin"
readonly RUST_PROJECT_DIR="${SCRIPT_DIR}/codebase"
readonly GO_PROJECT_DIR="${SCRIPT_DIR}"

# 产物名称
readonly RUST_BIN="codeactor-codebase"
readonly GO_BIN="codeactor"

# 保留的文件模式（不清理的文件）
readonly PRESERVE_PATTERNS=(
    "fzf"
    "jq"
    "rg"
    "1"
)

# =============================================================================
# 颜色定义
# =============================================================================
if [[ -t 1 ]] || [[ "${FORCE_COLOR:-}" == "1" ]]; then
    readonly RED='\033[0;31m'
    readonly GREEN='\033[0;32m'
    readonly YELLOW='\033[1;33m'
    readonly BLUE='\033[0;34m'
    readonly MAGENTA='\033[0;35m'
    readonly CYAN='\033[0;36m'
    readonly WHITE='\033[1;37m'
    readonly BOLD='\033[1m'
    readonly UNDERLINE='\033[4m'
    readonly RESET='\033[0m'
    readonly DIM='\033[2m'
else
    readonly RED=''
    readonly GREEN=''
    readonly YELLOW=''
    readonly BLUE=''
    readonly MAGENTA=''
    readonly CYAN=''
    readonly WHITE=''
    readonly BOLD=''
    readonly UNDERLINE=''
    readonly RESET=''
    readonly DIM=''
fi

# =============================================================================
# 全局变量
# =============================================================================
BUILD_START_TIME=0
PARALLEL_JOBS=""
COMMAND="build"
SKIP_RUST="${SKIP_RUST:-false}"
SKIP_GO="${SKIP_GO:-false}"
BUILD_TYPE="${BUILD_TYPE:-release}"
VERSION_INFO=""

# =============================================================================
# 工具函数
# =============================================================================

# 打印带时间戳的消息
log_info() {
    echo -e "${BLUE}[codeactor-build]${RESET} ${CYAN}$*${RESET}"
}

log_success() {
    echo -e "${BLUE}[codeactor-build]${RESET} ${GREEN}$*${RESET}"
}

log_warning() {
    echo -e "${BLUE}[codeactor-build]${RESET} ${YELLOW}⚠ $*${RESET}"
}

log_error() {
    echo -e "${BLUE}[codeactor-build]${RESET} ${RED}✗ $*${RESET}" >&2
}

log_debug() {
    if [[ "${DEBUG:-0}" == "1" ]]; then
        echo -e "${BLUE}[codeactor-build]${RESET} ${DIM}[DEBUG] $*${RESET}"
    fi
}

# 获取文件大小的友好显示
get_file_size_human() {
    local file="$1"
    if [[ -f "$file" ]]; then
        local size_bytes
        size_bytes=$(stat -c%s "$file" 2>/dev/null || stat -f%z "$file" 2>/dev/null)
        if (( size_bytes >= 1073741824 )); then
            echo "$(awk "BEGIN {printf \"%.1f\", $size_bytes/1073741824}") GB"
        elif (( size_bytes >= 1048576 )); then
            echo "$(awk "BEGIN {printf \"%.1f\", $size_bytes/1048576}") MB"
        elif (( size_bytes >= 1024 )); then
            echo "$(awk "BEGIN {printf \"%.1f\", $size_bytes/1024}") KB"
        else
            echo "${size_bytes} B"
        fi
    else
        echo "N/A"
    fi
}

# 检查命令是否存在
command_exists() {
    command -v "$1" &>/dev/null
}

# 获取 Git 版本信息
get_git_version() {
    local git_dir
    git_dir="$(git rev-parse --git-dir 2>/dev/null)" || true
    
    local commit_hash tag build_time
    
    commit_hash=$(git rev-parse --short=7 HEAD 2>/dev/null) || commit_hash="unknown"
    
    tag=$(git describe --tags --exact-match 2>/dev/null) || tag=$(git describe --tags --abbrev=0 2>/dev/null) || tag=""
    
    build_time=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    
    VERSION_INFO="{\"commit\":\"${commit_hash}\",\"tag\":\"${tag}\",\"build_time\":\"${build_time}\"}"
    
    echo "${VERSION_INFO}"
}

# 构建 ldflags 字符串
build_ldflags() {
    local commit_hash tag build_time
    
    commit_hash=$(git rev-parse --short=7 HEAD 2>/dev/null) || commit_hash="unknown"
    tag=$(git describe --tags --exact-match 2>/dev/null) || tag=$(git describe --tags --abbrev=0 2>/dev/null) || tag=""
    build_time=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    
    # 构建 Go ldflags
    local ldflags="-s -w"
    ldflags+=" -X 'codeactor/internal/globalctx.Version=${tag:-dev}-${commit_hash}'"
    ldflags+=" -X 'codeactor/internal/globalctx.BuildTime=${build_time}'"
    ldflags+=" -X 'codeactor/internal/globalctx.GitCommit=${commit_hash}'"
    
    echo "${ldflags}"
}

# 清理 dist/bin 中的旧产物（保留非本脚本文件）
clean_dist_bin() {
    log_info "🧹 清理构建产物..."
    
    if [[ ! -d "$DIST_DIR" ]]; then
        log_info "  创建目录: ${DIST_DIR}"
        mkdir -p "$DIST_DIR"
        return 0
    fi
    
    local cleaned_count=0
    for file in "$DIST_DIR"/*; do
        [[ -e "$file" ]] || continue  # 跳过没有匹配的情况
        
        local basename
        basename=$(basename "$file")
        
        # 检查是否是保留文件
        local is_preserved=false
        for pattern in "${PRESERVE_PATTERNS[@]}"; do
            if [[ "$basename" == "$pattern" ]]; then
                is_preserved=true
                break
            fi
        done
        
        # 清理本脚本的产物（仅 Rust，Go 产物已在项目根目录）
        if [[ "$basename" == "${RUST_BIN}"* ]]; then
            if rm -f "$file"; then
                log_debug "  已删除: ${basename}"
                ((cleaned_count++))
            fi
        elif [[ "$is_preserved" == "false" ]]; then
            # 不确定是否是本脚本产物，但也不在保留列表中
            # 检查是否是之前构建的 Rust 产物（codeactor 已不在 dist/bin 中）
            if [[ "$basename" =~ ^codeactor-codebase ]]; then
                if rm -f "$file"; then
                    log_debug "  已删除: ${basename}"
                    ((cleaned_count++))
                fi
            fi
        fi
    done
    
    if (( cleaned_count > 0 )); then
        log_success "  已清理 ${cleaned_count} 个旧产物"
    else
        log_debug "  无需清理"
    fi
}

# =============================================================================
# 前置检查
# =============================================================================
check_prerequisites() {
    log_info "🔍 检查前置依赖..."
    
    local all_ok=true
    
    # 检查 Rust 工具链
    if [[ "${SKIP_RUST}" != "true" ]]; then
        if ! command_exists cargo; then
            log_error "cargo 未安装或不在 PATH 中"
            log_info "  请安装 Rust: https://rustup.rs/"
            all_ok=false
        else
            local rustc_version cargo_version
            rustc_version=$(rustc --version 2>/dev/null | awk '{print $2}')
            cargo_version=$(cargo --version 2>/dev/null | awk '{print $2}')
            log_info "  ✓ Rust ${rustc_version} / Cargo ${cargo_version}"
        fi
    fi
    
    # 检查 Go 工具链
    if [[ "${SKIP_GO}" != "true" ]]; then
        if ! command_exists go; then
            log_error "go 未安装或不在 PATH 中"
            log_info "  请安装 Go: https://go.dev/doc/install"
            all_ok=false
        else
            local go_version
            go_version=$(go version 2>/dev/null | awk '{print $3}' | sed 's/go//')
            log_info "  ✓ Go ${go_version}"
        fi
    fi
    
    if [[ "${all_ok}" == "false" ]]; then
        log_error "前置检查失败，请安装缺失的工具"
        exit 1
    fi
    
    log_success "所有前置依赖检查通过"
}

# =============================================================================
# Rust 构建函数
# =============================================================================
build_rust() {
    local start_time end_time elapsed
    
    log_info "📦 构建 Rust 项目 (${RUST_PROJECT_DIR}/${RUST_BIN})..."
    
    cd "$RUST_PROJECT_DIR"
    start_time=$(date +%s%N)
    
    local cargo_flags="-p codeactor-codebase"
    cargo_flags+=" --manifest-path ${RUST_PROJECT_DIR}/Cargo.toml"
    if [[ "${BUILD_TYPE}" == "release" ]]; then
        cargo_flags+=" --release"
    fi
    
    if [[ -n "$PARALLEL_JOBS" ]]; then
        cargo_flags+=" -j ${PARALLEL_JOBS}"
    fi
    
    # 添加额外的 RUSTFLAGS
    if [[ -n "${RUSTFLAGS:-}" ]]; then
        export RUSTFLAGS="${RUSTFLAGS}"
        log_debug "  RUSTFLAGS: ${RUSTFLAGS}"
    fi
    
    if ! cargo build ${cargo_flags} 2>&1; then
        log_error "Rust 构建失败"
        return 1
    fi
    
    end_time=$(date +%s%N)
    elapsed=$(awk "BEGIN {printf \"%.1f\", ($end_time - $start_time) / 1000000000}")
    
    # 复制产物到 dist/bin
    local bin_source bin_dest
    if [[ "${BUILD_TYPE}" == "debug" ]]; then
        bin_source="${RUST_PROJECT_DIR}/target/debug/${RUST_BIN}"
    else
        bin_source="${RUST_PROJECT_DIR}/target/release/${RUST_BIN}"
    fi
    
    bin_dest="${DIST_DIR}/${RUST_BIN}"
    
    if [[ -f "$bin_source" ]]; then
        cp "$bin_source" "$bin_dest"
        chmod +x "$bin_dest"
        log_success "✅ Rust 构建完成 (${elapsed}s) -> ${bin_dest}"
    else
        log_error "构建产物未找到: ${bin_source}"
        return 1
    fi
}

# =============================================================================
# Go 构建函数
# =============================================================================
build_go() {
    local start_time end_time elapsed ldflags
    
    log_info "📦 构建 Go 项目 (${GO_PROJECT_DIR}/${GO_BIN})..."
    
    cd "$GO_PROJECT_DIR"
    start_time=$(date +%s%N)
    
    # 构建 ldflags
    local ldflags
    ldflags="$(build_ldflags)"
    log_debug "  Go ldflags: ${ldflags}"
    
    # 使用数组来避免引号嵌套问题
    local go_cmd_args=(-o "${SCRIPT_DIR}/${GO_BIN}" -ldflags "${ldflags}")
    
    # 添加额外的 GOFLAGS
    if [[ -n "${GOFLAGS:-}" ]]; then
        # shellcheck disable=SC2206
        go_cmd_args+=(${GOFLAGS})
    fi
    
    if ! go build "${go_cmd_args[@]}" . 2>&1; then
        log_error "Go 构建失败"
        return 1
    fi
    
    end_time=$(date +%s%N)
    elapsed=$(awk "BEGIN {printf \"%.1f\", ($end_time - $start_time) / 1000000000}")
    
    if [[ -f "${SCRIPT_DIR}/${GO_BIN}" ]]; then
        chmod +x "${SCRIPT_DIR}/${GO_BIN}"
        log_success "✅ Go 构建完成 (${elapsed}s) -> ${SCRIPT_DIR}/${GO_BIN}"
    else
        log_error "构建产物未找到: ${SCRIPT_DIR}/${GO_BIN}"
        return 1
    fi
}

# =============================================================================
# 显示产物信息
# =============================================================================
show_artifacts() {
    log_info "📋 构建产物:"
    
    local has_artifacts=false
    
    if [[ -f "${DIST_DIR}/${RUST_BIN}" ]]; then
        local rust_size
        rust_size=$(get_file_size_human "${DIST_DIR}/${RUST_BIN}")
        printf "  ${GREEN}%-35s${RESET} %s\n" "${DIST_DIR}/${RUST_BIN}" "${rust_size}"
        has_artifacts=true
    fi
    
    if [[ -f "${SCRIPT_DIR}/${GO_BIN}" ]]; then
        local go_size
        go_size=$(get_file_size_human "${SCRIPT_DIR}/${GO_BIN}")
        printf "  ${GREEN}%-35s${RESET} %s\n" "${SCRIPT_DIR}/${GO_BIN}" "${go_size}"
        has_artifacts=true
    fi
    
    if [[ "${has_artifacts}" == "false" ]]; then
        log_warning "未发现构建产物"
    fi
}

# =============================================================================
# 显示版本信息
# =============================================================================
show_version_info() {
    local commit_hash tag build_time
    
    commit_hash=$(git rev-parse --short=7 HEAD 2>/dev/null) || commit_hash="unknown"
    tag=$(git describe --tags --exact-match 2>/dev/null) || tag=$(git describe --tags --abbrev=0 2>/dev/null) || tag=""
    build_time=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    
    log_info "📌 版本信息:"
    echo -e "  ${BOLD}Commit:${RESET}  ${commit_hash}"
    if [[ -n "$tag" ]]; then
        echo -e "  ${BOLD}Tag:${RESET}     ${GREEN}${tag}${RESET}"
    else
        echo -e "  ${BOLD}Tag:${RESET}     ${YELLOW}(none - dev build)${RESET}"
    fi
    echo -e "  ${BOLD}Time:${RESET}    ${build_time}"
    echo -e "  ${BOLD}Platform:${RESET} $(uname -s -r -m)"
}

# =============================================================================
# 帮助信息
# =============================================================================
show_help() {
    cat <<EOF
${BOLD}[codeactor-build]${RESET} - codeactor 项目构建脚本

${BOLD}用法:${RESET}
  ${SCRIPT_NAME} [选项] [命令]

${BOLD}命令:${RESET}
  build        并行构建 Rust 和 Go 项目（默认）
  clean        清理所有构建产物
  release      构建并发布（包含版本信息）
  help, -h     显示此帮助信息

${BOLD}选项:${RESET}
  -j N         设置构建并行度（传递给 cargo build -j N）
  -h, --help   显示帮助信息

${BOLD}环境变量:${RESET}
  SKIP_RUST    设置为 true 时跳过 Rust 构建
  SKIP_GO      设置为 true 时跳过 Go 构建
  BUILD_TYPE   设置为 release 或 debug（默认: release）
  RUSTFLAGS    传递给 rustc 的额外标志
  GOFLAGS      传递给 go build 的额外标志
  DEBUG        设置为 1 时显示调试信息
  FORCE_COLOR  设置为 1 时强制彩色输出

${BOLD}示例:${RESET}
  ${SCRIPT_NAME}                    # 构建所有项目
  ${SCRIPT_NAME} clean              # 清理构建产物
  ${SCRIPT_NAME} release            # 构建并发布
  SKIP_RUST=true ${SCRIPT_NAME}     # 仅构建 Go 项目
  ${SCRIPT_NAME} -j 8               # 使用 8 个并行任务构建
  DEBUG=1 ${SCRIPT_NAME}            # 显示调试信息

${BOLD}产物:${RESET}
  dist/bin/codeactor-codebase    # Rust 子项目产物
  ./codeactor                    # Go 主项目产物

EOF
}

# =============================================================================
# 清理命令
# =============================================================================
cmd_clean() {
    log_info "🧹 清理构建产物..."
    
    # 清理 Rust 构建产物
    if [[ -d "${RUST_PROJECT_DIR}/target" ]]; then
        log_info "  清理 Rust target 目录..."
        rm -rf "${RUST_PROJECT_DIR}/target"
        log_success "  Rust target 已清理"
    fi
    
    # 清理 Go 构建缓存
    if command_exists go; then
        log_info "  清理 Go 构建缓存..."
        go clean -cache -modcache -testcache 2>/dev/null || true
        log_success "  Go 缓存已清理"
    fi
    
    # 清理 dist/bin
    if [[ -d "$DIST_DIR" ]]; then
        log_info "  清理 dist/bin..."
        for file in "$DIST_DIR"/*; do
            [[ -e "$file" ]] || continue
            local basename
            basename=$(basename "$file")
            
            # 只清理 Rust 产物（Go 产物 codeactor 已在项目根目录）
            if [[ "$basename" == "${RUST_BIN}"* ]]; then
                rm -f "$file"
            fi
        done
        log_success "  dist/bin 已清理"
    fi
    
    log_success "✨ 清理完成"
}

# =============================================================================
# Release 命令
# =============================================================================
cmd_release() {
    log_info "🚀 Release 构建模式"
    
    # 显示版本信息
    show_version_info
    echo ""
    
    # 执行构建
    cmd_build
    
    # 显示产物信息
    show_artifacts
    echo ""
    
    # 显示版本信息
    show_version_info
    
    log_success "✨ Release 构建完成"
}

# =============================================================================
# 主构建命令
# =============================================================================
cmd_build() {
    # 确保 dist/bin 目录存在
    mkdir -p "$DIST_DIR"
    
    # 清理旧产物
    clean_dist_bin
    
    # 并行构建 Rust 和 Go 项目
    local pids=()
    local failed=false
    
    if [[ "${SKIP_RUST}" != "true" ]]; then
        (
            build_rust || exit 1
        ) &
        pids+=($!)
        log_info "  Rust 构建已启动 (PID: $!)"
    else
        log_warning "⊘ 跳过 Rust 构建 (SKIP_RUST=true)"
    fi
    
    if [[ "${SKIP_GO}" != "true" ]]; then
        (
            build_go || exit 1
        ) &
        pids+=($!)
        log_info "  Go 构建已启动 (PID: $!)"
    else
        log_warning "⊘ 跳过 Go 构建 (SKIP_GO=true)"
    fi
    
    # 等待所有并行任务完成
    log_info "⏳ 等待并行构建完成..."
    for pid in "${pids[@]}"; do
        wait "$pid" || {
            failed=true
        }
    done
    
    if [[ "$failed" == "true" ]]; then
        log_error "部分构建失败，请查看上方错误信息"
        exit 1
    fi
    
    # 显示产物信息
    show_artifacts
}

# =============================================================================
# 主入口
# =============================================================================
main() {
    # 解析命令行参数
    while [[ $# -gt 0 ]]; do
        case "$1" in
            -h|--help)
                show_help
                exit 0
                ;;
            -j)
                if [[ -n "${2:-}" ]]; then
                    PARALLEL_JOBS="$2"
                    shift 2
                else
                    log_error "-j 需要指定并行度数字"
                    exit 1
                fi
                ;;
            build|clean|release|help)
                COMMAND="$1"
                shift
                ;;
            *)
                log_error "未知命令: $1"
                echo "使用 '${SCRIPT_NAME} help' 查看用法"
                exit 1
                ;;
        esac
    done
    
    # 记录开始时间
    BUILD_START_TIME=$(date +%s)
    
    log_info "🏗️  codeactor 构建开始"
    log_info "   命令: ${COMMAND}"
    log_info "   类型: ${BUILD_TYPE}"
    log_info "   Rust: $( [[ "${SKIP_RUST}" == "true" ]] && echo "跳过" || echo "构建" )"
    log_info "   Go:   $( [[ "${SKIP_GO}" == "true" ]] && echo "跳过" || echo "构建" )"
    [[ -n "$PARALLEL_JOBS" ]] && log_info "   并行度: -j ${PARALLEL_JOBS}"
    echo ""
    
    # 执行命令
    case "$COMMAND" in
        build)
            check_prerequisites
            cmd_build
            ;;
        clean)
            cmd_clean
            ;;
        release)
            check_prerequisites
            cmd_release
            ;;
        help)
            show_help
            ;;
    esac
    
    # 计算总耗时
    local end_time total_seconds
    end_time=$(date +%s)
    total_seconds=$(( end_time - BUILD_START_TIME ))
    
    echo ""
    log_success "✨ 总耗时: ${total_seconds}s"
    log_success "🎉 构建成功"
}

# 执行主函数
main "$@"
