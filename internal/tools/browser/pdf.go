package browser

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// PDFTool PDF 生成工具
type PDFTool struct {
	WorkspaceDir string // 工作区目录，用于验证输出路径
}

func (t *PDFTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	// 确定输出文件路径
	outputFile := ""
	if of, ok := params["output_file"].(string); ok && of != "" {
		outputFile = of
	} else {
		// 默认路径
		timestamp := time.Now().Format("20060102_150405")
		outputFile = filepath.Join("browser", "pdfs", fmt.Sprintf("page_%s.pdf", timestamp))
	}

	// 确保输出在工作区目录内
	if t.WorkspaceDir == "" {
		return nil, fmt.Errorf("工作区目录未设置")
	}

	fullPath := filepath.Join(t.WorkspaceDir, outputFile)

	// 确保目录存在
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建输出目录失败: %w", err)
	}

	// 生成 PDF - rod 的 PDF 返回 *StreamReader，需要读取完整流
	pdfStream, err := page.PDF(&proto.PagePrintToPDF{
		PrintBackground:    true,
		PreferCSSPageSize:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("生成 PDF 失败: %w", err)
	}
	defer pdfStream.Close()

	// 读取 PDF 数据
	pdfData, err := io.ReadAll(pdfStream)
	if err != nil {
		return nil, fmt.Errorf("读取 PDF 数据失败: %w", err)
	}

	// 保存文件
	if err := os.WriteFile(fullPath, pdfData, 0644); err != nil {
		return nil, fmt.Errorf("保存 PDF 失败: %w", err)
	}

	return map[string]interface{}{
		"status":      "success",
		"output_file": outputFile,
		"size_bytes":  len(pdfData),
	}, nil
}
