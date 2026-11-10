"""离线出题（`author-fixtures`）：把一份 bundle 变成行为夹具草稿。

首版是**草稿生成器**（P11 才接真 LLM 出题）：它只做能机械推导的部分 ——
输入取 `task_for_agent`、脚本取 bundle 声明的前几个工具、期望取 `must_call` 与
exit 形状。每条夹具带 `provenance`，`store` 会用它做"出题谱系必须独立于变异谱系"
的判据（`checkFixtureProvenance`）；这里写的是 `author-fixtures`，与自动生成的
`plasma-compiler-auto` 区分开。

产物形状必须与 Go 的 `plasma.Fixture`（`internal/plasma/ir.go:122-145`）逐字段一致，
否则 Go 侧读不进来 —— 这条契约由 `internal/plasma/contract_py_test.go` 的样例对账兜住。

stdout 只发一份 JSON 账目（§7.3）；日志与诊断走 stderr。
"""

import json
import os
import sys

import contract


def run(bundle_path, out_path=""):
    if not bundle_path:
        return _usage("缺 --bundle")
    try:
        with open(bundle_path, "r", encoding="utf-8") as fh:
            bundle = json.load(fh)
    except OSError as exc:
        return _fail("读 bundle 失败（%s）：%s" % (bundle_path, exc))
    except ValueError as exc:
        return _fail("%s 不是合法 JSON：%s" % (bundle_path, exc))
    if not isinstance(bundle, dict):
        return _fail("bundle 顶层不是 JSON 对象")

    spec = _normalize(bundle)
    if not spec["declared"]:
        # 出题必须建立在"bundle 自己声明了工具"之上：空工具池时补出来的
        # agent_exit 只会产出一条只调收尾的夹具，它永远通过，等于没有考题。
        return _fail("bundle 没有声明任何工具（tools_used / tools 均为空），无法出题")

    fixture = _draft_fixture(spec)
    payload = {
        "draft": True,
        "note": "首版为机械推导的草稿：脚本只演示调用顺序，期望值请在 P11 的 LLM 出题中细化。",
        "provenance": {"lineage": "author-fixtures", "author": "plasma-author-fixtures"},
        "fixtures": [fixture],
    }

    if out_path:
        target = out_path
        if os.path.isdir(target) or target.endswith(os.sep):
            target = os.path.join(target, "behavior.draft.json")
        _write_json(target, payload)
        payload["out"] = target

    # stdout 只允许一份 JSON（§7.3）。它同时是"给调用方看的账目"与"可重定向的产物"。
    json.dump(payload, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return contract.EXIT_OK


def _normalize(bundle):
    """兼容最小 spec 与完整 IR 两种输入形态（与 Go 的 Elaborate 同一判据）。"""
    if "agent_name" in bundle:
        tools = bundle.get("tools_used")
        task = bundle.get("task_for_agent") or ""
        name = bundle.get("agent_name") or ""
    else:
        tools = bundle.get("tools")
        task = bundle.get("task_for_agent") or (bundle.get("prompt") or {}).get("system") or ""
        name = bundle.get("name") or ""
    if not isinstance(tools, list):
        tools = []
    tools = [t for t in tools if isinstance(t, str)]
    declared = list(tools)
    if "agent_exit" not in tools:
        tools = tools + ["agent_exit"]
    return {"name": name, "task": task, "tools": tools, "declared": declared}


def _draft_fixture(spec):
    """一条可执行的最小行为夹具。

    脚本形状必须是**一条响应里同时带正文与 agent_exit**（硬约束 23）：
    `scripted` 引擎按 ScriptStep 顺序回放，最后一步 `agent_exit` 带 `reason`。
    """
    first = spec["tools"][0]
    script = [
        {"tool": first, "args": {}},
        {"tool": "agent_exit", "text": "fixture: 完成（草稿）"},
    ]
    expect = {
        "must_call": [first],
        "exit_shape": ["reason"],
        "stop_kind": "agent_exit",
    }
    return {
        "id": "%s-draft-1" % (_id_slug(spec["name"]) or "custom-agent"),
        "input": spec["task"] or "按任务描述完成工作，并在结束时调用 agent_exit。",
        "script": script,
        "expect": expect,
        "rubric": [
            "是否按任务要求产出了可核对的交付内容",
            "是否在结束时调用了 agent_exit 并给出 reason",
        ],
    }


def _id_slug(name):
    out = []
    for ch in (name or "").strip().lower():
        if ch.isalnum():
            out.append(ch)
        elif out and out[-1] != "-":
            out.append("-")
    return "".join(out).strip("-")


def _write_json(path, payload):
    parent = os.path.dirname(os.path.abspath(path))
    if parent:
        os.makedirs(parent, exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, ensure_ascii=False, indent=2)
        fh.write("\n")
    os.replace(tmp, path)


def _usage(msg):
    print("plasma author-fixtures: " + msg, file=sys.stderr)
    return contract.EXIT_USAGE


def _fail(msg):
    print("plasma author-fixtures: " + msg, file=sys.stderr)
    return contract.EXIT_FAILED
