"""judge 的输出契约与确定性（P11 §四.1）。

判据有三条硬的：
  1. stdout 只有一份 JSON（日志一律 stderr）—— 否则调用方的 `json.load` 会被日志污染；
  2. 同样的输入永远同样的输出（评分是**口径**，不是抽签）；
  3. 判决是门槛而不是分数的别名：夹具全红但 token 很省，不许拿到 pass。
"""

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

import contract  # noqa: E402
import judge  # noqa: E402


def _fixture(fixture_id, passed=True, steps=3, tokens_in=800, tokens_out=200,
             stop_kind="agent_exit", deliverable_chars=120):
    return {
        "fixture_id": fixture_id,
        "passed": passed,
        "reason": "" if passed else "交付文本缺少「结论」",
        "steps": steps,
        "tokens_in": tokens_in,
        "tokens_out": tokens_out,
        "stop_kind": stop_kind,
        "deliverable_chars": deliverable_chars,
        "rubric": ["结论要有依据"],
    }


def _payload(fixtures, lint_warnings=0):
    return {
        "schema_version": 1,
        "agent": "p11_behavioral_agent",
        "bundle_hash": "deadbeef" * 8,
        "grade": "provisional",
        "lint_warnings": lint_warnings,
        "fixtures": fixtures,
    }


def _run(payload, out_path=""):
    """跑一次 judge.run，返回 (exit_code, stdout_text, stderr_text)。"""
    tmp = tempfile.mkdtemp(prefix="judge-test-")
    in_path = os.path.join(tmp, "outcomes.json")
    with open(in_path, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, ensure_ascii=False)

    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = judge.run(in_path, out_path)
    return code, out.getvalue(), err.getvalue()


class JudgeOutputShape(unittest.TestCase):
    def test_stdout_is_exactly_one_json_document(self):
        code, out, err = _run(_payload([_fixture("behavioral-exit")]))
        self.assertEqual(code, contract.EXIT_OK)
        parsed = json.loads(out)  # 多一行日志就会在这里炸
        self.assertEqual(parsed["schema_version"], 1)
        self.assertIn("score", parsed)
        self.assertIn(parsed["verdict"], ("pass", "fail"))
        self.assertIsInstance(parsed["reasons"], list)
        # 口径必须写进 stderr：分数要能被解释，否则一次 59 分没人知道差在哪。
        self.assertIn("score =", err)

    def test_scoring_is_deterministic(self):
        payload = _payload([_fixture("a"), _fixture("b", passed=False)])
        _, first, _ = _run(payload)
        _, second, _ = _run(payload)
        self.assertEqual(json.loads(first), json.loads(second))

    def test_writes_out_file_when_asked(self):
        tmp = tempfile.mkdtemp(prefix="judge-out-")
        target = os.path.join(tmp, "verdict.json")
        _, out, _ = _run(_payload([_fixture("a")]), out_path=target)
        with open(target, encoding="utf-8") as fh:
            on_disk = json.load(fh)
        self.assertEqual(on_disk, json.loads(out))


class JudgeVerdict(unittest.TestCase):
    def test_all_green_is_pass(self):
        code, out, _ = _run(_payload([_fixture("a"), _fixture("b")]))
        verdict = json.loads(out)
        self.assertEqual(code, contract.EXIT_OK)
        self.assertEqual(verdict["verdict"], "pass")
        self.assertGreaterEqual(verdict["score"], judge.PASS_THRESHOLD)

    def test_failed_fixture_is_fail(self):
        code, out, _ = _run(_payload([_fixture("a"), _fixture("b", passed=False)]))
        verdict = json.loads(out)
        self.assertEqual(code, contract.EXIT_FAILED)
        self.assertEqual(verdict["verdict"], "fail")
        # 机读侧在 metrics，人读侧在 reasons：两者都要，缺一个就是"判了但说不清"。
        self.assertEqual(verdict["metrics"]["fixtures_passed"], 1)
        self.assertTrue(any("条夹具失败" in r for r in verdict["reasons"]))

    def test_cheap_but_red_cannot_pass(self):
        """夹具全红但 token 极省、步数极少：分数可能不低，判决必须是 fail。

        这条就是"判决不是分数的别名"的判据 —— 把 verdict 交给 score 会让
        「省 token 的失败」混进 verified。
        """
        red = [_fixture("a", passed=False, steps=1, tokens_in=10, tokens_out=5)]
        code, out, _ = _run(_payload(red))
        verdict = json.loads(out)
        self.assertEqual(code, contract.EXIT_FAILED)
        self.assertEqual(verdict["verdict"], "fail")

    def test_empty_deliverable_fails(self):
        code, out, _ = _run(_payload([_fixture("a", deliverable_chars=0)]))
        verdict = json.loads(out)
        self.assertEqual(code, contract.EXIT_FAILED)
        self.assertEqual(verdict["verdict"], "fail")
        self.assertFalse(verdict["metrics"]["deliverable_nonempty"])
        self.assertTrue(any("交付文本为空" in r for r in verdict["reasons"]))

    def test_missing_stop_kind_fails_exit_contract(self):
        code, out, _ = _run(_payload([_fixture("a", stop_kind="max_steps")]))
        verdict = json.loads(out)
        self.assertEqual(code, contract.EXIT_FAILED)
        self.assertFalse(verdict["metrics"]["exit_contract_ok"])
        self.assertTrue(any("退出契约" in r for r in verdict["reasons"]))

    def test_empty_fixtures_is_never_pass(self):
        """零条观测的"全过"是最危险的一种绿灯：空集不许判 pass。"""
        code, out, _ = _run(_payload([]))
        verdict = json.loads(out)
        self.assertEqual(code, contract.EXIT_FAILED)
        self.assertEqual(verdict["verdict"], "fail")
        self.assertTrue(any("没有夹具观测" in r for r in verdict["reasons"]))


class JudgeUsage(unittest.TestCase):
    def test_missing_in_is_exit_usage(self):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = judge.run("")
        self.assertEqual(code, contract.EXIT_USAGE)
        self.assertEqual(out.getvalue(), "")
        self.assertIn("--in", err.getvalue())

    def test_broken_json_is_exit_usage(self):
        tmp = tempfile.mkdtemp(prefix="judge-bad-")
        path = os.path.join(tmp, "outcomes.json")
        with open(path, "w", encoding="utf-8") as fh:
            fh.write("{not json")
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = judge.run(path)
        self.assertEqual(code, contract.EXIT_USAGE)
        self.assertEqual(out.getvalue(), "")
        self.assertIn("不是合法 JSON", err.getvalue())


if __name__ == "__main__":
    unittest.main()
