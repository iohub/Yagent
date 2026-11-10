"""在线生成策略（zipapp 的 `generate` 子命令，ADR-14 的 Python 侧）。

契约（§6.3 / §7.3）：
    generate [--resume] [--fix] --workdir W

    - 需要 LLM 时写 `W/llm_req_NNN.json` + `W/state.json`，**exit 4**；
    - 被 Go respawn 后（--resume）读 `W/llm_resp_NNN.json` 继续；
    - 产出 `W/bundle.json`（最小 spec：thinking/agent_name/agent_design/
      tools_used/task_for_agent）后 **exit 0**；
    - 用法错 exit 2，业务失败 exit 1；日志**一律 stderr**，stdout 不发任何东西。

**每次进程运行只推进一个状态步**：draft 请求 → 响应自检 → （不合格则）refine 请求。
把多轮塞进一次进程会让内存里的中间结果与文件序号不同步，而 trampoline 的价值
正是"每一步都可从 state.json 重放"（ADR-14）。

首版只产最小 spec（OD-2）：完整 IR 留给进化路径，扩充默认值由 Go 的 Elaborate 做。
"""

import json
import os
import sys
import zipfile

import contract
import manifest as manifest_mod
import sampling
import state

# 一条生成链最多几轮"响应 → 自检"。超过就 exit 1：Go 侧的 max_llm_calls 是
# 采样次数上限，这里是**策略自身**的收敛判据，两者都不能少（否则 Python 可以
# 一直换措辞而不收敛，把预算烧在同一个问题上）。
MAX_ROUNDS = 3

SEED_MEMBER = "prompts/generate_seed.md"
SEED_PLACEHOLDER = "{{TOOL_POOL_TABLE}}"
REQUIRED_FIELDS = ("agent_name", "agent_design", "tools_used", "task_for_agent")


def run(workdir, resume=False, fix=False):
    """执行一个状态步，返回退出码。"""
    req = _read_json(os.path.join(workdir, contract.FILE_REQ))
    if req is None:
        return _fail("workdir 里没有可读的 %s（Go 侧应在 spawn 前写好它）" % contract.FILE_REQ)

    man_path = os.path.join(workdir, contract.FILE_MANIFEST)
    if not os.path.isfile(man_path):
        return _fail("workdir 里没有 %s（能力清单是只读契约，缺了不许猜工具）" % contract.FILE_MANIFEST)
    man = manifest_mod.load(man_path)
    issues = manifest_mod.validate(man)
    if issues:
        return _fail("manifest 不可用：" + "；".join(issues))

    st = state.load(workdir)
    diag = _read_json(os.path.join(workdir, contract.FILE_DIAG))

    # --fix：Go 编译失败后带着 diagnostics.json 回来。诊断原文进对话（不是只说
    # "JSON 无效"）：码 + JSON pointer + note 是模型唯一能据以定位的证据。
    if fix:
        if diag is None:
            return _fail("收到 --fix 但没有可读的 %s（Go 侧只在编译失败时传这个标志）" % contract.FILE_DIAG)
        if st is None:
            st = _new_state(req, man)
        st["phase"] = "waiting"
        st["seq"] = int(st.get("seq", 0)) + 1
        st["issues"] = _diag_issues(diag)
        st["messages"].append({"role": "assistant", "content": json.dumps(st.get("spec") or {}, ensure_ascii=False)})
        st["messages"].append({"role": "user", "content": _fix_message(diag, man)})
        return _emit_request(workdir, st)

    if st is None:
        st = _new_state(req, man)
        return _emit_request(workdir, st)

    phase = st.get("phase")
    if phase == "waiting":
        return _advance(workdir, st, man)
    if phase == "done":
        # Go 只会在编译通过时 exit 0；再次进来而没有 --fix 说明 bundle 已经是
        # 上一次的产物 —— 原样回报成功，不要重新花一次采样。
        sampling.log("state 已是 done 且没有 --fix：沿用已有 %s" % contract.FILE_BUNDLE)
        return contract.EXIT_OK
    if phase == "start":
        return _emit_request(workdir, st)

    # 未知 phase（手改/半旧状态）：清掉重新起草。硬跑一个认不出的状态机，
    # 症状是"模型看起来很笨"，实际是状态错位。
    sampling.log("state.phase=%r 不可识别：清状态重新 draft" % phase)
    state.clear(workdir)
    st = _new_state(req, man)
    return _emit_request(workdir, st)


