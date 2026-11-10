"""manifest.json 的只读视图（Go 写、Python 读）。

manifest 是**唯一**的能力/码表/默认值来源（`internal/plasma/manifest.go`）：
工具池判据是 Go 侧的 `Effect.Has(PlasmaPalette)`，码表来自 `DiagCatalog()`。
Python 侧不许复制这两份表 —— 复制一份的后果是"生成器选了一个 Go 会拒的工具"，
也就是一次必然失败的 trampoline 往返。

这里的函数只做**读取与渲染**：不新增能力名、不猜默认值。
"""

import json


def load(path):
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict):
        raise ValueError("manifest.json 不是 JSON 对象")
    return data


def tools(manifest):
    """工具池（按 Go 侧已排好的字典序返回，这里不再排序）。"""
    items = manifest.get("tools")
    return items if isinstance(items, list) else []


def tool_names(manifest):
    names = []
    for item in tools(manifest):
        if isinstance(item, dict) and isinstance(item.get("name"), str):
            names.append(item["name"])
    return names


def defaults(manifest):
    d = manifest.get("defaults")
    return d if isinstance(d, dict) else {}


def codes(manifest):
    """code -> summary 的映射（给 explain 与自检文案用）。"""
    out = {}
    items = manifest.get("codes")
    if not isinstance(items, list):
        return out
    for item in items:
        if isinstance(item, dict) and isinstance(item.get("code"), str):
            out[item["code"]] = item.get("summary", "")
    return out


def palette(manifest):
    items = manifest.get("palette")
    return items if isinstance(items, list) else []


def tool_pool_table(manifest):
    """把工具池渲染成 markdown 表格，替换 generate_seed.md 的 `{{TOOL_POOL_TABLE}}`。

    渲染放在 Python 侧（P9a 删掉了 Go 侧的 renderCustomAgentToolPool）：表格的
    内容来自 manifest，所以两侧不可能漂。空工具池必须显式说出来 —— 一张空表
    会让模型以为"没有工具可用"，而真正的原因可能是 palette 配错了。
    """
    rows = tools(manifest)
    if not rows:
        return "（工具池为空：manifest.tools 为空，说明 Palette 或注册表有问题，不要凭空发明工具名）"
    lines = ["| 工具名 | 能力位 | 说明 |", "|---|---|---|"]
    for item in rows:
        if not isinstance(item, dict):
            continue
        name = item.get("name", "")
        effects = item.get("effects") or []
        brief = (item.get("brief") or "").replace("\n", " ")
        lines.append("| `%s` | %s | %s |" % (name, ", ".join(effects), brief))
    return "\n".join(lines)


def validate(manifest):
    """一份 manifest 至少要有的键。缺了就报出来，不要拿缺省的默认值硬跑。"""
    issues = []
    if manifest.get("schema_version") is None:
        issues.append("manifest 缺 schema_version")
    if not tool_names(manifest):
        issues.append("manifest.tools 为空（生成器无工具可选）")
    return issues
