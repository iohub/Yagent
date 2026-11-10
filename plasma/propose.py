"""离线提案（`propose`）：给**内建** Agent 的 prompt 出一份 diff 提案。

安全模型（§1.2 / §8.1 T5）没变：Python **不碰工作区、不直连 provider、不持密钥**。
所需材料全部由 Go 写进 `--in` 文件：

    {
      "schema_version": 1,
      "agent": "coding",
      "prompt": "<内建 prompt 全文>",
      "prompt_hash": "<Go 算好的 sha256，用来与这里重算的值对账>",
      "prompt_path": "internal/agents/coding.prompt.md",
      "editable_fields": ["prompt.system", "runtime.max_steps"],
      "telemetry": {"window":200,"runs":7,"failures":3,"escalations":1,
                    "stop_reasons":{"max_steps":3},"avg_steps":18.4,
                    "top_errors":["..."]},
      "lint": ["PLS-301: ..."]
    }

产出**两个**文件（都落在 `--out` 目录）：

    <agent>-<timestamp>.json   结构化提案（status 恒为 pending）
    <agent>-<timestamp>.md     人读的说明 + diff

**永不自动 apply**：内建 Agent 的 prompt 与 Go 代码是"改行为要同时改两处"的耦合
（根 `AGENTS.md` §4），所以这份产物只是提案 —— 本模块**不写**任何 `*.prompt.md`，
连打开它们的路径都没有。TUI `/proposals approve` 只改 JSON 里的 status 字段。

启发式是**确定性**的：同一份输入（同样的 prompt + 同样的遥测摘要）永远得到同样的
`rationale`/`diff`，不读时钟做判断、不用随机数、不读环境变量（时间戳只进文件名与
`created_at`）。LLM 一律不经这里 —— 要走模型只能经 `yagent plasma sample`。

stdout 只发一份 JSON 信封（§7.3：离线子命令 stdout 仅一份 JSON），日志走 stderr。
"""

import datetime
import hashlib
import json
import os
import sys

import contract

# 输入/输出文档的 schema_version。Go 侧写的是同一个数；对不上直接拒收 ——
# "尽力解析"会把旧协议当新协议读，症状是提案里少了一半字段却没人说话。
PROPOSE_SCHEMA_VERSION = 1

# 提案状态只有三个值，且**只有 pending 会被这里产出**。approved/rejected 是
# TUI 的判决结果（Go 侧写），Python 永远不写它们（任务 §四.4 的变异二就钉这条）。
PROPOSAL_STATUS_PENDING = "pending"
PROPOSAL_STATUS_APPROVED = "approved"
PROPOSAL_STATUS_REJECTED = "rejected"

# 遥测窗口的默认值：与 Go 侧 `[plasma]` 无关，只影响"看多少条最近的生产样本"。
DEFAULT_TELEMETRY_WINDOW = 200

# 规则顺序即 diff 段落顺序，也是测试断言顺序。固定顺序是确定性的一部分：
# 集合型遍历会让同一份输入产出不同字节的提案。
RULE_ORDER = ["R1", "R2", "R3", "R4", "R5"]


class Rule(object):
    """一条启发式规则：命中判据 + 追加到 prompt 末尾的片段。"""

    def __init__(self, rule_id, title, why, marker, block):
        self.rule_id = rule_id
        self.title = title
        self.why = why
        self.marker = marker
        self.block = block


# 追加片段一律以 `## ` 小标题开头：`marker` 就是那个小标题，用来判"prompt 里已经有
# 这一段了"（有就不要重复建议 —— 重复提案会让人以为每次运行都在发现新问题）。
_RULES = [
    Rule(
        "R1",
        "finish checklist",
        "最近的运行里有失败样本",
        "## Finish Checklist",
        "\n".join([
            "## Finish Checklist",
            "",
            "判据三条，全部满足才允许 agent_exit：",
            "1. 交付物已落盘，且路径写进 handoff.conclusion；",
            "2. `verify` 命令的退出码为 0（失败就修到 0，不要带着红跑收尾）；",
            "3. reason 里一句话说清这次交付**是什么**，不写心路历程。",
            "",
        ]),
    ),
    Rule(
        "R2",
        "step budget discipline",
        "有运行撞上 max_steps 收尾",
        "## Step Budget",
        "\n".join([
            "## Step Budget",
            "",
            "先做最少必要的一组动作再收尾：读 → 改 → 验 → 收。",
            "不要为了一次改动反复重读同一批文件；撞到步数上限的 run 等于什么都没交付。",
            "",
        ]),
    ),
    Rule(
        "R3",
        "escalation criteria",
        "有运行触发了能力升级",
        "## Escalation",
        "\n".join([
            "## Escalation",
            "",
            "越权动作（写文件之外的能力）只在两种情况下发起：",
            "1. 任务明确要求；2. 说明为什么只读路径做不到，并给出替代方案。",
            "没有这两条就不发起，直接报告\"做不到 + 缺什么\"。",
            "",
        ]),
    ),
    Rule(
        "R4",
        "lint hygiene",
        "编译期 lint 有未消化的告警",
        "## Prompt Hygiene",
        "\n".join([
            "## Prompt Hygiene",
            "",
            "prompt 里提到的工具名与参数名必须与注册表逐字一致；",
            "声明放弃的能力（policy.deny）不要在同一份 prompt 里出现。",
            "",
        ]),
    ),
    Rule(
        "R5",
        "deliverable contract",
        "没有命中任何失败模式，按交付契约做保守加固",
        "## Deliverable Contract",
        "\n".join([
            "## Deliverable Contract",
            "",
            "收尾时交付正文只写结论与证据；过程与试错留在工具调用里，不要搬进交付。",
            "handoff.conclusion 一句话概括结论，便于上层索引。",
            "",
        ]),
    ),
]