def _advance(workdir, st, man):
    """phase=waiting：读响应 → 自检 → 出包 or 下一轮请求。"""
    seq = int(st.get("seq", 0))
    try:
        resp = sampling.read_response(workdir, seq)
    except FileNotFoundError:
        # Go 收到 exit 4 后必须写响应再 respawn。没写就回来 = 契约被破坏，
        # 重发同一个请求会安静地烧掉 max_llm_calls，所以直接失败并点名。
        return _fail("Go 未写回 %s（exit 4 之后必须由 serveSample 写响应）" % contract.llm_resp_name(seq))
    except ValueError as exc:
        return _fail("采样响应不可解析：%s" % exc)

    text = sampling.content_of(resp)
    st["messages"].append({"role": "assistant", "content": text})
    st["rounds"] = int(st.get("rounds", 0)) + 1

    spec, found = _parse_spec(text)
    if spec is None:
        st["issues"] = found
        st["phase"] = "waiting"
        if int(st["rounds"]) >= MAX_ROUNDS:
            state.clear(workdir)
            return _fail("%d 轮仍未产出可解析的最小 spec：%s" % (MAX_ROUNDS, "；".join(found)))
        st["seq"] = seq + 1
        st["messages"].append({"role": "user", "content": _refine_message(found, man)})
        return _emit_request(workdir, st)

    problems = _critique(spec, man)
    if problems:
        st["issues"] = problems
        st["phase"] = "waiting"
        if int(st["rounds"]) >= MAX_ROUNDS:
            state.clear(workdir)
            return _fail("%d 轮仍未产出可用的 spec：%s" % (MAX_ROUNDS, "；".join(problems)))
        st["seq"] = seq + 1
        st["messages"].append({"role": "user", "content": _refine_message(problems, man)})
        return _emit_request(workdir, st)

    st["spec"] = spec
    st["phase"] = "done"
    st["issues"] = []
    _write_bundle(workdir, spec)
    state.save(workdir, st)
    sampling.log("bundle 已写出（%s，%d 个工具）" % (contract.FILE_BUNDLE, len(spec["tools_used"])))
    return contract.EXIT_OK


def _emit_request(workdir, st):
    """写请求 + 落 state + exit 4。顺序先请求后状态：Go 只在收到 exit 4 后读请求。"""
    seq = int(st.get("seq", 0))
    st["phase"] = "waiting"
    sampling.write_request(workdir, seq, st["messages"], tools=None)
    state.save(workdir, st)
    sampling.log("需要采样：%s（第 %d 轮，seq=%03d）" % (contract.llm_req_name(seq), int(st.get("rounds", 0)) + 1, seq))
    return contract.EXIT_SAMPLING


def _new_state(req, man):
    """首次 draft 的对话：system = 策略种子 + 工具池表格，user = 任务与上下文。"""
    system = load_seed().replace(SEED_PLACEHOLDER, manifest_mod.tool_pool_table(man))
    task = req.get("task") or ""
    context = req.get("context") or ""
    user = "## 任务\n%s\n" % task
    if context:
        user += "\n## 上下文\n%s\n" % context
    user += (
        "\n## 输出契约\n"
        "只输出一个 JSON 对象（不要 markdown 围栏、不要解释文字），键为：\n"
        "thinking / agent_name / agent_design / tools_used / task_for_agent。\n"
        "tools_used 必须全部来自上面的工具池，且必须包含 agent_exit。\n"
    )
    return state.fresh([
        {"role": "system", "content": system},
        {"role": "user", "content": user},
    ])


