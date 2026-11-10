"""generate 状态机：每次运行只推进一步，序号按 state.seq 对齐。"""

import json
import os
import sys
import tempfile
import unittest

_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _ROOT not in sys.path:
    sys.path.insert(0, _ROOT)

import contract  # noqa: E402
import generate  # noqa: E402
import state  # noqa: E402

MANIFEST = {
    "schema_version": 1,
    "capability_lock": "deadbeef",
    "palette": ["fs_read"],
    "tools": [
        {"name": "read_file", "effects": ["fs_read"], "brief": "读文件"},
        {"name": "agent_exit", "effects": [], "brief": "收尾"},
    ],
    "codes": [{"code": "PLS-101", "severity": "error", "summary": "unknown tool"}],
    "defaults": {"max_steps": 15},
}

SPEC = {
    "thinking": "只读分析任务，分配只读工具。",
    "agent_name": "Log Leak Auditor",
    "agent_design": "### Role\nYou audit logs for credential leakage.\n\n### Instructions\n1. Search.\n2. Read.\n3. Report findings with file:line.",
    "tools_used": ["read_file", "agent_exit"],
    "task_for_agent": "找出把会话令牌写进日志的调用点。",
}


def _workdir(root):
    w = os.path.join(root, "plasma-test")
    os.makedirs(w, exist_ok=True)
    with open(os.path.join(w, contract.FILE_REQ), "w", encoding="utf-8") as fh:
        json.dump({"task": "审查日志泄漏", "context": "只读即可"}, fh)
    with open(os.path.join(w, contract.FILE_MANIFEST), "w", encoding="utf-8") as fh:
        json.dump(MANIFEST, fh)
    return w


def _write_resp(workdir, seq, content, finish="stop"):
    payload = {"choices": [{"content": content, "finish_reason": finish}], "usage": {"total_tokens": 42}}
    with open(os.path.join(workdir, contract.llm_resp_name(seq)), "w", encoding="utf-8") as fh:
        json.dump(payload, fh)


