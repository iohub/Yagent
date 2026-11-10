"""zipapp 的命令行入口（stdlib argparse，退出码统一在 contract.py）。

命令表（§7.3）：

    在线   generate [--resume] [--fix] --workdir W
    离线   author-fixtures --bundle B [--out PATH]
           explain --in report.json [--out PATH]
           judge --in outcomes.json [--out PATH]
           evolve --in telemetry.jsonl --store STORE --out DIR [--max-candidates N]
           propose --in input.json --out PROPOSAL_DIR

`propose` 是"内建 Agent 提案"（§6.9）：产出 JSON + Markdown 两个文件，**永不自动
apply**，也绝不写任何 `*.prompt.md`。它的输入由 Go 侧装配（prompt 全文 + 遥测摘要），
Python 只做确定性启发式 —— 不碰工作区、不连 provider、不持密钥。

用法错由 argparse 自己 exit 2（与 `EXIT_USAGE` 同值）；业务失败一律 exit 1，
并且**只写 stderr** —— stdout 是数据通道（`explain`/`author-fixtures`/`judge`/`evolve`
各发一份 JSON，`generate` 什么都不发）。
"""

import argparse
import sys

import author_fixtures
import contract
import evolve
import explain
import generate
import judge
import propose


def build_parser():
    parser = argparse.ArgumentParser(
        prog="plasma.pyz",
        description="yagent plasma 的 Python 侧：在线生成策略 + 离线出题/解释。",
    )
    sub = parser.add_subparsers(dest="command", metavar="<command>")

    gen = sub.add_parser("generate", help="在线生成：写采样请求（exit 4）或产出 bundle（exit 0）")
    gen.add_argument("--workdir", required=True, help="Go 侧建好的 workdir（含 req.json/manifest.json）")
    gen.add_argument("--resume", action="store_true", help="从 state.json 继续（Go respawn 时传）")
    gen.add_argument("--fix", action="store_true", help="带 diagnostics.json 重新生成（编译失败后传）")

    fixtures = sub.add_parser("author-fixtures", help="离线出题：把 bundle 变成行为夹具草稿")
    fixtures.add_argument("--bundle", required=True, help="bundle.json 路径（最小 spec 或完整 IR）")
    fixtures.add_argument("--out", default="", help="输出文件或目录（缺省只打印 JSON）")

    exp = sub.add_parser("explain", help="离线解释：把诊断报告渲染成人能照着改的文本")
    exp.add_argument("--in", dest="in_path", required=True, help="diagnostics.json / compile --format json 的产物")
    exp.add_argument("--out", default="", help="写纯文本到该路径（缺省只打印 JSON 信封）")

    jud = sub.add_parser("judge", help="离线评分：把 5a+5b 的观测折成 0-100 分与判决")
    jud.add_argument("--in", dest="in_path", required=True, help="outcomes.json（Go 侧写出的夹具观测）")
    jud.add_argument("--out", default="", help="verdict JSON 的输出路径（缺省只打印到 stdout）")

    evo = sub.add_parser("evolve", help="离线进化：遥测 → 适应度 → 确定性变异候选")
    evo.add_argument("--in", dest="in_path", required=True, help="telemetry.jsonl")
    evo.add_argument("--store", dest="store_dir", required=True, help="plasma store 根（取父 bundle 原文）")
    evo.add_argument("--out", dest="out_dir", required=True, help="输出目录（必须已含 policy.json）")
    evo.add_argument("--max-candidates", dest="max_candidates", type=int, default=3,
                     help="最多产出几个候选（默认 3）")

    prop = sub.add_parser("propose", help="离线提案：给内建 Agent 出一份 prompt diff 提案（永不自动 apply）")
    prop.add_argument("--in", dest="in_path", required=True,
                      help="Go 侧写好的输入 JSON（agent + prompt 全文 + 遥测摘要）")
    prop.add_argument("--out", dest="out_path", required=True,
                      help="提案目录：写 <agent>-<ts>.json 与 .md（不碰任何 *.prompt.md）")

    return parser


def main(argv=None):
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:
        # argparse 用 SystemExit 表达 --help（0）与用法错（2）。入口返回同一个码，
        # 而不是让异常穿透：调用方（测试/嵌入方）要的是返回值，不是栈。
        return int(exc.code or 0)

    if args.command is None:
        parser.print_help(sys.stderr)
        return contract.EXIT_USAGE

    try:
        if args.command == "generate":
            return generate.run(args.workdir, resume=args.resume, fix=args.fix)
        if args.command == "author-fixtures":
            return author_fixtures.run(args.bundle, args.out)
        if args.command == "explain":
            return explain.run(args.in_path, args.out)
        if args.command == "judge":
            return judge.run(args.in_path, args.out)
        if args.command == "evolve":
            return evolve.run(args.in_path, args.out_dir, args.store_dir,
                              max_candidates=args.max_candidates)
        if args.command == "propose":
            return propose.run(args.in_path, args.out_path)
    except KeyboardInterrupt:
        print("plasma: 被中断", file=sys.stderr)
        return contract.EXIT_TIMEOUT
    except Exception as exc:  # noqa: BLE001 - 入口必须把任何异常翻成可读的 exit 1
        print("plasma %s: 内部错误：%s: %s" % (args.command, type(exc).__name__, exc), file=sys.stderr)
        return contract.EXIT_FAILED

    print("plasma: 未知子命令 %r" % args.command, file=sys.stderr)
    return contract.EXIT_USAGE
