"""离线评分（`judge`）：把一次 5a+5b 的实测结果折成 0-100 分与一个判决。

为什么评分在 Python 而不在 Go：分值是**进化的适应度**，而适应度的口径会变（加一项、
调权重）。放在 Go 里改一次权重要动编译产物与内嵌 zipapp；放在这里就是一次改字符串。
反过来，能机械判定的部分（Expect 求值）留在 Go —— 见 `internal/agents/plasma_fixture.go`。

输入（`--in`，Go 侧写出的 outcomes.json）：

    {"schema_version":1, "agent":"…", "bundle_hash":"…", "grade":"provisional",
     "lint_warnings":0,
     "fixtures":[{"fixture_id":"…","passed":true,"reason":"","steps":3,
                  "tokens_in":…,"tokens_out":…,"stop_kind":"agent_exit",
                  "deliverable_chars":42,"rubric":[…]}]}

输出（`--out` 与 stdout，同一份）：

    {"schema_version":1,"score":0-100,"verdict":"pass|fail",
     "reasons":[…],"metrics":{…}}

评分口径（**首版确定性启发式，不调 LLM**，每一项都写进 stderr 日志，便于解释）：

    score = 50×fixture_pass_rate
          + 15×exit_contract_ok
          + 10×deliverable_nonempty
          + 15×steps_efficiency      # 1 步满分，21 步 0 分
          + 10×token_efficiency      # 0 token 满分，60000 token 0 分
          -  2×lint_warnings
    截断到 [0,100] 后四舍五入。

    verdict = pass 当且仅当 全部夹具通过 **且** exit 契约成立 **且** 交付非空 **且** score ≥ 60。
    三条"必须为真"的一条不成立就 fail —— 分数是连续量，判决是门槛，
    把判决也交给分数会让一次"夹具全红但 token 很省"拿到 60 分。

退出码（与 contract.py 同一张表）：
    0 = verdict=pass
    1 = verdict=fail（**判决写出来了**，这不是"命令失败"）
    2 = 用法错（缺参数 / 输入不是合法 JSON / 结构不对）
    3 = 内部错误（异常；与 contract.EXIT_TIMEOUT 同数值槽，judge 不做超时，不冲突）

stdout 只许一份 JSON，日志与口径一律写 stderr（§7.3）。
"""

import json
import os
import sys

import contract

SCHEMA_VERSION = 1

# 权重是公开口径：改它要同时改上面的 docstring 与 stderr 的口径日志。
W_PASS_RATE = 50.0
W_EXIT = 15.0
W_DELIVERABLE = 10.0
W_STEPS = 15.0
W_TOKENS = 10.0
LINT_PENALTY = 2.0

STEPS_FULL = 1.0
STEPS_ZERO = 21.0
TOKENS_ZERO = 60000.0
PASS_THRESHOLD = 60.0

# 与 Go 侧 plasma.FixtureStopKindAgentExit 逐字同值（plasma/fixture.go）。
STOP_KIND_AGENT_EXIT = "agent_exit"


