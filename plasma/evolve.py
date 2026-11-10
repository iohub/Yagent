"""离线进化（`evolve`）：把遥测账本折成适应度，再产出若干个**确定性变异**候选。

为什么进化在 Python：变异策略（改 prompt / 换工具 / 调旋钮）是**内容生成**，
按 ADR-1 归 Python；而"候选能不能用"的裁决留在 Go（编译 1-4 + 5a + 5b），
所以这里只产出候选 bundle JSON，不判断它们对不对。

## 三道 Goodhart 防线里属于本文件的那一道

适应度**只统计 `fixture_run=false`** 的样本（`_production_records`）。夹具样本是
"我们自己出的题"，把它算进成功率等于让出题动作给被考对象加分 —— 这一条由
`plasma/tests/test_evolve.py` 的混合样本用例钉住。

## 门禁

进化默认关闭（`evolve_enable=false`，§6.8：它是唯一会在后台改系统行为的开关）。
policy 由 Go 侧（`yagent plasma evolve`）写进 `<out>/policy.json`；Python 侧**独立**
再判一次 —— 两侧都判不是冗余：直接跑 `python3 plasma.pyz evolve` 时只有这一道。

## 输入与输出

    plasma.pyz evolve --in telemetry.jsonl --store ~/.yagent/agents --out <dir> [--max-candidates N]

    <out>/policy.json              Go 写，Python 读（含 evolve_enable / promote_min_runs）
    <out>/candidates/<i>-<kind>.json  候选 bundle（最小 spec 或完整 IR 形状）
    stdout                        一份 JSON 账目（分组适应度 + 候选 + 被跳过的原因）

**不直连 provider**（ADR-7）：本命令不调 LLM。需要 LLM 的变异（prompt 重写）留 P12 ——
首版只做确定性变异，宁可慢一点，也不把密钥与路由搬到 Python 里。

退出码：0 = 产出了候选（含"0 个候选但有可解释原因"）；1 = 拒绝执行（进化关闭）；
2 = 用法错；3 = 内部错误。
"""

import json
import os
import sys

import contract

SCHEMA_VERSION = 1
POLICY_SCHEMA_VERSION = 1

# 变异种类：每条都必须可解释、可复现（同输入同输出），否则"进化"会变成一次抽签。
MUTATION_TOOL_ADD = "tool-add"
MUTATION_PROMPT_LOCAL = "prompt-local"
MUTATION_RUNTIME_KNOB = "runtime-knob"

# tool-add 的候选池。两条硬要求，缺一条那种变异就恒被拒（等于只有两种变异）：
#   1. 名字必须**真实注册**在 toolspec 里 —— 写个不存在的名字得到的是 PLS-101，
#      而"这个变异必然编译不过"不携带任何信息，只是白烧一轮；
#   2. Effect 必须落在 PlasmaPalette 内（只读族）—— 加 run_bash / create_file 只会
#      得到一条 PLS-201/505，那是权限升级而不是变异（palette.go:25-28）。
# 池子按"信息量"排序：探查类工具最可能补上一个只会 read_file 的 Agent 的短板。
TOOL_ADD_POOL = ("list_dir", "print_dir_tree", "semantic_search", "find_function_caller")

# runtime-knob 的调整幅度：往下收 3 步。放宽不是变异而是"花钱买通过"。
MAX_STEPS_DELTA = -3

PROMPT_LOCAL_SUFFIX = "\n在给出结论前，先用一次 read_file 或 grep_search 核对关键证据。"


