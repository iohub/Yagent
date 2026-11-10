"""跨语言契约的 Python 镜像（zipapp 侧）。

本文件的每一个常量都与 Go 侧**逐字对应**，并且由
`internal/plasma/contract_py_test.go` 反向读取源码做对账（反漂移闸门）。

为什么常量要写成 `NAME = <literal>` 一行一条：Go 侧测试用正则在**源码文本**上
提取字面量，多行拼接 / 计算式 / 别名赋值都会让闸门失效，而闸门失效的表现是
"两侧漂了但没人红"。所以这里不写 `EXIT_OK = 0 if x else 1` 这种形态。

对照表（Go 侧）：`internal/plasma/workdir.go` 的 Exit*/File*/LLMReq/LLMResp/workdirPrefix，
`internal/plasma/manifest.go` 的 ManifestSchemaVersion。
"""

# ---- 退出码（workdir.go: ExitOK..ExitSampling）----
EXIT_OK = 0
EXIT_FAILED = 1
EXIT_USAGE = 2
EXIT_TIMEOUT = 3
EXIT_SAMPLING = 4

# ---- workdir 契约文件名（workdir.go: File*）----
FILE_REQ = "req.json"
FILE_MANIFEST = "manifest.json"
FILE_STATE = "state.json"
FILE_DIAG = "diagnostics.json"
FILE_BUNDLE = "bundle.json"

# ---- 采样请求/响应的序号命名（workdir.go: LLMReq/LLMResp，%03d 从 000 起）----
LLM_REQ_PATTERN = "llm_req_%03d.json"
LLM_RESP_PATTERN = "llm_resp_%03d.json"

# ---- 孤儿 workdir 的命名前缀（workdir.go: workdirPrefix）----
WORKDIR_PREFIX = "plasma-"

# ---- manifest 的 schema_version（manifest.go: ManifestSchemaVersion）----
MANIFEST_SCHEMA_VERSION = 1

# ---- state.json 的 schema_version（Python 私有；Go 不碰这个文件）----
# 不匹配 ⇒ 整轮重来（清状态重新 draft），不要硬跑：半旧状态会把上一轮的
# messages 与新协议混在一起，症状是"模型看起来很笨"，实际是状态机错位。
STATE_SCHEMA_VERSION = 1


def llm_req_name(seq):
    """`llm_req_%03d.json`：序号按下标对齐，不靠时间戳、不扫目录。"""
    return LLM_REQ_PATTERN % seq


def llm_resp_name(seq):
    """`llm_resp_%03d.json`：Go 侧 serveSample 写的是同一个下标。"""
    return LLM_RESP_PATTERN % seq


def exit_name(code):
    """给退出码一个人话名字（与 Go 的 ExitCodeName 同口径）。"""
    return {
        EXIT_OK: "ok",
        EXIT_FAILED: "业务失败",
        EXIT_USAGE: "用法错",
        EXIT_TIMEOUT: "超时被杀",
        EXIT_SAMPLING: "需要 LLM 采样",
    }.get(code, "未知")
