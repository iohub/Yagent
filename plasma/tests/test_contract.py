"""契约镜像的自我校验（与 Go 侧的反漂移闸门共用同一份样例）。"""

import json
import os
import sys
import tempfile
import unittest

_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _ROOT not in sys.path:
    sys.path.insert(0, _ROOT)

import contract  # noqa: E402
import sampling  # noqa: E402
import generate  # noqa: E402

_TESTDATA = os.path.join(_ROOT, "testdata", "contract")


class ContractConstantsTest(unittest.TestCase):
    def test_exit_codes_match_go(self):
        self.assertEqual(contract.EXIT_OK, 0)
        self.assertEqual(contract.EXIT_FAILED, 1)
        self.assertEqual(contract.EXIT_USAGE, 2)
        self.assertEqual(contract.EXIT_TIMEOUT, 3)
        self.assertEqual(contract.EXIT_SAMPLING, 4)

    def test_file_names_match_go(self):
        self.assertEqual(contract.FILE_REQ, "req.json")
        self.assertEqual(contract.FILE_MANIFEST, "manifest.json")
        self.assertEqual(contract.FILE_STATE, "state.json")
        self.assertEqual(contract.FILE_DIAG, "diagnostics.json")
        self.assertEqual(contract.FILE_BUNDLE, "bundle.json")
        self.assertEqual(contract.WORKDIR_PREFIX, "plasma-")

    def test_sequence_names_are_zero_padded(self):
        self.assertEqual(contract.llm_req_name(0), "llm_req_000.json")
        self.assertEqual(contract.llm_resp_name(7), "llm_resp_007.json")
        self.assertEqual(contract.llm_req_name(1234), "llm_req_1234.json")

    def test_manifest_schema_version_matches_go(self):
        self.assertEqual(contract.MANIFEST_SCHEMA_VERSION, 1)


class ContractSampleTest(unittest.TestCase):
    """两侧共用同一份样例：Go 侧反序列化进真实结构体，这里断言同形状可产出。"""

    def test_request_sample_shape(self):
        with open(os.path.join(_TESTDATA, "llm_req_000.json"), "r", encoding="utf-8") as fh:
            sample = json.load(fh)
        self.assertEqual(set(sample), {"messages", "max_tokens", "temperature"})
        self.assertTrue(sample["messages"])
        for msg in sample["messages"]:
            self.assertIn("role", msg)
            self.assertIn("content", msg)

        with tempfile.TemporaryDirectory() as workdir:
            path = sampling.write_request(workdir, 0, sample["messages"])
            self.assertEqual(os.path.basename(path), "llm_req_000.json")
            with open(path, "r", encoding="utf-8") as fh:
                produced = json.load(fh)
        self.assertEqual(set(produced), set(sample))
        self.assertEqual(produced["messages"], sample["messages"])

    def test_response_sample_is_readable(self):
        with open(os.path.join(_TESTDATA, "llm_resp_000.json"), "r", encoding="utf-8") as fh:
            sample = json.load(fh)
        self.assertTrue(sampling.content_of(sample))
        self.assertEqual(sampling.finish_reason_of(sample), "stop")
        self.assertEqual(sampling.usage_of(sample).get("total_tokens"), 1500)

        spec, problems = generate._parse_spec(sampling.content_of(sample))
        self.assertEqual(problems, [])
        self.assertIsNotNone(spec)
        self.assertTrue(spec["agent_name"])
        self.assertTrue(spec["agent_design"])
        self.assertIn("agent_exit", spec["tools_used"])
        self.assertTrue(spec["task_for_agent"])


class SeedTest(unittest.TestCase):
    def test_seed_is_available_from_source_tree(self):
        seed = generate.load_seed()
        self.assertIn(generate.SEED_PLACEHOLDER, seed)
        self.assertIn("Agent Architect", seed)


if __name__ == "__main__":
    unittest.main()