def run(in_path, out_path):
    """`propose --in <input.json> --out <proposal_dir>`。"""
    if not in_path:
        return _usage("缺 --in")
    if not out_path:
        return _usage("缺 --out")
    try:
        with open(in_path, "r", encoding="utf-8") as fh:
            payload = json.load(fh)
    except OSError as exc:
        return _fail("读提案输入失败（%s）：%s" % (in_path, exc))
    except ValueError as exc:
        return _fail("%s 不是合法 JSON：%s" % (in_path, exc))
    if not isinstance(payload, dict):
        return _fail("%s 的顶层必须是 JSON 对象" % in_path)

    schema = payload.get("schema_version")
    if schema != PROPOSE_SCHEMA_VERSION:
        return _fail("输入 schema_version = %r，本版本只认 %d"
                     % (schema, PROPOSE_SCHEMA_VERSION))

    agent = str(payload.get("agent") or "").strip()
    prompt = payload.get("prompt")
    if not agent:
        return _usage("输入缺少 agent")
    if not isinstance(prompt, str) or not prompt.strip():
        return _fail("输入里的 prompt 为空：%s 没有可提案的基准文本" % agent)

    base_hash = hashlib.sha256(prompt.encode("utf-8")).hexdigest()
    declared = str(payload.get("prompt_hash") or "").strip()
    if declared and declared != base_hash:
        # 两侧算法必须同源：不一致说明 Go 读到的 prompt 与这里看到的不同，
        # 那样写出的 base_hash 会在 TUI 审批时指向一份不存在的基准。
        return _fail("prompt_hash 对不上（Go 侧 %s，本地 %s）" % (declared, base_hash))

    telemetry = payload.get("telemetry") if isinstance(payload.get("telemetry"), dict) else {}
    lint = payload.get("lint") if isinstance(payload.get("lint"), list) else []
    prompt_path = str(payload.get("prompt_path") or "").strip()
    editable = payload.get("editable_fields") if isinstance(payload.get("editable_fields"), list) else []

    hits, evidence = _evaluate(prompt, telemetry, lint)
    rationale = _rationale(agent, hits, telemetry)
    diff = _diff(prompt_path, hits)

    created = datetime.datetime.now(datetime.timezone.utc)
    proposal_id = _allocate(out_path, agent, created)
    if not proposal_id:
        return _fail("在 %s 里连续 5 秒都拿不到可用的提案文件名（同名文件已存在）" % out_path)

    json_path = os.path.join(out_path, proposal_id + ".json")
    md_path = os.path.join(out_path, proposal_id + ".md")
    document = {
        "schema_version": PROPOSE_SCHEMA_VERSION,
        "id": proposal_id,
        "agent": agent,
        # status 恒为 pending：这里**不允许**出现 approved/rejected（任务 §四.4 变异二）。
        "status": PROPOSAL_STATUS_PENDING,
        "base_hash": base_hash,
        "rationale": rationale,
        "diff": diff,
        "created_at": created.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "rule_ids": [r.rule_id for r in hits],
        "evidence": evidence,
        "editable_fields": [str(x) for x in editable],
        "prompt_path": prompt_path,
    }
    try:
        os.makedirs(out_path, exist_ok=True)
        _write_text(json_path, json.dumps(document, ensure_ascii=False, indent=2) + "\n")
        _write_text(md_path, _markdown(document))
    except OSError as exc:
        return _fail("写提案失败（%s）：%s" % (out_path, exc))

    envelope = {
        "id": proposal_id,
        "agent": agent,
        "status": PROPOSAL_STATUS_PENDING,
        "base_hash": base_hash,
        "rule_ids": document["rule_ids"],
        "json": json_path,
        "md": md_path,
    }
    json.dump(envelope, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return contract.EXIT_OK


def _evaluate(prompt, telemetry, lint):
    """按固定顺序挑出命中的规则，返回 (hits, evidence)。

    判据全部来自计数，不看时间/随机数；prompt 里已出现该段落标记的规则直接跳过
    （同一段建议不重复提）。一条都没命中时用 R5 兜底 —— 提案不能是空 diff。
    """
    runs = _as_int(telemetry.get("runs"))
    failures = _as_int(telemetry.get("failures"))
    escalations = _as_int(telemetry.get("escalations"))
    stop_reasons = telemetry.get("stop_reasons") if isinstance(telemetry.get("stop_reasons"), dict) else {}
    max_steps = _as_int(stop_reasons.get("max_steps"))

    evidence = [
        "runs=%d（窗口 %d）" % (runs, _as_int(telemetry.get("window")) or DEFAULT_TELEMETRY_WINDOW),
        "failures=%d" % failures,
        "escalations=%d" % escalations,
        "stop_reasons=%s" % _render_stop_reasons(stop_reasons),
    ]
    top_errors = [str(e) for e in (telemetry.get("top_errors") or []) if str(e).strip()][:3]
    for item in top_errors:
        evidence.append("err: " + item)
    for item in lint[:3]:
        evidence.append("lint: " + str(item))

    wanted = {
        "R1": failures >= 3 or (runs > 0 and failures * 3 >= runs),
        "R2": max_steps >= 2,
        "R3": escalations >= 1,
        "R4": len(lint) > 0,
        "R5": False,
    }
    hits = []
    for rule in _RULES:
        if rule.rule_id == "R5":
            continue  # R5 只做兜底：没有别的命中时才有它，不该与 R1..R4 并列
        if not wanted.get(rule.rule_id):
            continue
        if rule.marker in prompt:
            continue
        hits.append(rule)
    if not hits:
        hits = [_RULES[-1]]
    return hits, evidence


def _rationale(agent, hits, telemetry):
    parts = []
    for rule in hits:
        parts.append("%s：%s" % (rule.title, rule.why))
    head = "给 %s 的提案，依据是最近的生产遥测。" % agent
    return head + "；".join(parts) + "。"


def _diff(prompt_path, hits):
    """生成 unified-diff 形态的**纯文本**（不落盘、不改任何文件）。

    只写 `+` 行：提案是\"在 prompt 末尾追加一段\"，没有删除任何既有内容，
    所以不需要上下文行；渲染出来的形状仍然是人能直接照着抄的 diff。
    """
    target = prompt_path or "prompt.system"
    lines = ["--- a/" + target, "+++ b/" + target]
    for rule in hits:
        lines.append("@@ %s：%s @@" % (rule.rule_id, rule.title))
        for line in rule.block.splitlines():
            lines.append("+" + line)
    return "\n".join(lines) + "\n"


def _markdown(document):
    body = [
        "# 提案 %s" % document["id"],
        "",
        "- agent: `%s`" % document["agent"],
        "- 状态: `%s`（审批只改这个字段，**不会**改写任何 `*.prompt.md`）" % document["status"],
        "- base_hash: `%s`" % document["base_hash"],
        "- 创建时间: %s" % document["created_at"],
        "- 规则: %s" % ", ".join(document["rule_ids"]),
        "",
        "## 理由",
        "",
        document["rationale"],
        "",
        "## 证据",
        "",
    ]
    body.extend("- " + item for item in document["evidence"])
    body.extend([
        "",
        "## 建议的改动（diff）",
        "",
        "```diff",
        document["diff"].rstrip("\n"),
        "```",
        "",
        "## 审批",
        "",
        "在 TUI 里 `/proposals approve %s` 或 `/proposals reject %s`。" % (document["id"], document["id"]),
        "**批准也不会自动 apply**：内建 Agent 的 prompt 与 Go 代码要同时改（根 `AGENTS.md` §4）。",
        "",
    ])
    return "\n".join(body)


def _allocate(out_path, agent, created):
    """给提案找一个没被占用的 id：`<agent>-<ts>`，撞名就往后挪一秒（最多 5 次）。

    id 形态必须固定（TUI 的 `/proposals <id>` 要能当成文件名用），所以不用 `-2` 后缀；
    撞名只可能发生在同一秒内连跑两次，挪一秒既保形态又不会覆盖已有提案。
    """
    os.makedirs(out_path, exist_ok=True)
    for offset in range(5):
        stamp = (created + datetime.timedelta(seconds=offset)).strftime("%Y%m%dT%H%M%SZ")
        candidate = "%s-%s" % (agent, stamp)
        if not os.path.exists(os.path.join(out_path, candidate + ".json")) and \
                not os.path.exists(os.path.join(out_path, candidate + ".md")):
            return candidate
    return ""


def _render_stop_reasons(stop_reasons):
    if not stop_reasons:
        return "{}"
    keys = sorted(str(k) for k in stop_reasons.keys())
    return "{" + ", ".join("%s=%d" % (k, _as_int(stop_reasons.get(k))) for k in keys) + "}"


def _as_int(value):
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0


def _write_text(path, text):
    """原子写（同目录 tmp + rename）：半写的提案 JSON 会让 TUI 的 list 读到一个坏文件。"""
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        fh.write(text)
    os.replace(tmp, path)


def _usage(msg):
    print("plasma propose: " + msg, file=sys.stderr)
    return contract.EXIT_USAGE


def _fail(msg):
    print("plasma propose: " + msg, file=sys.stderr)
    return contract.EXIT_FAILED
