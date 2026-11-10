"""evolve：适应度只吃生产样本、门禁、以及三种确定性变异（P11 §四.2/§四.3）。

本文件里最重要的一条是 `FixtureSamplesAreExcluded`：它是 Goodhart 防线三的**唯一**
可执行判据。适应度如果把夹具样本算进去，出题动作本身就会给被考对象加分 ——
而那种自证在单条用例上看起来完全正常（分数只是"稍微高一点"）。
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
import evolve  # noqa: E402

PARENT_AGENT = "p11_evolve_agent"
PARENT_HASH = "a" * 64


def _production(steps=4, tokens_in=900, tokens_out=300, ok=True, degraded=False):
    return {
        "agent": PARENT_AGENT,
        "bundle_hash": PARENT_HASH,
        "stop_reason": "agent_exit" if ok else "max_steps",
        "err": not ok,
        "degraded": degraded,
        "escalated": False,
        "lint": 0,
        "steps": steps,
        "tokens_in": tokens_in,
        "tokens_out": tokens_out,
        "fixture_run": False,
    }


def _fixture_run(steps=1, tokens_in=10, tokens_out=5, ok=True):
    """夹具样本：`fixture_run=true`。它们**完美得可疑** —— 正是刷分的形状。"""
    rec = _production(steps=steps, tokens_in=tokens_in, tokens_out=tokens_out, ok=ok)
    rec["fixture_run"] = True
    return rec


def _parent_bundle():
    return {
        "schema_version": 1,
        "name": PARENT_AGENT,
        "display_name": "P11 Evolve Agent",
        "version": "0.1.0",
        "prompt": {"system": "You inspect code and then call agent_exit.", "lang": "en"},
        "tools": ["read_file"],
        "policy": {"workspace_scoped": True},
        "runtime": {"max_steps": 9, "stop_on_finish": True},
        "fixtures": {
            "provenance": {"author": "plasma-compiler-auto", "lineage": "compiler"},
            "structural": [{"id": "exit-contract", "input": "x", "expect": {}}],
        },
        "quality": {"grade": "provisional", "score": 71.0, "runs": 9},
    }


def _write_store(root):
    """把父 bundle 落到 store 的坐标上：`<store>/<agent>/<hash>/bundle.json`。"""
    directory = os.path.join(root, PARENT_AGENT, PARENT_HASH)
    os.makedirs(directory, exist_ok=True)
    path = os.path.join(directory, "bundle.json")
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(_parent_bundle(), fh, ensure_ascii=False)
    return path


def _write_telemetry(path, records, trailing_garbage=False):
    with open(path, "w", encoding="utf-8") as fh:
        for rec in records:
            fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
        if trailing_garbage:
            fh.write('{"agent": "half-written"\n')  # 进程被杀会留下的半行


def _run(records, enable=True, max_candidates=3, trailing_garbage=False):
    root = tempfile.mkdtemp(prefix="evolve-test-")
    store = os.path.join(root, "store")
    out_dir = os.path.join(root, "out")
    os.makedirs(out_dir, exist_ok=True)
    _write_store(store)
    telemetry = os.path.join(root, "telemetry.jsonl")
    _write_telemetry(telemetry, records, trailing_garbage=trailing_garbage)
    with open(os.path.join(out_dir, "policy.json"), "w", encoding="utf-8") as fh:
        json.dump({
            "schema_version": 1,
            "evolve_enable": enable,
            "promote_min_runs": 5,
            "behavioral_token_budget": 60000,
        }, fh)

    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = evolve.run(telemetry, out_dir, store, max_candidates=max_candidates)
    return code, out.getvalue(), err.getvalue(), out_dir


class EvolveFitness(unittest.TestCase):
    def test_fixture_samples_are_excluded(self):
        """Goodhart 防线三：混合样本里，夹具样本必须先被剔掉再算适应度。

        构造方式让两边结果**必然不同**：生产样本全是失败（success_rate=0），
        夹具样本全成功且极省（若被计入，success_rate 会被抬到 0.5）。
        """
        records = [_production(ok=False) for _ in range(5)] + [_fixture_run() for _ in range(1)]
        code, out, err, _ = _run(records)
        self.assertEqual(code, contract.EXIT_OK)
        ledger = json.loads(out)
        self.assertEqual(ledger["samples_total"], 6)
        self.assertEqual(ledger["samples_used"], 5)
        self.assertEqual(ledger["excluded_fixture_runs"], 1)
        self.assertEqual(len(ledger["groups"]), 1)
        group = ledger["groups"][0]
        self.assertEqual(group["samples"], 5)
        self.assertEqual(group["metrics"]["success_rate"], 0.0)
        self.assertIn("排除夹具样本 1", err)

    def test_absolute_fitness_value_matches_production_only(self):
        """把口径钉成数字：适应度公式的任何一项漂移都会让这条红。"""
        records = [_production() for _ in range(2)] + [_fixture_run()]
        code, out, _, _ = _run(records)
        self.assertEqual(code, contract.EXIT_OK)
        group = json.loads(out)["groups"][0]
        want, _ = evolve.fitness([_production() for _ in range(2)])
        self.assertAlmostEqual(group["fitness"], want, places=6)
        self.assertEqual(group["metrics"]["steps_avg"], 4.0)
        self.assertEqual(group["metrics"]["tokens_avg"], 1200.0)

    def test_insufficient_samples_only_blocks_promotion(self):
        """样本不足 ⇒ 不标 promoted，但**不丢弃**（仍然作为变异父本）。"""
        records = [_production() for _ in range(2)]
        code, out, _, _ = _run(records)
        self.assertEqual(code, contract.EXIT_OK)
        ledger = json.loads(out)
        self.assertFalse(ledger["groups"][0]["promote_eligible"])
        self.assertEqual(ledger["promote_min_runs"], 5)
        self.assertGreaterEqual(len(ledger["candidates"]), 1)

    def test_bad_lines_are_counted_not_fatal(self):
        code, out, _, _ = _run([_production() for _ in range(6)], trailing_garbage=True)
        self.assertEqual(code, contract.EXIT_OK)
        self.assertEqual(json.loads(out)["bad_lines"], 1)


class EvolveGate(unittest.TestCase):
    def test_disabled_refuses_to_run(self):
        """evolve_enable=false ⇒ 拒绝执行并 exit 1（不是 exit 2：用法没错）。"""
        code, out, err, out_dir = _run([_production() for _ in range(6)], enable=False)
        self.assertEqual(code, contract.EXIT_FAILED)
        self.assertIn("evolve_enable", err)
        self.assertIn("默认关闭", err)
        # 拒绝时不该留下任何候选：半跑的进化比不跑更危险。
        self.assertFalse(os.path.isdir(os.path.join(out_dir, "candidates")))
        self.assertEqual(out, "")

    def test_missing_policy_refuses_to_run(self):
        root = tempfile.mkdtemp(prefix="evolve-nopolicy-")
        telemetry = os.path.join(root, "telemetry.jsonl")
        _write_telemetry(telemetry, [_production()])
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = evolve.run(telemetry, root, root)
        self.assertEqual(code, contract.EXIT_USAGE)
        self.assertIn("policy.json", err.getvalue())


class EvolveMutations(unittest.TestCase):
    def _candidates(self, max_candidates=3):
        code, out, _, out_dir = _run([_production() for _ in range(6)],
                                     max_candidates=max_candidates)
        self.assertEqual(code, contract.EXIT_OK)
        return json.loads(out)["candidates"], out_dir

    def test_three_kinds_with_lineage(self):
        candidates, out_dir = self._candidates()
        # 谱系带 `evolve:` 前缀：与出题谱系（author-fixtures / compiler）区分开，
        # 也是 Go 侧 CheckFixtureProvenance 判定"自己出题自己考"的字符串依据。
        kinds = [c["bundle"]["lineage"]["mutation"] for c in candidates]
        self.assertEqual(len(kinds), 3)
        self.assertEqual(set(kinds), {
            "evolve:" + evolve.MUTATION_TOOL_ADD,
            "evolve:" + evolve.MUTATION_PROMPT_LOCAL,
            "evolve:" + evolve.MUTATION_RUNTIME_KNOB,
        })
        for cand in candidates:
            self.assertEqual(cand["mutation"], cand["bundle"]["lineage"]["mutation"])
            self.assertEqual(cand["parent_hash"], PARENT_HASH)
            self.assertEqual(cand["parent_agent"], PARENT_AGENT)
            self.assertTrue(os.path.isfile(cand["path"]))
            self.assertTrue(cand["path"].startswith(os.path.join(out_dir, "candidates")))

    def test_mutations_do_not_inherit_fixtures_or_grade(self):
        """夹具不可变（ADR-5）：变异不许把父本的 fixtures/quality 抄过来。

        抄过来的后果是"新 hash 白拿父本的一次通过记录"，也就是防线一在进化侧的破口。
        """
        candidates, _ = self._candidates()
        for cand in candidates:
            bundle = cand["bundle"]
            self.assertNotIn("fixtures", bundle)
            self.assertNotIn("quality", bundle)
            self.assertEqual(bundle["lineage"]["parent_hash"], PARENT_HASH)
            self.assertEqual(bundle["lineage"]["rejected"], [])

    def test_mutation_payloads_are_deterministic(self):
        first, _ = self._candidates()
        second, _ = self._candidates()
        for a, b in zip(first, second):
            self.assertEqual(a["mutation"], b["mutation"])
            self.assertEqual(a["bundle"], b["bundle"])

    def test_tool_add_only_grows_tools(self):
        candidates, _ = self._candidates()
        tool_add = [c for c in candidates
                    if c["bundle"]["lineage"]["mutation"] == "evolve:" + evolve.MUTATION_TOOL_ADD][0]
        before = set(_parent_bundle()["tools"])
        after = set(tool_add["bundle"]["tools"])
        self.assertEqual(len(after), len(before) + 1)
        self.assertTrue(before.issubset(after))

    def test_runtime_knob_only_tightens_steps(self):
        candidates, _ = self._candidates()
        knob = [c for c in candidates
                if c["bundle"]["lineage"]["mutation"] == "evolve:" + evolve.MUTATION_RUNTIME_KNOB][0]
        self.assertLess(knob["bundle"]["runtime"]["max_steps"],
                        _parent_bundle()["runtime"]["max_steps"])

    def test_max_candidates_is_honoured(self):
        candidates, _ = self._candidates(max_candidates=1)
        self.assertEqual(len(candidates), 1)


if __name__ == "__main__":
    unittest.main()
