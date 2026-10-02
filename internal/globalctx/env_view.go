package globalctx

// EnvView 以方法视图暴露 GlobalCtx 中运行时可变的环境字段。
// 保持动态读取语义：调用方每次调用 ProjectPath()/FullYoloMode() 都会
// 重新读取 GlobalCtx 当前值（而非构造期快照），与原先直接访问字段一致。
type EnvView struct {
	g *GlobalCtx
}

// NewEnvView 基于给定 GlobalCtx 创建 EnvView。
func NewEnvView(g *GlobalCtx) *EnvView {
	return &EnvView{g: g}
}

// ProjectPath 返回当前项目路径（动态读取）。
func (v *EnvView) ProjectPath() string {
	return v.g.ProjectPath
}

// FullYoloMode 返回当前是否处于 FULL-YOLO 自主模式（动态读取）。
func (v *EnvView) FullYoloMode() bool {
	return v.g.FullYoloMode
}