def _refine_message(problems, man):
    lines = ["上一份输出没有通过自检，逐条问题如下："]
    for i, p in enumerate(problems, 1):
        lines.append("%d. %s" % (i, p))
    lines.append("")
    lines.append("可用的工具池（只能从这里选，名字逐字一致）：")
    lines.append(", ".join(manifest_mod.tool_names(man)))
    lines.append("")
    lines.append("请重写整份设计并只输出一个 JSON 对象（不要 markdown 围栏、不要解释）。")
    return "\n".join(lines)


def _fix_message(diag, man):
    """把 Go 的编译诊断原文喂进 refine 提示（码 + pointer + note）。"""
    items = _diag_items(diag)
    lines = ["Go 编译器拒绝了上一份 bundle，诊断如下（原文，逐条修）："]
    if not items:
        lines.append("（diagnostics.json 里没有可识别的诊断条目）")
    for i, d in enumerate(items, 1):
        code = d.get("code", "")
        severity = d.get("severity", "")
        path = d.get("path", "")
        message = d.get("message", "")
        note = d.get("note", "")
        lines.append("%d. %s[%s] %s: %s" % (i, severity, code, path, message))
        if note:
            lines.append("   note: %s" % note)
    lines.append("")
    lines.append("请输出修订后的完整 JSON 对象（键：thinking/agent_name/agent_design/tools_used/task_for_agent），"
                 "只输出 JSON，不要 markdown 围栏。")
    lines.append("可用的工具池：" + ", ".join(manifest_mod.tool_names(man)))
    return "\n".join(lines)


def _critique(spec, man):
    """本地自检：字段非空 + 工具名都在 manifest 里 + 必须含 agent_exit。

    这里只查"Go 一定会拒"的东西（工具名/必填字段），不查风格。查得越少越不会
    与编译器漂移：真正的裁决在 Go 的 Compile。
    """
    problems = []
    for field in REQUIRED_FIELDS:
        value = spec.get(field)
        if isinstance(value, str) and not value.strip():
            problems.append("%s 不能为空字符串" % field)
        elif value is None:
            problems.append("缺 %s" % field)

    tools = spec.get("tools_used")
    if not isinstance(tools, list) or not tools:
        problems.append("tools_used 必须是非空数组")
        tools = []
    known = manifest_mod.tool_names(man)
    known_set = set(known)
    for name in tools:
        if not isinstance(name, str):
            problems.append("tools_used 里有非字符串项：%r" % (name,))
        elif name not in known_set:
            hint = _suggest(name, known)
            problems.append("工具 %r 不在 manifest 工具池里%s" % (name, hint))
    if tools and "agent_exit" not in tools:
        problems.append("tools_used 必须包含 agent_exit（否则 Agent 无法收尾）")
    if not isinstance(spec.get("agent_design"), str) or len(spec.get("agent_design") or "") < 40:
        problems.append("agent_design 太短：它会被直接当作 system prompt，必须是一份完整提示")
    return problems


def _suggest(name, known):
    """did-you-mean：只提示真实存在的工具名（不凭空发明）。"""
    import difflib

    close = difflib.get_close_matches(name, known, n=1, cutoff=0.5)
    return "（是不是 %r？）" % close[0] if close else ""


def _parse_spec(text):
    """从模型文本里取最小 spec。返回 (spec, problems)。

    取不到合法 JSON 时 problems 至少有一条可执行的话（不是"格式错误"）。
    """
    if not text.strip():
        return None, ["响应没有 content（模型只给了空串或只给了 reasoning）"]
    obj = extract_json_object(text)
    if obj is None:
        return None, ["响应里没有可解析的 JSON 对象：只输出一个 {...}，不要 markdown 围栏或前后解释文字"]
    if not isinstance(obj, dict):
        return None, ["响应顶层不是 JSON 对象（期望 {thinking, agent_name, ...}）"]
    if "agent_name" not in obj and "name" in obj:
        return None, ["响应看起来是完整 IR（有 name 键）：本版本首版只接受最小 spec，请改成 agent_name/agent_design/tools_used/task_for_agent"]
    spec = {
        "thinking": obj.get("thinking") or "",
        "agent_name": obj.get("agent_name"),
        "agent_design": obj.get("agent_design"),
        "tools_used": obj.get("tools_used"),
        "task_for_agent": obj.get("task_for_agent"),
    }
    return spec, []


