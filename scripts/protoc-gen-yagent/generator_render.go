package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func generateRenderMapping(doc ProtocolDocument, outputPath string) {
	type RenderEntry struct {
		Event       string      `json:"event"`
		Component   string      `json:"component"`
		Heading     string      `json:"heading,omitempty"`
		ShowHeader  bool        `json:"show_header,omitempty"`
		Collapse    bool        `json:"collapse,omitempty"`
		StreamMode  bool        `json:"stream_mode,omitempty"`
		Merge       bool        `json:"merge_consecutive,omitempty"`
		MaxLines    int         `json:"max_preview_lines,omitempty"`
	}

	type RenderMapping struct {
		Version string        `json:"version"`
		Events  []RenderEntry `json:"events"`
	}

	var entries []RenderEntry
	for _, et := range doc.EventTypes {
		entry := RenderEntry{
			Event: et.Name,
		}
		if et.RenderHint != nil {
			entry.Component = et.RenderHint.Component
			entry.Heading = et.RenderHint.Heading
			if et.RenderHint.ShowHeader != nil {
				entry.ShowHeader = *et.RenderHint.ShowHeader
			}
			if et.RenderHint.Collapse != nil {
				entry.Collapse = *et.RenderHint.Collapse
			}
			if et.RenderHint.StreamMode != nil {
				entry.StreamMode = *et.RenderHint.StreamMode
			}
			if et.RenderHint.MergeConsecutive != nil {
				entry.Merge = *et.RenderHint.MergeConsecutive
			}
			if et.RenderHint.MaxPreviewLines != nil {
				entry.MaxLines = *et.RenderHint.MaxPreviewLines
			}
		}
		entries = append(entries, entry)
	}

	mapping := RenderMapping{
		Version: doc.Protocol.Version,
		Events:  entries,
	}

	data, err := json.MarshalIndent(mapping, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "序列化渲染映射失败: %v\n", err)
		return
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "写入渲染映射文件失败: %v\n", err)
		return
	}
	fmt.Printf("  ✓ 渲染映射已生成: %s\n", outputPath)
}