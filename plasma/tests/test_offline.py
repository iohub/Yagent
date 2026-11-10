"""离线子命令：explain 与 author-fixtures 的输出契约（stdout 只有一份 JSON）。"""

import contextlib
import io
import json
import os
import sys
import tempfile
import unittest

_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _ROOT not in sys.path:
    sys.path.insert(0, _ROOT)

import author_fixtures  # noqa: E402
import cli  # noqa: E402
import contract  # noqa: E402
import explain  # noqa: E402

REPORT = {
    "diagnostics": [
        {
            "code": "PLS-101",
            "severity": "error",
            "path": "bundle.tools[0]",
            "message": 'unknown tool "read_filez"',
            "note": 'did you mean "read_file"?',
        },
        {
            "code": "PLS-302",
            "severity": "warning",
            "path": "bundle.prompt.system",
            "message": "提示里的参数名与工具 schema 不一致",
        },
    ],
    "escalation_required": False,
}

SPEC = {
    "thinking": "只读分析。",
    "agent_name": "Log Leak Auditor",
    "agent_design": "### Role\nAudit logs.\n\n### Instructions\nSearch, read, report.",
    "tools_used": ["read_file", "agent_exit"],
    "task_for_agent": "找出把令牌写进日志的调用点。",
}


def _run_cli(argv):
    out = io.StringIO()
    err = io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = cli.main(argv)
    return code, out.getvalue(), err.getvalue()


class ExplainTest(unittest.TestCase):
    def test_writes_text_and_json_envelope(self):
        with tempfile.TemporaryDirectory() as root:
            report_path = os.path.join(root, "report.json")
            with open(report_path, "w", encoding="utf-8") as fh:
                json.dump(REPORT, fh)
            out_path = os.path.join(root, "explain.txt")
            code, stdout, _ = _run_cli(["explain", "--in", report_path, "--out", out_path])
            self.assertEqual(code, contract.EXIT_OK)
            envelope = json.loads(stdout)
            self.assertEqual(envelope["errors"], 1)
            self.assertEqual(envelope["warnings"], 1)
            with open(out_path, "r", encoding="utf-8") as fh:
                text = fh.read()
        self.assertIn("PLS-101", text)
        self.assertIn("did you mean", text)
        self.assertIn("合计：1 error / 1 warning", text)
        self.assertIn("修复方向", text)

    def test_empty_report_still_writes_a_sentence(self):
        with tempfile.TemporaryDirectory() as root:
            report_path = os.path.join(root, "report.json")
            with open(report_path, "w", encoding="utf-8") as fh:
                json.dump({"diagnostics": [], "grade": "provisional"}, fh)
            out_path = os.path.join(root, "explain.txt")
            code, stdout, _ = _run_cli(["explain", "--in", report_path, "--out", out_path])
            self.assertEqual(code, contract.EXIT_OK)
            self.assertEqual(json.loads(stdout)["diagnostics"], 0)
            with open(out_path, "r", encoding="utf-8") as fh:
                self.assertIn("没有诊断条目", fh.read())

    def test_missing_input_is_usage_error(self):
        code, _, err = _run_cli(["explain"])
        self.assertEqual(code, contract.EXIT_USAGE)
        self.assertIn("--in", err)

    def test_bad_json_is_business_failure(self):
        with tempfile.TemporaryDirectory() as root:
            bad = os.path.join(root, "bad.json")
            with open(bad, "w", encoding="utf-8") as fh:
                fh.write("not json")
            code, _, err = _run_cli(["explain", "--in", bad])
        self.assertEqual(code, contract.EXIT_FAILED)
        self.assertIn("不是合法 JSON", err)


class AuthorFixturesTest(unittest.TestCase):
    def test_drafts_a_fixture_from_minimal_spec(self):
        with tempfile.TemporaryDirectory() as root:
            bundle_path = os.path.join(root, "bundle.json")
            with open(bundle_path, "w", encoding="utf-8") as fh:
                json.dump(SPEC, fh)
            out_path = os.path.join(root, "fixtures.json")
            code, stdout, _ = _run_cli(["author-fixtures", "--bundle", bundle_path, "--out", out_path])
            self.assertEqual(code, contract.EXIT_OK)
            payload = json.loads(stdout)
            self.assertTrue(payload["draft"])
            with open(out_path, "r", encoding="utf-8") as fh:
                written = json.load(fh)
        self.assertEqual(len(written["fixtures"]), 1)
        fixture = written["fixtures"][0]
        self.assertEqual(set(fixture), {"id", "input", "script", "expect", "rubric"})
        self.assertEqual(fixture["script"][-1]["tool"], "agent_exit")
        self.assertEqual(fixture["expect"]["must_call"], ["read_file"])
        self.assertIn("agent_exit", [step["tool"] for step in fixture["script"]])

    def test_empty_tool_pool_fails(self):
        with tempfile.TemporaryDirectory() as root:
            bundle_path = os.path.join(root, "bundle.json")
            with open(bundle_path, "w", encoding="utf-8") as fh:
                json.dump({"agent_name": "X", "tools_used": []}, fh)
            code, _, err = _run_cli(["author-fixtures", "--bundle", bundle_path])
        self.assertEqual(code, contract.EXIT_FAILED)
        self.assertIn("工具", err)

    def test_render_is_stable(self):
        """同一份诊断渲染两次逐字相同（explain 的输出会进日志/工单，抖动没有价值）。"""
        items = explain._items(REPORT)
        self.assertEqual(explain.render(REPORT, items), explain.render(REPORT, items))


class StubCommandTest(unittest.TestCase):
    def test_unimplemented_commands_exit_usage(self):
        for name in ("evolve", "judge", "propose"):
            code, _, err = _run_cli([name])
            self.assertEqual(code, contract.EXIT_USAGE)
            self.assertIn("未实现", err)

    def test_no_command_prints_help_to_stderr(self):
        code, _, err = _run_cli([])
        self.assertEqual(code, contract.EXIT_USAGE)
        self.assertIn("generate", err)


if __name__ == "__main__":
    unittest.main()