def extract_json_object(text):
    """从自由文本里取第一个**字符串感知**的平衡 `{...}`。

    为什么不用 json.loads(text) 一把梭：模型偶尔会加一句前言或 markdown 围栏，
    直接失败会把一次本可用的输出丢掉。但也不能用正则做贪婪匹配 —— 字符串里的
    `}` 会让截断点落在 JSON 中间（那种 bug 只在提示里含花括号时出现）。
    """
    stripped = text.strip()
    if stripped.startswith("```"):
        lines = stripped.splitlines()
        if lines and lines[0].startswith("```"):
            lines = lines[1:]
        if lines and lines[-1].strip().startswith("```"):
            lines = lines[:-1]
        stripped = "\n".join(lines).strip()

    start = stripped.find("{")
    while start != -1:
        end = _balanced_end(stripped, start)
        if end != -1:
            try:
                return json.loads(stripped[start:end + 1])
            except ValueError:
                pass
        start = stripped.find("{", start + 1)
    return None


def _balanced_end(text, start):
    """返回与 text[start]（'{'）配对的 '}' 下标；不平衡返回 -1。字符串感知。"""
    depth = 0
    in_string = False
    escaped = False
    for i in range(start, len(text)):
        ch = text[i]
        if in_string:
            if escaped:
                escaped = False
            elif ch == "\\":
                escaped = True
            elif ch == '"':
                in_string = False
            continue
        if ch == '"':
            in_string = True
        elif ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                return i
    return -1


def _diag_items(diag):
    """兼容两种诊断载体：Report（{diagnostics:[...]}）与裸数组。"""
    if isinstance(diag, list):
        return [d for d in diag if isinstance(d, dict)]
    items = diag.get("diagnostics")
    if isinstance(items, list):
        return [d for d in items if isinstance(d, dict)]
    return []


def _diag_issues(diag):
    issues = []
    for d in _diag_items(diag):
        issues.append("%s[%s] %s: %s" % (d.get("severity", ""), d.get("code", ""), d.get("path", ""), d.get("message", "")))
    return issues or ["diagnostics.json 里没有可识别的诊断条目"]


def _write_bundle(workdir, spec):
    p = os.path.join(workdir, contract.FILE_BUNDLE)
    tmp = p + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(spec, fh, ensure_ascii=False, indent=2)
        fh.write("\n")
    os.replace(tmp, p)


def load_seed():
    """读策略种子。zipapp 内用 zipimporter.archive，源码树内用文件系统。

    两处都要支持：`scripts/check_plasma_pyz_sync.sh` 与 unittest 都从源码树跑，
    而生产只跑 pyz —— 只实现一种的后果是"测试绿、生产读不到种子"。
    """
    here = os.path.dirname(os.path.abspath(__file__))
    fs_path = os.path.join(here, SEED_MEMBER)
    if os.path.isfile(fs_path):
        with open(fs_path, "r", encoding="utf-8") as fh:
            return fh.read()

    archive = _archive_path()
    if archive:
        try:
            with zipfile.ZipFile(archive) as zf:
                return zf.read(SEED_MEMBER).decode("utf-8")
        except (KeyError, OSError, zipfile.BadZipFile) as exc:
            raise RuntimeError("zipapp 内读不到 %s：%s" % (SEED_MEMBER, exc))
    raise RuntimeError("找不到策略种子 %s（既不在源码目录，也不在 zipapp 里）" % SEED_MEMBER)


def _archive_path():
    loader = globals().get("__loader__")
    archive = getattr(loader, "archive", None)
    if archive and os.path.isfile(archive):
        return archive
    argv0 = os.path.abspath(sys.argv[0]) if sys.argv and sys.argv[0] else ""
    if argv0.endswith(".pyz") and os.path.isfile(argv0):
        return argv0
    return ""


def _read_json(path):
    try:
        with open(path, "r", encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return None


def _fail(msg):
    sampling.log("plasma generate: " + msg)
    return contract.EXIT_FAILED
