"""代理池：静态列表文件 + 订阅 URL 拉取 + round-robin 轮换。

代理来源（可同时配置）：
1. 静态文件：每行一条代理，# 开头为注释，空行忽略。
2. 订阅 URL（PROXY_SUBSCRIBE_URLS）：proxyscrape / webshare 等订阅，
   响应为每行 ip:port 或 ip:port:user:pass 的文本列表。

支持 scheme：http://user:pass@host:port / socks5://host:port / 无 scheme 按 http。
线程安全：next() 用锁保护游标；mark_bad() 把失效代理移入黑名单（进程内）。
"""
import threading
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlparse

import requests as std_requests


@dataclass
class Proxy:
    server: str      # 传给 Camoufox 的 server 值，如 http://host:port
    username: str    # 可能为空
    password: str    # 可能为空

    def camofox_dict(self) -> dict | None:
        """转 Camoufox/Playwright 的 proxy 参数。

        注意：空 username 必须省略该键（Playwright 传 username="" 会
        PROXY_GATEWAY_TIMEOUT），只传 password 时 resin 等代理正常认证。
        """
        if not self.server:
            return None
        proxy = {"server": self.server}
        if self.username:
            proxy["username"] = self.username
            proxy["password"] = self.password
        elif self.password:
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

    @classmethod
    def from_file_and_subscriptions(cls, path: str, subscribe_urls: list[str]) -> "ProxyPool":
        """文件代理 + 订阅 URL 拉取合并（去重）。"""
        pool = cls.from_file(path)
        seen = {p.server for p in pool._all}
        for url in subscribe_urls:
            fetched = fetch_subscription(url)
            added = 0
            for proxy in fetched:
                if proxy.server not in seen:
                    pool._all.append(proxy)
                    seen.add(proxy.server)
                    added += 1
            print(f"🔄 订阅 {url[:60]}... 拉到 {len(fetched)} 条（新增 {added}）")
        return pool

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


def _parse_subscription_line(line: str) -> Proxy | None:
    """解析订阅行：ip:port 或 ip:port:user:pass（proxyscrape/webshare 格式）。"""
    line = line.strip()
    if not line or line.startswith("#"):
        return None
    parts = line.split(":")
    if len(parts) == 2:
        return Proxy(server=f"http://{parts[0]}:{parts[1]}", username="", password="")
    if len(parts) == 4:
        return Proxy(server=f"http://{parts[0]}:{parts[1]}",
                     username=parts[2], password=parts[3])
    return None


def fetch_subscription(url: str) -> list[Proxy]:
    """从订阅 URL 拉取代理列表（每行 ip:port 或 ip:port:user:pass）。"""
    try:
        resp = std_requests.get(url, timeout=25, headers={"User-Agent": "curl/8.0"})
        resp.raise_for_status()
    except Exception as exc:
        print(f"⚠️  订阅拉取失败 {url[:70]}: {exc}")
        return []

    proxies = []
    for line in resp.text.splitlines():
        parsed = _parse_subscription_line(line)
        if parsed:
            proxies.append(parsed)
    return proxies
