"""采样通道的 Python 侧：写 llm_req_NNN.json，读 llm_resp_NNN.json。

**形状唯一真源是 Go**（`internal/agents/trampoline.go` 的 llmRequestFile 与
`internal/llm/engine.go` 的 Message/Response）。这里只做两件事：

  - 按 Go 的 json tag 组装请求（messages/options），**不发明字段**；
  - 从 Go 写的响应里取 `choices[0].content` 与 usage。

刻意没有 `tier`：引擎本身绑定档位，Go 侧拿到一个 tier 字符串也无处施加 ——
多一个没人使用的字段就是装饰性配置。Go 的 llmRequestFile 里也没有它。

日志一律走 stderr：stdout 在 generate 路径上是**禁止**的（§7.3 只允许一份 JSON，
而 generate 的产物是文件不是 stdout）。
"""

import json
import os
import sys

import contract


def req_path(workdir, seq):
    return os.path.join(workdir, contract.llm_req_name(seq))


def resp_path(workdir, seq):
    return os.path.join(workdir, contract.llm_resp_name(seq))


def write_request(workdir, seq, messages, tools=None, max_tokens=4096, temperature=0.2):
    """写一份采样请求（Go 侧 serveSample 读它）。

    `messages` 的每一项必须是 Go `llm.Message` 的形状：role 必填，
    content/tool_calls/tool_call_id/name 按需。这里不校验形状 —— 校验在 Go 侧
    （空 messages 会被明确报错），Python 侧再抄一份判据就是两份同形实现。
    """
    req = {
        "messages": list(messages),
        "max_tokens": int(max_tokens),
        "temperature": float(temperature),
    }
    if tools:
        req["tools"] = list(tools)
    p = req_path(workdir, seq)
    _write_json(p, req)
    return p


def read_response(workdir, seq):
    """读 Go 写的响应。文件不存在 ⇒ 抛 FileNotFoundError（调用方翻成可读错误）。"""
    p = resp_path(workdir, seq)
    with open(p, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict):
        raise ValueError("%s 不是 JSON 对象" % os.path.basename(p))
    return data


def content_of(resp):
    """取 `choices[0].content`；取不到返回空串。

    Go 的 Response.choices 是数组（现在恒 1 条）。取第一条而不是拼接全部：
    多 choice 的语义是"候选答案"，拼起来等于把两个答案混成一个。
    """
    choices = resp.get("choices")
    if not isinstance(choices, list) or not choices:
        return ""
    first = choices[0]
    if not isinstance(first, dict):
        return ""
    content = first.get("content")
    return content if isinstance(content, str) else ""


def usage_of(resp):
    """取 usage（六个字段与 Go TokenUsage 同名）；缺失返回空 dict。"""
    usage = resp.get("usage")
    return usage if isinstance(usage, dict) else {}


def finish_reason_of(resp):
    choices = resp.get("choices")
    if not isinstance(choices, list) or not choices:
        return ""
    first = choices[0]
    if not isinstance(first, dict):
        return ""
    reason = first.get("finish_reason")
    return reason if isinstance(reason, str) else ""


def log(msg):
    """所有 Python 日志的唯一出口：stderr。"""
    print(msg, file=sys.stderr)


def _write_json(path, data):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(data, fh, ensure_ascii=False, indent=2)
        fh.write("\n")
    os.replace(tmp, path)
