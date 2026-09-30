package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func main() {
	var input, outputDir string
	flag.StringVar(&input, "input", "protocol/agent-events.yaml", "输入 YAML 协议定义文件")
	flag.StringVar(&outputDir, "output", "protocol", "输出目录")
	flag.Parse()

	// 读取并解析 YAML
	data, err := os.ReadFile(input)
	if err != nil {
		log.Fatalf("读取输入文件失败: %v", err)
	}

	var doc ProtocolDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		log.Fatalf("解析 YAML 失败: %v", err)
	}

	fmt.Printf("协议: %s v%s\n", doc.Protocol.Name, doc.Protocol.Version)
	fmt.Printf("事件类型数量: %d\n", len(doc.EventTypes))

	// 确保输出目录存在
	os.MkdirAll(filepath.Join(outputDir, "ts"), 0755)
	os.MkdirAll(filepath.Join(outputDir, "go"), 0755)

	// 生成所有输出
	generateTypeScript(doc, filepath.Join(outputDir, "ts", "agent-events.ts"))
	generateRenderMapping(doc, filepath.Join(outputDir, "ts", "render-mapping.ts"))
	generateJSONSchema(doc, filepath.Join(outputDir, "agent-events.schema.json"))
	generateGoTypes(doc, filepath.Join(outputDir, "go", "agent-events.go"))

	fmt.Println("✅ 所有文件生成完成！")
}