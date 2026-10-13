package toolspec

import "context"

// 本文件是 Profile.testDeps 用的探针实现：只为让 Available 的 nil 检查通过，
// 一律返回零值。它们同时充当窄接口的编译期契约测试——接口方法一改，这里就编不过。

type probeFileOps struct{}

func (probeFileOps) ExecuteReadFile(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeFileOps) ExecuteListDir(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeFileOps) ExecutePrintDirTree(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeFileOps) ExecuteCreateFile(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeFileOps) ExecuteDeleteFile(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeFileOps) ExecuteRenameFile(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

type probeSearchOps struct{}

func (probeSearchOps) ExecuteGrepSearch(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

type probeSysOps struct{}

func (probeSysOps) ExecuteRunBash(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

type probeEditOps struct{}

func (probeEditOps) ExecuteReplaceBlock(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

type probeRepoOps struct{}

func (probeRepoOps) ExecuteSemanticSearch(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeRepoOps) ExecuteQueryCodeSkeleton(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeRepoOps) ExecuteQueryCodeSnippet(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeRepoOps) ExecuteFindFunctionCallees(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeRepoOps) ExecuteFindFunctionCallers(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeRepoOps) ExecuteCallGraph(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

type probeFlowOps struct{}

func (probeFlowOps) ExecuteAgentExit(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (probeFlowOps) ExecuteAskUserForHelp(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

type probeThinker struct{}

func (probeThinker) Call(context.Context, string) (string, error) { return "", nil }

type probeMicro struct{}

func (probeMicro) Execute(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

type probeDeep struct{}

func (probeDeep) Execute(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

type probeDesign struct{}

func (probeDesign) Execute(context.Context, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

// probeBrowser 报告能力可用，并让任意 browser_* 名字都能解析到执行器。
type probeBrowser struct{}

func (probeBrowser) ToolFunc(string) (ToolFunc, bool) { return probeToolFunc, true }
func (probeBrowser) Available() (bool, string)        { return true, "" }

var (
	_ FileToolSet      = probeFileOps{}
	_ SearchToolSet    = probeSearchOps{}
	_ SysToolSet       = probeSysOps{}
	_ EditToolSet      = probeEditOps{}
	_ RepoOps          = probeRepoOps{}
	_ FlowToolSet      = probeFlowOps{}
	_ Thinker          = probeThinker{}
	_ MicroAgentRunner = probeMicro{}
	_ DeepThinker      = probeDeep{}
	_ Designer         = probeDesign{}
	_ BrowserResolver  = probeBrowser{}
)