class GenerateStateMachineTest(unittest.TestCase):
    def test_first_run_requests_sampling(self):
        with tempfile.TemporaryDirectory() as root:
            w = _workdir(root)
            self.assertEqual(generate.run(w), contract.EXIT_SAMPLING)
            self.assertTrue(os.path.isfile(os.path.join(w, "llm_req_000.json")))
            with open(os.path.join(w, contract.FILE_STATE), "r", encoding="utf-8") as fh:
                st = json.load(fh)
            self.assertEqual(st["phase"], "waiting")
            self.assertEqual(st["seq"], 0)
            with open(os.path.join(w, "llm_req_000.json"), "r", encoding="utf-8") as fh:
                req = json.load(fh)
            self.assertEqual(set(req), {"messages", "max_tokens", "temperature"})
            self.assertIn("read_file", req["messages"][0]["content"])
            self.assertNotIn(generate.SEED_PLACEHOLDER, req["messages"][0]["content"])

    def test_valid_response_produces_bundle(self):
        with tempfile.TemporaryDirectory() as root:
            w = _workdir(root)
            self.assertEqual(generate.run(w), contract.EXIT_SAMPLING)
            _write_resp(w, 0, json.dumps(SPEC, ensure_ascii=False))
            self.assertEqual(generate.run(w, resume=True), contract.EXIT_OK)
            with open(os.path.join(w, contract.FILE_BUNDLE), "r", encoding="utf-8") as fh:
                bundle = json.load(fh)
            self.assertEqual(set(bundle), {"thinking", "agent_name", "agent_design", "tools_used", "task_for_agent"})
            self.assertEqual(bundle["tools_used"], SPEC["tools_used"])

    def test_invalid_response_asks_for_refine(self):
        with tempfile.TemporaryDirectory() as root:
            w = _workdir(root)
            self.assertEqual(generate.run(w), contract.EXIT_SAMPLING)
            _write_resp(w, 0, "这不是 JSON，只是一句解释。")
            self.assertEqual(generate.run(w, resume=True), contract.EXIT_SAMPLING)
            self.assertTrue(os.path.isfile(os.path.join(w, "llm_req_001.json")))
            with open(os.path.join(w, contract.FILE_STATE), "r", encoding="utf-8") as fh:
                st = json.load(fh)
            self.assertEqual(st["seq"], 1)
            self.assertTrue(st["issues"])
            self.assertEqual(st["messages"][-2]["role"], "assistant")
            self.assertEqual(st["messages"][-1]["role"], "user")
            self.assertIn("JSON", st["messages"][-1]["content"])

    def test_unknown_tool_is_reported_with_suggestion(self):
        with tempfile.TemporaryDirectory() as root:
            w = _workdir(root)
            self.assertEqual(generate.run(w), contract.EXIT_SAMPLING)
            bad = dict(SPEC)
            bad["tools_used"] = ["read_filez", "agent_exit"]
            _write_resp(w, 0, json.dumps(bad, ensure_ascii=False))
            self.assertEqual(generate.run(w, resume=True), contract.EXIT_SAMPLING)
            with open(os.path.join(w, contract.FILE_STATE), "r", encoding="utf-8") as fh:
                st = json.load(fh)
            self.assertTrue(any("read_filez" in issue for issue in st["issues"]))
            self.assertTrue(any("read_file" in issue for issue in st["issues"]))

    def test_missing_response_file_fails_loudly(self):
        with tempfile.TemporaryDirectory() as root:
            w = _workdir(root)
            self.assertEqual(generate.run(w), contract.EXIT_SAMPLING)
            # 故意不写 llm_resp_000.json：Go 侧契约被破坏时必须点名，而不是重发请求。
            self.assertEqual(generate.run(w, resume=True), contract.EXIT_FAILED)

    def test_stale_state_schema_restarts(self):
        with tempfile.TemporaryDirectory() as root:
            w = _workdir(root)
            self.assertEqual(generate.run(w), contract.EXIT_SAMPLING)
            stale = {
                "schema_version": contract.STATE_SCHEMA_VERSION + 1,
                "phase": "waiting",
                "seq": 5,
                "rounds": 0,
                "messages": [{"role": "user", "content": "旧协议"}],
                "issues": [],
                "spec": None,
            }
            with open(os.path.join(w, contract.FILE_STATE), "w", encoding="utf-8") as fh:
                json.dump(stale, fh)
            self.assertEqual(generate.run(w, resume=True), contract.EXIT_SAMPLING)
            with open(os.path.join(w, contract.FILE_STATE), "r", encoding="utf-8") as fh:
                st = json.load(fh)
            self.assertEqual(st["seq"], 0)
            self.assertEqual(st["phase"], "waiting")
            self.assertNotIn("旧协议", json.dumps(st["messages"], ensure_ascii=False))

    def test_fix_feeds_diagnostics_verbatim(self):
        with tempfile.TemporaryDirectory() as root:
            w = _workdir(root)
            self.assertEqual(generate.run(w), contract.EXIT_SAMPLING)
            _write_resp(w, 0, json.dumps(SPEC, ensure_ascii=False))
            self.assertEqual(generate.run(w, resume=True), contract.EXIT_OK)
            with open(os.path.join(w, contract.FILE_DIAG), "w", encoding="utf-8") as fh:
                json.dump({
                    "diagnostics": [{
                        "code": "PLS-101",
                        "severity": "error",
                        "path": "bundle.tools[0]",
                        "message": 'unknown tool "read_filez"',
                        "note": 'did you mean "read_file"?',
                    }]
                }, fh)
            self.assertEqual(generate.run(w, resume=True, fix=True), contract.EXIT_SAMPLING)
            with open(os.path.join(w, contract.FILE_STATE), "r", encoding="utf-8") as fh:
                st = json.load(fh)
            self.assertEqual(st["seq"], 1)
            last = st["messages"][-1]["content"]
            self.assertIn("PLS-101", last)
            self.assertIn("bundle.tools[0]", last)
            self.assertIn('did you mean "read_file"?', last)

    def test_done_without_fix_is_idempotent(self):
        with tempfile.TemporaryDirectory() as root:
            w = _workdir(root)
            self.assertEqual(generate.run(w), contract.EXIT_SAMPLING)
            _write_resp(w, 0, json.dumps(SPEC, ensure_ascii=False))
            self.assertEqual(generate.run(w, resume=True), contract.EXIT_OK)
            self.assertEqual(generate.run(w, resume=True), contract.EXIT_OK)

    def test_round_limit_fails(self):
        with tempfile.TemporaryDirectory() as root:
            w = _workdir(root)
            self.assertEqual(generate.run(w), contract.EXIT_SAMPLING)
            code = contract.EXIT_SAMPLING
            for seq in range(generate.MAX_ROUNDS):
                _write_resp(w, seq, "still not json")
                code = generate.run(w, resume=True)
            self.assertEqual(code, contract.EXIT_FAILED)
            self.assertFalse(os.path.exists(os.path.join(w, contract.FILE_STATE)))


class ExtractJSONTest(unittest.TestCase):
    def test_braces_inside_strings_do_not_truncate(self):
        text = '前言 {"agent_name": "A {B}", "n": 1} 后记'
        obj = generate.extract_json_object(text)
        self.assertEqual(obj["agent_name"], "A {B}")

    def test_markdown_fence_is_tolerated(self):
        text = '```json\n{"agent_name": "X"}\n```'
        self.assertEqual(generate.extract_json_object(text), {"agent_name": "X"})

    def test_garbage_returns_none(self):
        self.assertIsNone(generate.extract_json_object("no json here"))


if __name__ == "__main__":
    unittest.main()
