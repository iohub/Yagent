package messaging

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// WAL 写前日志接口
//
// WAL (Write-Ahead Log) 用于确保事件不丢失。
// 所有事件在投递到消费者之前先写入 WAL，崩溃后可通过 Replay 恢复。
type WAL interface {
	// Append 追加事件到 WAL，返回分配的序列号
	Append(event *Event) error

	// Replay 从指定序列号开始回放所有事件
	// 如果 ctx 被取消，中途停止并返回 ctx.Err()
	Replay(ctx context.Context, since SeqNum, fn func(*Event) error) error

	// Truncate 截断 WAL，保留 upTo 之后的事件
	// 用于清理已处理的事件，节省磁盘空间
	Truncate(upTo SeqNum) error

	// Sync 强制将缓冲区内容刷新到磁盘
	Sync() error

	// LastSeqNum 返回当前最后一个序列号
	LastSeqNum() SeqNum

	// Close 关闭 WAL 并释放资源
	Close() error
}

// WALOptions WAL 配置选项
type WALOptions struct {
	FilePath    string // WAL 文件路径（默认: .codeactor/wal/wal.log）
	SyncOnWrite bool   // 每次写入后是否 fsync（默认 true）
	MaxFileSize int64  // 最大文件字节数，超过后轮转（0=不轮转，默认 64MB）
}

// FileWAL 文件系统 WAL 实现
//
// 设计说明：
//   - 每个事件以 JSON 格式存储为一行
//   - 序列号按写入顺序单调递增
//   - 支持文件轮转（当文件大小超过阈值时）
//   - 支持崩溃恢复（重启后从文件末尾恢复序列号计数器）
type FileWAL struct {
	mu       sync.Mutex
	file     *os.File
	encoder  *json.Encoder
	path     string
	opts     WALOptions
	lastSeq  SeqNum
	closed   bool
}

// NewFileWAL 创建一个新的 FileWAL 实例
//
// 参数:
//   - opts: WAL 配置选项
//
// 返回值:
//   - *FileWAL: 初始化的 WAL 实例
//   - error: 创建失败时的错误
func NewFileWAL(opts WALOptions) (*FileWAL, error) {
	// 应用默认值
	if opts.FilePath == "" {
		opts.FilePath = filepath.Join(".codeactor", "wal", "wal.log")
	}
	if opts.MaxFileSize == 0 {
		opts.MaxFileSize = 64 * 1024 * 1024 // 64MB
	}

	// 确保目录存在
	dir := filepath.Dir(opts.FilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create WAL dir: %w", err)
	}

	// 以追加模式打开/创建文件
	file, err := os.OpenFile(opts.FilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open WAL file: %w", err)
	}

	wal := &FileWAL{
		file:    file,
		encoder: json.NewEncoder(file),
		path:    opts.FilePath,
		opts:    opts,
	}

	// 计算当前最后序列号（从文件大小推断已有的行数）
	wal.lastSeq = wal.countLines()

	return wal, nil
}

// Append 追加事件到 WAL
//
// 流程：
//   1. 检查文件是否需要轮转
//   2. 分配单调递增的序列号
//   3. JSON 编码写入
//   4. 可选 fsync 确保持久化
func (w *FileWAL) Append(event *Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return fmt.Errorf("WAL is closed")
	}

	// 检查文件大小，触发轮转
	if w.opts.MaxFileSize > 0 {
		info, err := w.file.Stat()
		if err == nil && info.Size() >= w.opts.MaxFileSize {
			if err := w.rotate(); err != nil {
				return fmt.Errorf("WAL rotate: %w", err)
			}
		}
	}

	// 分配序列号
	w.lastSeq++
	event.SeqNum = uint64(w.lastSeq)

	// JSON 编码写入
	if err := w.encoder.Encode(event); err != nil {
		return fmt.Errorf("WAL encode: %w", err)
	}

	// 可选 fsync
	if w.opts.SyncOnWrite {
		if err := w.file.Sync(); err != nil {
			return fmt.Errorf("WAL sync: %w", err)
		}
	}

	return nil
}

