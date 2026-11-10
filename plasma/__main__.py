"""zipapp 入口：`python3 plasma.pyz <command> ...`。

zipapp 的 `-m cli:main` 会生成自己的 `__main__.py`；这里保留同名文件是为了
**源码树也能跑**（`python3 plasma/__main__.py` / unittest 的 CLI 级用例），
两种运行方式的退出码语义完全一致。
"""

import os
import sys

# 源码树运行时脚本所在目录不在 sys.path 上（zipapp 里则本来就是顶层模块）。
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import cli  # noqa: E402 - 路径先注入

if __name__ == "__main__":
    sys.exit(cli.main())
