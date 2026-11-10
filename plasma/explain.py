"""离线解释（`explain`）：把 Go 的诊断报告渲染成人能照着改的文本。

输入是 Go 写的 `diagnostics.json`（`plasma.Report`）或 `yagent plasma compile --format json`
的结果（`{"diagnostics":[...]}`），也接受裸数组。输出：

  - `--out PATH`：写纯文本（给 TUI/日志/人看）；
  - stdout：一份 JSON 信封（`{"errors":N,"warnings":M,"out":"..."}`），
    这是 §7.3 的"stdout 仅一份 JSON"。

**不复制码表**：码表唯一来源是 Go 的 `DiagCatalog()`。这里只按**码族前缀**
（0xx parse / 1xx resolve / 2xx 权限 / 3xx lint）给一句方向，族是 handful 个，
不是码表；把 13 个码抄一份进来就会与 manifest 漂移。
"""

import json
import os
import sys

import contract

FAMILY_HINTS = [
    ("0", "解析/形态问题：先修 JSON 结构与必填字段，再谈工具与权限。"),
    ("1", "工具名解析问题：只能从 manifest 的工具池里选，名字逐字一致。"),
    ("2", "类型/权限问题：palette 之外的能力会被拒；越权只影响 grade，不阻断声明。"),
    ("3", "一致性/lint 警告：不阻断，但会进 quality.lint 影响适应度。"),
    ("5", "验证问题：结构夹具没过。看 reason 里点名的那条断言。"),
    ("6", "落盘/emit 问题：store 拒收（不可变、谱系、hash）。"),
    ("7", "加载问题：活注册表复核失败（工具被改名/删除）。"),
]


def run(in_path, out_path=""):
    if not in_path:
        return _usage("缺 --in")
    try:
        with open(in_path, "r", encoding="utf-8") as fh:
            report = json.load(fh)
    except OSError as exc:
        return _fail("读诊断失败（%s）：%s" % (in_path, exc))
    except ValueError as exc:
        return _fail("%s 不是合法 JSON：%s" % (in_path, exc))

    items = _items(report)
    text = render(report, items)

    if out_path:
        _write_text(out_path, text)

    envelope = {
        "errors": sum(1 for d in items if (d.get("severity") or "") == "error"),
        "warnings": sum(1 for d in items if (d.get("severity") or "") == "warning"),
        "diagnostics": len(items),
        "out": out_path,
    }
    json.dump(envelope, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return contract.EXIT_OK


def render(report, items=None):
    """渲染纯文本。空诊断也要有一句可读的话（否则 --out 会写出 0 字节文件）。"""
    if items is None:
        items = _items(report)
    if not items:
        grade = report.get("grade") if isinstance(report, dict) else ""
        if grade:
            return "没有诊断条目（grade=%s）。\n" % grade
        return "没有诊断条目。\n"

    lines = []
    seen_families = []
    for d in items:
        code = d.get("code") or ""
        severity = d.get("severity") or ""
        path = d.get("path") or "bundle"
        message = d.get("message") or ""
        lines.append("%s: %s[%s]: %s" % (path, severity, code, message))
        note = d.get("note") or ""
        if note:
            lines.append("    note: %s" % note)
        family = code[4:5] if code.startswith("PLS-") else code[:1]
        if family and family not in seen_families:
            seen_families.append(family)

    for family in seen_families:
        hint = _hint(family)
        if hint:
            lines.append("")
            lines.append("修复方向（码族 %sxx）：%s" % (family, hint))

    errors = sum(1 for d in items if (d.get("severity") or "") == "error")
    warnings = sum(1 for d in items if (d.get("severity") or "") == "warning")
    lines.append("")
    lines.append("合计：%d error / %d warning。" % (errors, warnings))
    return "\n".join(lines) + "\n"


def _hint(family):
    for prefix, hint in FAMILY_HINTS:
        if family == prefix:
            return hint
    return ""


def _items(report):
    if isinstance(report, list):
        return [d for d in report if isinstance(d, dict)]
    if not isinstance(report, dict):
        return []
    items = report.get("diagnostics")
    return [d for d in items if isinstance(d, dict)] if isinstance(items, list) else []


def _write_text(path, text):
    parent = os.path.dirname(os.path.abspath(path))
    if parent:
        os.makedirs(parent, exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


def _usage(msg):
    print("plasma explain: " + msg, file=sys.stderr)
    return contract.EXIT_USAGE


def _fail(msg):
    print("plasma explain: " + msg, file=sys.stderr)
    return contract.EXIT_FAILED