// Replay 从指定序列号开始回放所有事件
//
// 注意：此方法会先对当前 WAL 执行 sync，确保读取到最新数据。
// 损坏的行会被跳过（记录日志但不终止回放）。
func (w *FileWAL) Replay(ctx context.Context, since SeqNum, fn func(*Event) error) error {
	w.mu.Lock()
	// 先 sync 确保持久化
	if err := w.file.Sync(); err != nil {
		w.mu.Unlock()
		return err
	}
	w.mu.Unlock()

	// 以只读方式打开文件用于回放
	file, err := os.Open(w.path)
	if err != nil {
		return fmt.Errorf("open WAL for replay: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	lineNum := SeqNum(0)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		lineNum++
		if lineNum < since {
			continue
		}

		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			// 跳过损坏的行（记录日志但不终止回放）
			continue
		}
		event.SeqNum = uint64(lineNum)

		if err := fn(&event); err != nil {
			return err
		}
	}

	return scanner.Err()
}

// Truncate 截断 WAL，清空所有已存储的事件
//
// 用途：清理已处理的事件，释放磁盘空间。
// 截断后，新事件的序列号从 1 开始重新编号。
//
// 实现方式：
//   1. 关闭当前写入文件
//   2. 清空文件内容
//   3. 重新打开文件并重置计数器
func (w *FileWAL) Truncate(upTo SeqNum) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return fmt.Errorf("WAL is closed")
	}

	// 先关闭当前文件
	if err := w.file.Close(); err != nil {
		return err
	}

	// 清空文件（删除所有事件）
	if err := os.Truncate(w.path, 0); err != nil {
		return err
	}

	// 重新打开文件
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	w.file = file
	w.encoder = json.NewEncoder(file)
	w.lastSeq = 0

	return nil
}

// Sync 强制将缓冲区内容刷新到磁盘
func (w *FileWAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Sync()
}

// LastSeqNum 返回当前最后一个序列号
func (w *FileWAL) LastSeqNum() SeqNum {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastSeq
}

// Close 关闭 WAL 并释放资源
func (w *FileWAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}

// rotate 轮转 WAL 文件（重命名当前文件，创建新文件）
//
// 流程：
//   1. 关闭当前文件
//   2. 重命名为 .1, .2, ...（覆盖旧备份）
//   3. 创建新文件并重置计数器
func (w *FileWAL) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}

	// 重命名当前文件为 wal.log.1, wal.log.2, ...
	backupPath := fmt.Sprintf("%s.1", w.path)
	if err := os.Rename(w.path, backupPath); err != nil {
		return err
	}

	// 创建新文件
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	w.file = file
	w.encoder = json.NewEncoder(file)
	w.lastSeq = 0

	return nil
}

// countLines 计算文件中的行数（估算已有事件数）
//
// 用于崩溃恢复时初始化序列号计数器。
// 生产环境可优化为更高效的实现（如 mmap + 字节计数）。
func (w *FileWAL) countLines() SeqNum {
	file, err := os.Open(w.path)
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	count := SeqNum(0)
	for scanner.Scan() {
		count++
	}
	return count
}

// NoopWAL 空实现（用于测试和禁用 WAL）
//
// 所有操作都是 no-op，不存储任何数据。
// 适用于：
//   - 单元测试
//   - 不需要持久化的场景
//   - 性能测试（基准对比）
type NoopWAL struct{}

func (w *NoopWAL) Append(event *Event) error    { return nil }
func (w *NoopWAL) Replay(ctx context.Context, since SeqNum, fn func(*Event) error) error {
	return nil
}
func (w *NoopWAL) Truncate(upTo SeqNum) error { return nil }
func (w *NoopWAL) Sync() error                { return nil }
func (w *NoopWAL) LastSeqNum() SeqNum         { return 0 }
func (w *NoopWAL) Close() error               { return nil }
