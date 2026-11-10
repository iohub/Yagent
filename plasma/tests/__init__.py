"""stdlib unittest 的包入口。

`python3 -m unittest discover plasma/tests` 时，本文件先执行，把 zipapp 的顶层
目录（`plasma/`）放进 sys.path —— 否则 `import contract` 会找不到模块
（源码树里这些是顶层模块；zipapp 里它们本来就是 zip 根，两种布局必须都能测）。
"""

import os
import sys

_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _ROOT not in sys.path:
    sys.path.insert(0, _ROOT)
