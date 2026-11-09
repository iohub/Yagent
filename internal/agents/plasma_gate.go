package agents

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"yagent/internal/config"
	"yagent/internal/embedbin"
	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

// 本文件是"生成器此刻能不能用"的**唯一**判据（ADR-15）与它的端口适配。
//
// 为什么判据在 agents 而不在 toolspec：它要读 [plasma] 配置、找解释器、stat
// zipapp —— toolspec 是叶子包，这些都不在它的依赖白名单里。所以 toolspec 只声明
// PlasmaResolver 窄接口，真正的判定与**人话原因**留在这一侧。
//
// 三条纪律：
//   - 不可用是**正常状态**（P10 之前 plasma.pyz 天然不存在），但必须可见：
//     原因进 Surface.Skipped，delegate_meta 直接不出现在工具面上（ADR-15）；
//   - 判据本身可被单测钉死（plasmaAvailableWith 把配置与路径变成参数），
//     否则"降级"这条路径只能靠开发机的文件系统碰运气；
//   - 缺什么说什么 + 怎么修：一句 "unavailable" 等于没判。

// plasmaPyzName 是生成器 zipapp 的落盘名。P10 由 embedbin 解到 ~/.yagent/bin
// （与 rg/fzf 同一个目录，部署时不用多记一个路径）。
const plasmaPyzName = "plasma.pyz"

// plasmaAvailable 是零参形态：读真实配置、真实 PATH、真实文件系统。
func plasmaAvailable() (bool, string) {
	return plasmaAvailableWith(config.GlobalPlasma(), plasmaPyzPath)
}

// plasmaAvailableWith 把两个外部变量（配置、zipapp 路径）变成参数，使判据可测。
// 顺序即诊断顺序：先说"被关掉了"，再说"没有解释器"，最后说"没有包"。
func plasmaAvailableWith(cfg config.PlasmaConfig, pyzPath func() (string, error)) (bool, string) {
	if !cfg.Enable {
		return false, "plasma 生成器已关闭（[plasma] enable=false 或 YAGENT_PLASMA=0）；" +
			"修复：打开 [plasma] enable 并重启"
	}
	python, why := plasmaInterpreter(cfg.Python)
	if python == "" {
		return false, why
	}
	path, err := pyzPath()
	if err != nil {
		return false, "plasma 生成器未装配：定位不到 " + plasmaPyzName + "（" + err.Error() + "）；" +
			"修复：检查 $HOME 可读，或等 Python 包落地"
	}
	if strings.TrimSpace(path) == "" {
		return false, "plasma 生成器未装配：定位不到 " + plasmaPyzName + "（路径为空）；" +
			"修复：检查 $HOME 可读，或等 Python 包落地"
	}
	if info, statErr := os.Stat(path); statErr != nil || info.IsDir() {
		return false, "plasma 生成器未装配：未找到 " + path + "（P10 前属预期）；" +
			"修复：等 Python 包落地（plasma.pyz），或把 zipapp 放到该路径（解释器 " + python + " 已就绪）"
	}
	return true, ""
}

// PlasmaAvailableWith 是判据的**可注入形态**：配置与 zipapp 路径都由调用方给。
// 装配层用它把"同一个路径"同时交给判定与 spawner（Pkg），测试用它避开真实 HOME。
func PlasmaAvailableWith(cfg config.PlasmaConfig, pyz string) (bool, string) {
	return plasmaAvailableWith(cfg, func() (string, error) { return pyz, nil })
}

// PlasmaZipappPath 是 zipapp 落盘位置的唯一来源：判定（PlasmaAvailableWith）与
// spawner（ExecSpawner.Pkg）必须读同一个值 —— 读两处的症状是"判定通过但每次 exit 1"。
func PlasmaZipappPath() (string, error) { return plasmaPyzPath() }

