"""代理池：静态列表文件加载 + round-robin 轮换。

文件格式：每行一条代理，# 开头为注释，空行忽略。
支持 scheme：
  http://user:pass@host:port
  socks5://host:port
  host:port（无 scheme 时按 http 处理）

线程安全：next() 用锁保护游标；mark_bad() 把失效代理移入黑名单（进程内），
黑名单可被外部轮换周期清空（避免短暂故障的代理永久失效）。
"""
import threading
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlparse


@dataclass
class Proxy:
    server: str      # 传给 Camoufox 的 server 值，如 http://host:port
    username: str    # 可能为空
    password: str    # 可能为空

    def camofox_dict(self) -> dict | None:
        """转 Camoufox/Playwright 的 proxy 参数；无凭据时省略 username/password。"""
        if not self.server:
            return None
        proxy = {"server": self.server}
        if self.username:
            proxy["username"] = self.username
            proxy["password"] = self.password
        return proxy


class ProxyPool:
    def __init__(self, proxies: list[Proxy]):
        self._all = proxies
        self._cursor = 0
        self._bad: set[int] = set()
        self._lock = threading.Lock()

    @classmethod
    def from_file(cls, path: str) -> "ProxyPool":
        proxies = []
        if path:
            p = Path(path)
            if p.exists():
                for line in p.read_text(encoding="utf-8").splitlines():
                    parsed = _parse_line(line)
                    if parsed:
                        proxies.append(parsed)
        return cls(proxies)

    @property
    def count(self) -> int:
        return len(self._all)

    def next(self) -> Proxy | None:
        """round-robin 返回下一个未列入黑名单的代理；无可用代理返回 None。"""
        with self._lock:
            if not self._all:
                return None
            for _ in range(len(self._all)):
                idx = self._cursor % len(self._all)
                self._cursor += 1
                if idx not in self._bad:
                    return self._all[idx]
            return None

    def mark_bad(self, server: str) -> None:
        """把代理移入黑名单（按 server 值匹配）。"""
        with self._lock:
            for idx, proxy in enumerate(self._all):
                if proxy.server == server:
                    self._bad.add(idx)
                    break

    def reset_bad(self) -> None:
        """清空黑名单（外部定时调用，给短暂故障的代理机会）。"""
        with self._lock:
            self._bad.clear()


def _parse_line(line: str) -> Proxy | None:
    line = line.strip()
    if not line or line.startswith("#"):
        return None
    if "://" not in line:
        line = "http://" + line

    parsed = urlparse(line)
    if not parsed.hostname or not parsed.port:
        return None

    server = f"{parsed.scheme}://{parsed.hostname}:{parsed.port}"
    return Proxy(server=server, username=parsed.username or "", password=parsed.password or "")
