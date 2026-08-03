"""python -m registerer 入口。

Windows 控制台默认 GBK 会炸 emoji 输出，入口处统一重配置为 UTF-8。
"""
import sys


def _force_utf8_stdio():
    for stream in (sys.stdout, sys.stderr):
        if stream and hasattr(stream, "reconfigure"):
            try:
                stream.reconfigure(encoding="utf-8", errors="replace")
            except Exception:
                pass


_force_utf8_stdio()

from cli import main

raise SystemExit(main())
