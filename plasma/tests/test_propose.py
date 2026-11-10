"""propose：提案产出的形状、status 恒为 pending、以及"绝不写 *.prompt.md"（P12 §四.3）。

本文件里最重要的一条是 `NeverWritesPromptFiles`：它是"提案永不自动 apply"的**唯一**
可执行判据。批准/拒绝只改提案 JSON 里的 status 字段；一旦有人让 propose 顺手把建议
落到 `*.prompt.md`，内建 Agent 的 prompt 与 Go 代码就会失去同步（根 `AGENTS.md` §4），
而这种不一致在单条用例上看起来只是"提案生效了"。

`StatusIsAlwaysPending` 是任务 §四.4 变异二的靶子：把 `status` 直接写成 `approved`
（或任何非 pending 值）必须立刻变红 —— 提案的初始状态是"等人批"，不是"已批准"。
"""

import contextlib
import hashlib
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
import propose  # noqa: E402

AGENT = "coding"
PROMPT = "# Coding\n\nAutonomous software engineering agent. You own the file system.\n"


def _payload(**overrides):
    data = {
        "schema_version": propose.PROPOSE_SCHEMA_VERSION,
        "agent": AGENT,
        "prompt": PROMPT,
        "prompt_hash": hashlib.sha256(PROMPT.encode("utf-8")).hexdigest(),
        "prompt_path": "internal/agents/coding.prompt.md",
        "editable_fields": ["prompt.system"],
        "telemetry": {
            "window": 200,
            "runs": 8,
            "failures": 4,
            "escalations": 2,
            "stop_reasons": {"max_steps": 3, "agent_exit": 5},
            "top_errors": ["provider timeout"],
        },
        "lint": ["PLS-301: prompt 没有写收尾契约"],
    }
    data.update(overrides)
    # prompt 被替换时 hash 必须跟着重算，否则每个用例都在测"hash 对不上"那条分支。
    if "prompt" in overrides and "prompt_hash" not in overrides:
        data["prompt_hash"] = hashlib.sha256(str(overrides["prompt"]).encode("utf-8")).hexdigest()
    return data


class ProposeTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.in_path = os.path.join(self.tmp.name, "in.json")
        self.out_dir = os.path.join(self.tmp.name, "proposals")

    def _write_input(self, payload):
        with open(self.in_path, "w", encoding="utf-8") as fh:
            json.dump(payload, fh, ensure_ascii=False)

    def _run(self, in_path=None, out_path=None):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            code = propose.run(self.in_path if in_path is None else in_path,
                               self.out_dir if out_path is None else out_path)
        return code, buf.getvalue()

    def _files(self):
        return sorted(os.listdir(self.out_dir))

    def _load(self):
        names = [n for n in self._files() if n.endswith(".json")]
        self.assertEqual(len(names), 1, "应当恰好产出一份提案 JSON：%s" % self._files())
        with open(os.path.join(self.out_dir, names[0]), "r", encoding="utf-8") as fh:
            return json.load(fh)

    def test_output_shape(self):
        self._write_input(_payload())
        code, stdout = self._run()
        self.assertEqual(code, contract.EXIT_OK)
        doc = self._load()
        self.assertTrue(doc["base_hash"], "base_hash 不能为空")
        self.assertEqual(doc["base_hash"], hashlib.sha256(PROMPT.encode("utf-8")).hexdigest())
        self.assertTrue(doc["diff"].strip(), "diff 不能为空")
        self.assertIn("+## ", doc["diff"], "diff 里要有可抄的追加行")
        self.assertTrue(doc["rationale"].strip())
        self.assertEqual(doc["agent"], AGENT)
        self.assertEqual(doc["id"], os.path.splitext(os.path.basename(self._only_json()))[0])

        envelope = json.loads(stdout)
        self.assertEqual(envelope["status"], contract_status())
        self.assertEqual(envelope["id"], doc["id"])
        self.assertEqual(envelope["json"], os.path.join(self.out_dir, doc["id"] + ".json"))

    def test_status_is_always_pending(self):
        self._write_input(_payload())
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_OK)
        doc = self._load()
        self.assertEqual(doc["status"], propose.PROPOSAL_STATUS_PENDING)
        self.assertEqual(doc["status"], "pending")
        self.assertNotIn(doc["status"], (propose.PROPOSAL_STATUS_APPROVED, propose.PROPOSAL_STATUS_REJECTED))

    def test_never_writes_prompt_files(self):
        self._write_input(_payload())
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_OK)
        files = self._files()
        self.assertEqual(len(files), 2, "只允许产出 .json + .md 两个文件：%s" % files)
        for name in files:
            self.assertFalse(name.endswith(".prompt.md"), "propose 不许写 *.prompt.md：%s" % name)
        self.assertEqual(len([n for n in files if n.endswith(".json")]), 1)
        self.assertEqual(len([n for n in files if n.endswith(".md")]), 1)
        # 提案目录之外也不该多出任何东西（输入文件之外）。
        self.assertEqual(sorted(os.listdir(self.tmp.name)), ["in.json", "proposals"])

    def test_empty_telemetry_falls_back_to_r5(self):
        self._write_input(_payload(telemetry={}, lint=[]))
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_OK)
        self.assertEqual(self._load()["rule_ids"], ["R5"])

    def test_failures_select_r1(self):
        self._write_input(_payload(telemetry={"runs": 9, "failures": 4}, lint=[]))
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_OK)
        rules = self._load()["rule_ids"]
        self.assertIn("R1", rules)
        self.assertNotIn("R5", rules)

    def test_rule_below_threshold_is_not_selected(self):
        self._write_input(_payload(telemetry={"runs": 10, "failures": 2}, lint=[]))
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_OK)
        self.assertNotIn("R1", self._load()["rule_ids"])

    def test_marker_already_in_prompt_suppresses_rule(self):
        prompt = PROMPT + "\n## Finish Checklist\n\n已有段落。\n"
        self._write_input(_payload(prompt=prompt))
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_OK)
        doc = self._load()
        self.assertNotIn("R1", doc["rule_ids"], "prompt 里已有该段就不该重复建议")
        self.assertTrue(doc["diff"].strip())

    def test_all_rules_suppressed_falls_back_to_r5(self):
        prompt = PROMPT + "\n## Deliverable Contract\n\n已有。\n"
        self._write_input(_payload(prompt=prompt, telemetry={"runs": 1, "failures": 0}, lint=[]))
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_OK)
        # R5 的 marker 已存在 ⇒ 没有任何新建议 ⇒ 仍然必须给出非空 diff（回落 R5 段落）。
        doc = self._load()
        self.assertEqual(doc["rule_ids"], ["R5"])
        self.assertTrue(doc["diff"].strip())

    def test_missing_in_or_out_is_usage_error(self):
        self._write_input(_payload())
        self.assertEqual(self._run(in_path="")[0], contract.EXIT_USAGE)
        self.assertEqual(self._run(out_path="")[0], contract.EXIT_USAGE)

    def test_missing_input_file_fails(self):
        code, _ = self._run(in_path=os.path.join(self.tmp.name, "nope.json"))
        self.assertEqual(code, contract.EXIT_FAILED)

    def test_prompt_hash_mismatch_fails(self):
        self._write_input(_payload(prompt_hash="0" * 64))
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_FAILED)

    def test_empty_prompt_fails(self):
        self._write_input(_payload(prompt="  \n"))
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_FAILED)

    def test_bad_schema_version_fails(self):
        self._write_input(_payload(schema_version=99))
        code, _ = self._run()
        self.assertEqual(code, contract.EXIT_FAILED)

    def test_same_second_does_not_overwrite(self):
        self._write_input(_payload())
        self.assertEqual(self._run()[0], contract.EXIT_OK)
        first = self._load()["id"]
        self.assertEqual(self._run()[0], contract.EXIT_OK)
        ids = set()
        for name in self._files():
            if not name.endswith(".json"):
                continue
            with open(os.path.join(self.out_dir, name), "r", encoding="utf-8") as fh:
                ids.add(json.load(fh)["id"])
        self.assertEqual(len(ids), 2, "同一秒内两次提案必须落在两份文件上，不许覆盖")
        self.assertIn(first, ids)

    def _only_json(self):
        names = [n for n in self._files() if n.endswith(".json")]
        return os.path.join(self.out_dir, names[0])


def contract_status():
    return propose.PROPOSAL_STATUS_PENDING


if __name__ == "__main__":
    unittest.main()