// plasmaInterpreter 解析解释器：显式路径优先，否则 PATH 上的 python3。
// 显式路径写错必须报错而不是回落 PATH —— 用户写它就是为了钉版本/虚拟环境。
func plasmaInterpreter(configured string) (string, string) {
	if p := strings.TrimSpace(configured); p != "" {
		if info, err := os.Stat(p); err != nil || info.IsDir() {
			return "", "plasma 解释器不可用：[plasma] python = " + p + " 不是可执行文件；" +
				"修复：改成真实的 python3 路径，或留空走 PATH"
		}
		return p, ""
	}
	if p, err := exec.LookPath("python3"); err == nil {
		return p, ""
	}
	return "", "plasma 解释器不可用：PATH 上没有 python3；修复：安装 python3（≥3.9）或设置 [plasma] python"
}

// plasmaPyzPath 是 zipapp 的落盘位置的唯一来源（embedbin 的 bin 目录）。
func plasmaPyzPath() (string, error) { return embedbin.BinPath(plasmaPyzName) }

// plasmaGate 把"生成器有没有装配"翻译成工具可用性。
//
// Available 的判据是**装配事实**（gen != nil），不是"本机能不能生成"：装配层已经
// 用 plasmaAvailable() 判过一遍，并在这里没装配时把原因带进来。两处都读配置的后果
// 是"装配成功但工具被藏起来"或反之，而这两种都只在运行期才暴露。
type plasmaGate struct {
	gen    plasmaGenerator
	reason string
}

func (g plasmaGate) Available() (bool, string) {
	if g.gen != nil {
		return true, ""
	}
	if r := strings.TrimSpace(g.reason); r != "" {
		return false, r
	}
	// 装配层既没给生成器也没给原因：现场按真实判据算一句，绝不说"未知"。
	if ok, reason := plasmaAvailable(); ok {
		return false, "plasma 生成器可用但未装配（装配层漏接 PlasmaGenerator）；" +
			"修复：在 internal/app 把 NewPlasmaTrampoline 传进 DirectorAgentParams"
	} else {
		return false, reason
	}
}

// plasmaAvailableFor 是 delegate_meta 的可用性判据（specs.go 里 delegateAvailable 调它）。
// nil 端口 = 未装配 = 不可用：缺依赖的默认必须是降级，而不是"没注入就当有"。
//
// 这里**不**回落到 plasmaAvailable() 的现场判定：端口没注入时去读本机文件系统，等于让
// "这条工具在不在场上"随部署状态漂移，而调用方连个原因都拿不到（它本来就没接端口）。
// 判据只有一份（plasmaGate.Available），这里只负责把"没接"翻成一句可执行的话。
func plasmaAvailableFor(d *toolspec.Deps) (bool, string) {
	if d != nil && d.Plasma != nil {
		return d.Plasma.Available()
	}
	return false, "plasma 生成器未装配：工具面没有注入 PlasmaResolver；" +
		"修复：装配层给 toolspec.Deps.Plasma 传 plasmaGate{gen, reason}"
}

// plasmaFixHint 是"生成失败后该做什么"的统一尾句：Python 侧 --fix 与人类看的是
// 同一份诊断，所以错误里要指明下一步动作，而不是只描述格式。
const plasmaFixHint = "按上面的诊断（PATH 指向的字段）改 bundle 后重跑 delegate_meta；" +
	"若诊断指向 prompt 与工具不一致，先改 prompt 再改 tools"

// plasmaReportHead 取诊断报告的前 n 行（含 path/severity/code/message），给错误文案用。
// 全量 report 会随 tool 结果进上下文，一条错误不该把整个窗口吃掉；而"只报第一条"
// 又会让生成侧一次只修一个问题。
func plasmaReportHead(r *plasma.Report, n int) string {
	if r == nil {
		return ""
	}
	text := strings.TrimRight(r.Text(), "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if n > 0 && len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("…（共 %d 行诊断，已截断）", len(lines)))
	}
	return strings.Join(lines, "\n") + "\n"
}
