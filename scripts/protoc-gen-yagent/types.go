package main

// ProtocolDocument 是 YAML 协议定义文件的根结构
type ProtocolDocument struct {
	Protocol   ProtocolInfo   `yaml:"protocol"`
	EventTypes []EventTypeDef `yaml:"event_types"`
	WebSocketMessage MessageDef   `yaml:"websocket_message"`
	ExtensionMessage  MessageDef   `yaml:"extension_message"`
}

type ProtocolInfo struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
}

type EventTypeDef struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	RenderHint  *RenderHint `yaml:"render_hint,omitempty" json:"render_hint,omitempty"`
	Fields      []FieldDef  `yaml:"fields"`
}

type RenderHint struct {
	Component        string `yaml:"component,omitempty" json:"component,omitempty"`
	Heading          string `yaml:"heading,omitempty" json:"heading,omitempty"`
	ShowHeader       *bool  `yaml:"show_header,omitempty" json:"show_header,omitempty"`
	Collapse         *bool  `yaml:"collapse,omitempty" json:"collapse,omitempty"`
	StreamMode       *bool  `yaml:"stream_mode,omitempty" json:"stream_mode,omitempty"`
	MergeConsecutive *bool  `yaml:"merge_consecutive,omitempty" json:"merge_consecutive,omitempty"`
	MaxPreviewLines  *int   `yaml:"max_preview_lines,omitempty" json:"max_preview_lines,omitempty"`
	Actions          []struct {
		Label    string `yaml:"label,omitempty" json:"label,omitempty"`
		ActionID string `yaml:"action_id,omitempty" json:"action_id,omitempty"`
	} `yaml:"actions,omitempty" json:"actions,omitempty"`
}

type FieldDef struct {
	Name        string      `yaml:"name"`
	Type        string      `yaml:"type"`
	Description string      `yaml:"description"`
	Required    bool        `yaml:"required"`
	Enum        []string    `yaml:"enum,omitempty"`
	Items       *FieldDef   `yaml:"items,omitempty"`
	Fields      []FieldDef  `yaml:"fields,omitempty"`
}

type MessageDef struct {
	Description string     `yaml:"description"`
	Fields      []FieldDef `yaml:"fields"`
}