"""state.json 的读写（Python 私有状态，Go 从不读它）。

状态机只有三个 phase，每次进程运行**最多推进一个状态步**（ADR-14）：

    start   —— 还没发过采样请求（首次 draft，或 --fix 后重新起草）
    waiting —— 已写 llm_req_{seq}.json，等 Go 写回 llm_resp_{seq}.json
    done    —— 已写 bundle.json

为什么必须跨 respawn 保留：Go 侧只负责"写响应 + 重启进程"，策略是 Python 的。
把多轮塞进一次进程会让 deque/内存里的中间结果与文件里的序号不同步；而每次
只推进一步，任何一步崩掉都可以从 state.json 重放（顺序由 seq 决定，不看时间）。

schema_version 不匹配时**删掉整个 state 重新开始**：硬跑旧状态的表现是
"模型收到了上一版协议的 messages"，比从头来一遍难查得多（风险登记册明列）。
"""

import json
import os

import contract


def path(workdir):
    return os.path.join(workdir, contract.FILE_STATE)


def load(workdir):
    """读 state.json。返回 dict 或 None。

    None 有三种含义（对调用方都一样：从头开始）：
      - 文件不存在（首次运行）；
      - JSON 损坏（半写/被手改）；
      - schema_version 不匹配（旧版本状态）。
    后两种会顺手删掉坏文件，避免下一次仍然读到它。
    """
    p = path(workdir)
    try:
        with open(p, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        _remove(p)
        return None
    if not isinstance(data, dict):
        _remove(p)
        return None
    if data.get("schema_version") != contract.STATE_SCHEMA_VERSION:
        _remove(p)
        return None
    return data


def save(workdir, state):
    """原子写 state.json（先写临时文件再 rename）。

    Python 侧的原子写不必复刻 util.AtomicWrite 的全部选项：这个文件只有本进程读写，
    崩溃时的半写文件会被 load() 的 JSON 解析失败挡掉（上面那条分支）。
    """
    data = dict(state)
    data["schema_version"] = contract.STATE_SCHEMA_VERSION
    p = path(workdir)
    tmp = p + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(data, fh, ensure_ascii=False, indent=2)
        fh.write("\n")
    os.replace(tmp, p)


def clear(workdir):
    _remove(path(workdir))


def fresh(messages=None):
    """一份全新的状态。`messages` 是已经构造好的对话（含 system 提示）。"""
    return {
        "schema_version": contract.STATE_SCHEMA_VERSION,
        "phase": "start",
        "seq": 0,
        "rounds": 0,
        "messages": list(messages or []),
        "issues": [],
        "spec": None,
    }


def _remove(p):
    try:
        os.remove(p)
    except OSError:
        pass