def run(in_path, out_dir, store_dir, max_candidates=3):
    if not in_path:
        return _usage("缺 --in <telemetry.jsonl>")
    if not out_dir:
        return _usage("缺 --out <dir>")
    if not store_dir:
        return _usage("缺 --store <plasma store 目录>")

    policy = _load_policy(out_dir)
    if policy is None:
        return _usage("缺 %s —— 它由 `yagent plasma evolve` 写（默认关闭，必须显式开）"
                      % os.path.join(out_dir, "policy.json"))
    if not policy.get("evolve_enable"):
        print("plasma evolve: 进化默认关闭（[plasma] evolve_enable=false）；"
              "要运行请显式打开该开关", file=sys.stderr)
        return contract.EXIT_FAILED

    try:
        records, bad_lines = _read_telemetry(in_path)
    except RuntimeError as exc:
        # 缺账本是最常见的一种"这条命令没什么可做"：它不是内部错误，
        # 所以不走 cli 的兜底（那会把原因包成"内部错误：RuntimeError: …"）。
        print("plasma evolve: %s" % exc, file=sys.stderr)
        return contract.EXIT_FAILED
    production = _production_records(records)
    excluded = len(records) - len(production)

    groups = _groups(production)
    scored = sorted(groups, key=lambda g: (-g["fitness"], g["agent"], g["bundle_hash"]))
    min_runs = _positive(policy.get("promote_min_runs"), 5)
    for g in scored:
        # 样本不足**只影响升档**，不影响它作为变异父本：丢弃才是浪费数据。
        g["promote_eligible"] = g["samples"] >= min_runs

    candidates, skipped = _candidates(scored, store_dir, _positive(max_candidates, 3))
    _write_candidates(out_dir, candidates)

    ledger = {
        "schema_version": SCHEMA_VERSION,
        "samples_total": len(records),
        "samples_used": len(production),
        "excluded_fixture_runs": excluded,
        "bad_lines": bad_lines,
        "promote_min_runs": min_runs,
        "groups": scored,
        "candidates": candidates,
        "skipped": skipped,
    }
    _log_ledger(ledger)
    json.dump(ledger, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return contract.EXIT_OK


def fitness(records):
    """§6.7 的适应度：成功率 + 1/步数 + 1/token + 批准率 - 降级率 - lint 警告率。

    `1/步数` 与 `1/token` 写成 `1/(1+x)`：x=0 时（零步、零 token 的异常样本）
    除零会让整批进化崩掉，而那条样本恰恰是需要被看到的。差值极小，不影响排序。
    """
    total = len(records)
    if total == 0:
        return 0.0, {}
    success = 0
    degraded = 0
    escalated = 0
    lint = 0
    steps_sum = 0.0
    tokens_sum = 0.0
    for rec in records:
        if _is_success(rec):
            success += 1
        if rec.get("degraded"):
            degraded += 1
        if rec.get("escalated"):
            escalated += 1
        lint += _int(rec.get("lint"))
        steps_sum += _int(rec.get("steps"))
        tokens_sum += _int(rec.get("tokens_in")) + _int(rec.get("tokens_out"))

    steps_avg = steps_sum / total
    tokens_avg = tokens_sum / total
    success_rate = float(success) / total
    approval_rate = 1.0 - float(escalated) / total
    degrade_rate = float(degraded) / total
    lint_rate = float(lint) / total
    value = (
        success_rate
        + 1.0 / (1.0 + steps_avg)
        + 1.0 / (1.0 + tokens_avg)
        + approval_rate
        - degrade_rate
        - lint_rate
    )
    return round(value, 6), {
        "success_rate": round(success_rate, 4),
        "steps_avg": round(steps_avg, 2),
        "tokens_avg": round(tokens_avg, 2),
        "approval_rate": round(approval_rate, 4),
        "degrade_rate": round(degrade_rate, 4),
        "lint_rate": round(lint_rate, 4),
    }


def _is_success(rec):
    """成功的定义只有一条：以 agent_exit 收尾、没有错误、也没有降级。

    降级（`degraded`）是一次"报告 + 理由清单"而不是成功交付（agents 硬约束 13），
    把它算成功会让"重放失败"变成一次加分。
    """
    return rec.get("stop_reason") == "agent_exit" and not rec.get("err") and not rec.get("degraded")


def _production_records(records):
    """Goodhart 防线三：适应度只吃生产样本。

    `fixture_run` 缺失按 False 处理 —— 账本字段是 P9b 加的，老行没有它，
    而那些行确实是生产样本。
    """
    return [r for r in records if not r.get("fixture_run")]


def _read_telemetry(path):
    try:
        with open(path, "r", encoding="utf-8") as fh:
            lines = fh.readlines()
    except OSError as exc:
        raise RuntimeError("读不到 %s：%s" % (path, exc))
    out = []
    bad = 0
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            rec = json.loads(line)
        except ValueError:
            # 账本是追加写的：进程被杀会留下半行。跳过并计数，不因此中断整批进化。
            bad += 1
            continue
        if isinstance(rec, dict):
            out.append(rec)
        else:
            bad += 1
    return out, bad


def _groups(records):
    buckets = {}
    for rec in records:
        agent = str(rec.get("agent") or "").strip()
        hash_ = str(rec.get("bundle_hash") or "").strip()
        if not agent or not hash_:
            # 没有坐标就无法回到父 bundle：这类样本只能记一笔"用不上"。
            continue
        buckets.setdefault((agent, hash_), []).append(rec)
    out = []
    for (agent, hash_), recs in buckets.items():
        value, metrics = fitness(recs)
        out.append({
            "agent": agent,
            "bundle_hash": hash_,
            "samples": len(recs),
            "fitness": value,
            "metrics": metrics,
        })
    return out


def _candidates(scored, store_dir, limit):
    """挑最好的分组当父本，按三种确定性变异各产一份。"""
    candidates = []
    skipped = []
    if not scored:
        return candidates, skipped

    parent = _load_parent(scored[0], store_dir)
    if parent is None:
        for g in scored:
            if _load_parent(g, store_dir) is not None:
                parent = _load_parent(g, store_dir)
                break
    if parent is None:
        for g in scored:
            skipped.append({
                "agent": g["agent"],
                "bundle_hash": g["bundle_hash"],
                "reason": "store 里找不到父 bundle（%s）；进化需要磁盘上的原文才能算变异"
                          % os.path.join(store_dir, g["agent"], g["bundle_hash"], "bundle.json"),
            })
        return candidates, skipped

    group, bundle = parent
    for kind in (MUTATION_TOOL_ADD, MUTATION_PROMPT_LOCAL, MUTATION_RUNTIME_KNOB):
        if len(candidates) >= limit:
            break
        mutated = _mutate(bundle, kind)
        if mutated is None:
            skipped.append({
                "agent": group["agent"],
                "bundle_hash": group["bundle_hash"],
                "reason": "变异 %s 在这份父 bundle 上无适用点（无需改动即无候选）" % kind,
            })
            continue
        lineage = "evolve:%s" % kind
        mutated["lineage"] = {
            "parent_hash": group["bundle_hash"],
            "mutation": lineage,
            # rejected[] 由 Go 侧在编译失败时填（诊断码）—— 免费的负样本，
            # 下一轮生成时作为反例喂回去。
            "rejected": [],
        }
        candidates.append({
            "id": "%d-%s" % (len(candidates), kind),
            "mutation": lineage,
            "parent_agent": group["agent"],
            "parent_hash": group["bundle_hash"],
            "bundle": mutated,
        })
    return candidates, skipped


def _load_parent(group, store_dir):
    path = os.path.join(store_dir, group["agent"], group["bundle_hash"], "bundle.json")
    try:
        with open(path, "r", encoding="utf-8") as fh:
            bundle = json.load(fh)
    except (OSError, ValueError):
        return None
    if not isinstance(bundle, dict):
        return None
    return group, bundle


def _mutate(bundle, kind):
    """三种确定性变异。返回 None 表示"这份父本上没有可改的点"。

    变异**不继承**父本的 fixtures 与 quality：夹具是不可变的（ADR-5），
    新 hash 必须重新过 5a/5b 才能拿 grade —— 复制一份旧的等于让夹具跟着变异走。
    """
    clone = json.loads(json.dumps(bundle))
    clone.pop("fixtures", None)
    clone.pop("quality", None)

    if kind == MUTATION_TOOL_ADD:
        # 两种输入形态都要认：最小 spec 用 tools_used，完整 IR 用 tools。
        key = "tools_used" if "tools_used" in clone else "tools"
        tools = clone.get(key)
        if not isinstance(tools, list):
            return None
        have = {t for t in tools if isinstance(t, str)}
        for name in TOOL_ADD_POOL:
            if name not in have:
                clone[key] = list(tools) + [name]
                return clone
        return None

    if kind == MUTATION_PROMPT_LOCAL:
        prompt = clone.get("prompt")
        system = ""
        if isinstance(prompt, dict):
            system = str(prompt.get("system") or "")
        elif isinstance(clone.get("agent_design"), str):
            system = clone["agent_design"]
        if not system.strip():
            return None
        if PROMPT_LOCAL_SUFFIX in system:
            return None
        if isinstance(prompt, dict):
            clone["prompt"] = dict(prompt, system=system + PROMPT_LOCAL_SUFFIX)
        else:
            clone["agent_design"] = system + PROMPT_LOCAL_SUFFIX
        return clone

    if kind == MUTATION_RUNTIME_KNOB:
        runtime = clone.get("runtime")
        if not isinstance(runtime, dict):
            runtime = {}
        steps = _int(runtime.get("max_steps"))
        if steps <= 0:
            return None
        new_steps = steps + MAX_STEPS_DELTA
        if new_steps < 1:
            new_steps = 1
        if new_steps == steps:
            return None
        clone["runtime"] = dict(runtime, max_steps=new_steps)
        return clone

    return None


def _write_candidates(out_dir, candidates):
    if not candidates:
        return
    target_dir = os.path.join(out_dir, "candidates")
    os.makedirs(target_dir, exist_ok=True)
    for cand in candidates:
        path = os.path.join(target_dir, "%s.json" % cand["id"])
        _write_json(path, cand["bundle"])
        cand["path"] = path


def _load_policy(out_dir):
    path = os.path.join(out_dir, "policy.json")
    try:
        with open(path, "r", encoding="utf-8") as fh:
            policy = json.load(fh)
    except (OSError, ValueError):
        return None
    if not isinstance(policy, dict):
        return None
    if policy.get("schema_version") != POLICY_SCHEMA_VERSION:
        # 不认的版本必须拒跑：半旧策略会让门禁按另一种口径生效。
        return None
    return policy


def _log_ledger(ledger):
    print(
        "plasma evolve: samples=%d（排除夹具样本 %d），groups=%d，candidates=%d，skipped=%d"
        % (ledger["samples_used"], ledger["excluded_fixture_runs"],
           len(ledger["groups"]), len(ledger["candidates"]), len(ledger["skipped"])),
        file=sys.stderr,
    )
    for g in ledger["groups"]:
        print(
            "plasma evolve: 父本 %s@%s samples=%d fitness=%.4f promote_eligible=%s"
            % (g["agent"], g["bundle_hash"][:12], g["samples"], g["fitness"],
               g["promote_eligible"]),
            file=sys.stderr,
        )
    for cand in ledger["candidates"]:
        print("plasma evolve: 候选 %s（%s）→ %s"
              % (cand["id"], cand["mutation"], cand.get("path", "")), file=sys.stderr)
    for item in ledger["skipped"]:
        print("plasma evolve: 跳过 %s@%s：%s"
              % (item["agent"], item["bundle_hash"][:12], item["reason"]), file=sys.stderr)


def _write_json(path, payload):
    parent = os.path.dirname(os.path.abspath(path))
    if parent:
        os.makedirs(parent, exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, ensure_ascii=False, indent=2)
        fh.write("\n")
    os.replace(tmp, path)


def _int(value):
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0


def _positive(value, default):
    n = _int(value)
    return n if n > 0 else default


def _usage(msg):
    print("plasma evolve: " + msg, file=sys.stderr)
    return contract.EXIT_USAGE