def run(in_path, out_path=""):
    if not in_path:
        return _usage("缺 --in <outcomes.json>")
    try:
        with open(in_path, "r", encoding="utf-8") as fh:
            payload = json.load(fh)
    except OSError as exc:
        return _usage("读不到 %s：%s" % (in_path, exc))
    except ValueError as exc:
        return _usage("%s 不是合法 JSON：%s" % (in_path, exc))
    if not isinstance(payload, dict):
        return _usage("%s 顶层不是 JSON 对象" % in_path)

    fixtures = payload.get("fixtures")
    if fixtures is None:
        fixtures = []
    if not isinstance(fixtures, list):
        return _usage("%s 的 fixtures 不是数组" % in_path)

    verdict = evaluate(fixtures, lint_warnings=_lint(payload.get("lint_warnings")))
    _log_criteria()
    _log_verdict(verdict)

    if out_path:
        target = out_path
        if os.path.isdir(target) or target.endswith(os.sep):
            target = os.path.join(target, "verdict.json")
        _write_json(target, verdict)

    json.dump(verdict, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return contract.EXIT_OK if verdict["verdict"] == "pass" else contract.EXIT_FAILED


def evaluate(fixtures, lint_warnings=0):
    """把夹具观测折成分值与判决（纯函数：同样的输入永远同样的输出）。"""
    reasons = []
    total = len(fixtures)
    passed = 0
    steps_sum = 0
    tokens_sum = 0
    exit_ok = False
    deliverable_nonempty = total > 0
    for item in fixtures:
        if not isinstance(item, dict):
            continue
        if bool(item.get("passed")):
            passed += 1
        steps_sum += _int(item.get("steps"))
        tokens_sum += _int(item.get("tokens_in")) + _int(item.get("tokens_out"))
        if item.get("stop_kind") == STOP_KIND_AGENT_EXIT:
            exit_ok = True
        if _int(item.get("deliverable_chars")) <= 0:
            deliverable_nonempty = False

    pass_rate = (float(passed) / total) if total else 0.0
    steps_avg = (float(steps_sum) / total) if total else 0.0
    tokens_avg = (float(tokens_sum) / total) if total else 0.0
    steps_eff = _clamp01(1.0 - (steps_avg - STEPS_FULL) / (STEPS_ZERO - STEPS_FULL))
    tokens_eff = _clamp01(1.0 - tokens_avg / TOKENS_ZERO)

    score = (
        W_PASS_RATE * pass_rate
        + W_EXIT * (1.0 if exit_ok else 0.0)
        + W_DELIVERABLE * (1.0 if deliverable_nonempty else 0.0)
        + W_STEPS * steps_eff
        + W_TOKENS * tokens_eff
        - LINT_PENALTY * lint_warnings
    )
    score = max(0.0, min(100.0, score))
    score_int = int(round(score))

    if total == 0:
        # 没有观测就没有判决依据：一条夹具都没跑出来的"通过"是最危险的一种绿灯。
        reasons.append("没有夹具观测（fixtures 为空）：没有证据就不能判通过")
    if passed < total:
        reasons.append("%d/%d 条夹具失败" % (total - passed, passed))
    if not exit_ok:
        reasons.append("没有任何一条夹具以 %s 收尾：退出契约未被证实" % STOP_KIND_AGENT_EXIT)
    if not deliverable_nonempty and total:
        reasons.append("交付文本为空（deliverable_chars <= 0）：没有产出可核对的内容")
    if lint_warnings > 0:
        reasons.append("lint 警告 %d 条（每条 -%.0f 分）" % (lint_warnings, LINT_PENALTY))
    if steps_eff < 1.0:
        reasons.append("步数效率 %.2f（平均 %.1f 步）" % (steps_eff, steps_avg))
    if tokens_eff < 1.0:
        reasons.append("token 效率 %.2f（平均 %.0f token）" % (tokens_eff, tokens_avg))

    ok = total > 0 and passed == total and exit_ok and deliverable_nonempty
    verdict = "pass" if (ok and score_int >= PASS_THRESHOLD) else "fail"
    if ok and score_int < PASS_THRESHOLD:
        reasons.append("分数 %d 低于门槛 %.0f" % (score_int, PASS_THRESHOLD))

    return {
        "schema_version": SCHEMA_VERSION,
        "score": score_int,
        "verdict": verdict,
        "reasons": reasons,
        "metrics": {
            "fixtures_total": total,
            "fixtures_passed": passed,
            "fixture_pass_rate": round(pass_rate, 4),
            "exit_contract_ok": exit_ok,
            "deliverable_nonempty": deliverable_nonempty,
            "steps_avg": round(steps_avg, 2),
            "tokens_avg": round(tokens_avg, 2),
            "steps_efficiency": round(steps_eff, 4),
            "token_efficiency": round(tokens_eff, 4),
            "lint_warnings": lint_warnings,
        },
    }


def _log_criteria():
    print(
        "plasma judge: 口径 score = %.0f×pass_rate + %.0f×exit_contract + %.0f×deliverable"
        " + %.0f×steps_eff + %.0f×token_eff - %.0f×lint；pass 需全绿且 score>=%.0f"
        % (W_PASS_RATE, W_EXIT, W_DELIVERABLE, W_STEPS, W_TOKENS, LINT_PENALTY, PASS_THRESHOLD),
        file=sys.stderr,
    )


def _log_verdict(verdict):
    m = verdict["metrics"]
    print(
        "plasma judge: verdict=%s score=%d fixtures=%d/%d exit_ok=%s deliverable=%s"
        % (
            verdict["verdict"],
            verdict["score"],
            m["fixtures_passed"],
            m["fixtures_total"],
            m["exit_contract_ok"],
            m["deliverable_nonempty"],
        ),
        file=sys.stderr,
    )


def _clamp01(v):
    return 0.0 if v < 0.0 else (1.0 if v > 1.0 else v)


def _int(value):
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0


def _lint(value):
    n = _int(value)
    return n if n > 0 else 0


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
    print("plasma judge: " + msg, file=sys.stderr)
    return contract.EXIT_USAGE
