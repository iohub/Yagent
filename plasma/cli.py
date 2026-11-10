"""zipapp 的命令行入口（stdlib argparse，退出码统一在 contract.py）。

命令表（§7.3）：

    在线   generate [--resume] [--fix] --workdir W
    离线   author-fixtures --bundle B [--out PATH]
           explain --in report.json [--out PATH]
    未实现 evolve / judge / propose（P11/P12）—— 注册但立即 exit 2 并说明，
           不要静默"命令不存在"：那样调用方分不清"没实现"与"拼错了"。

用法错由 argparse 自己 exit 2（与 `EXIT_USAGE` 同值）；业务失败一律 exit 1，
并且**只写 stderr** —— stdout 是数据通道（`explain`/`author-fixtures` 各发一份 JSON，
`generate` 什么都不发）。
"""

import argparse
import sys

import author_fixtures
import contract
import explain
import generate


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

    for name, stage in (("evolve", "P11"), ("judge", "P11"), ("propose", "P12")):
        stub = sub.add_parser(name, help="未实现（%s）" % stage)
        stub.add_argument("--in", dest="in_path", default="", help=argparse.SUPPRESS)
        stub.add_argument("--out", dest="out_path", default="", help=argparse.SUPPRESS)

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
        if args.command in ("evolve", "judge", "propose"):
            stage = "P11" if args.command in ("evolve", "judge") else "P12"
            print("plasma %s: 未实现（计划在 %s 落地）" % (args.command, stage), file=sys.stderr)
            return contract.EXIT_USAGE
    except KeyboardInterrupt:
        print("plasma: 被中断", file=sys.stderr)
        return contract.EXIT_TIMEOUT
    except Exception as exc:  # noqa: BLE001 - 入口必须把任何异常翻成可读的 exit 1
        print("plasma %s: 内部错误：%s: %s" % (args.command, type(exc).__name__, exc), file=sys.stderr)
        return contract.EXIT_FAILED

    print("plasma: 未知子命令 %r" % args.command, file=sys.stderr)
    return contract.EXIT_USAGE
