"""Firecrawl 自动注册器 CLI 入口。

用法：
  python -m registerer --count 3 --concurrency 2
  python -m registerer --count 1 --headed          # 前台浏览器（排查风控）
  python -m registerer --server --port 8899        # HTTP 接口模式

重试策略：单次注册最多 3 次尝试；blocked/stalled/error 换代理重试，
exists/weak_password/mail_timeout 不重试（邮箱或邮件链路问题，重试无意义）。
"""
import argparse
import random
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed

from accounts import AccountSaver
from config import (
    DEFAULT_CONCURRENCY,
    DEFAULT_COUNT,
    DEFAULT_DELAY,
    HEADLESS,
    PROXY_ENABLED,
    PROXY_FILE,
    UPLOAD_ENABLED,
)
from firecrawl import RegisterResult, register_with_browser
from mail import create_email
from proxies import ProxyPool
from uploader import upload_key

_MAX_ATTEMPTS = 3
_RETRYABLE_STATUSES = ("blocked", "stalled", "error")

_attempts_lock = threading.Lock()
_proxy_hits: dict[str, int] = {}


def register_one(index: int, total: int, saver: AccountSaver, upload: bool,
                 pool: ProxyPool | None, headed: bool) -> RegisterResult:
    """注册一个账号：创建邮箱 → （换代理）重试注册 → 验证 → 上传 → 落盘。"""
    print(f"\n=== [{index}/{total}] 开始注册 ===")
    email, password = create_email()

    proxy = None
    result: RegisterResult | None = None
    for attempt in range(1, _MAX_ATTEMPTS + 1):
        proxy = pool.next() if pool else None
        if not proxy and pool:
            print("⚠️  代理池无可用的代理，本次尝试直连")
        result = register_with_browser(email, password, proxy=proxy, headed=headed)

        if result.status == "ok":
            break
        if result.status in _RETRYABLE_STATUSES and attempt < _MAX_ATTEMPTS:
            if pool and proxy:
                with _attempts_lock:
                    _proxy_hits[proxy.server] = _proxy_hits.get(proxy.server, 0) + 1
                if result.status == "error":
                    pool.mark_bad(proxy.server)
            print(f"↻ 状态={result.status}，换代理重试（{attempt + 1}/{_MAX_ATTEMPTS}）")
            time.sleep(3)
            continue
        break

    if result is None:
        result = RegisterResult(None, "error", "未知错误")

    if result.api_key:
        saver.save(email, password, result.api_key, result.status)
        if upload and result.status == "ok":
            name = f"auto-{email.split('@')[0]}"
            upload_key(name, result.api_key, proxy=proxy)
    else:
        saver.save(email, password, "", result.status)
    print(f"=== [{index}/{total}] 结束：{result.status} ===")
    return result


def do_register(count: int, concurrency: int, delay: int, upload: bool,
                headed: bool, accounts_out: str, proxy_file: str | None):
    pool = ProxyPool.from_file(proxy_file or PROXY_FILE) if (proxy_file or PROXY_ENABLED) else None
    if pool and pool.count == 0:
        print("⚠️  代理文件未读到可用代理（检查路径与格式），将直连注册")
    elif pool:
        print(f"🔄 代理池就绪：{pool.count} 条代理")

    saver = AccountSaver(accounts_out)
    print(f"🚀 开始注册：数量={count} 并发={concurrency} 间隔={delay}s "
          f"上传={'开' if upload else '关'} 代理={'开' if pool else '关'}")

    results = {"ok": 0}
    if concurrency <= 1 or count <= 1:
        for i in range(1, count + 1):
            r = register_one(i, count, saver, upload, pool, headed)
            results[r.status] = results.get(r.status, 0) + 1
            if i < count and delay > 0:
                time.sleep(delay)
    else:
        with ThreadPoolExecutor(max_workers=concurrency) as executor:
            futures = [executor.submit(register_one, i, count, saver, upload, pool, headed)
                       for i in range(1, count + 1)]
            for future in as_completed(futures):
                r = future.result()
                results[r.status] = results.get(r.status, 0) + 1
                time.sleep(delay)

    ok = results.get("ok", 0)
    print(f"\n🎉 全部完成：成功 {ok}/{count}  明细: {results}")
    if _proxy_hits:
        bad = sorted(_proxy_hits.items(), key=lambda kv: -kv[1])
        print("代理使用统计（失败命中次数）:", ", ".join(f"{k}({v})" for k, v in bad[:10]))
    return 0 if ok > 0 else 1


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Firecrawl 自动注册器")
    parser.add_argument("--count", type=int, default=DEFAULT_COUNT, help="注册数量")
    parser.add_argument("--concurrency", type=int, default=DEFAULT_CONCURRENCY, help="并发数")
    parser.add_argument("--delay", type=int, default=DEFAULT_DELAY, help="每轮间隔（秒）")
    parser.add_argument("--headed", action="store_true", help="前台浏览器（调试风控）")
    parser.add_argument("--no-upload", action="store_true", help="不上传 Key 到代理池")
    parser.add_argument("--accounts-out", default="accounts.csv", help="账号 CSV 输出路径")
    parser.add_argument("--proxy-file", default=None, help="代理列表文件（覆盖 .env）")
    parser.add_argument("--server", action="store_true", help="HTTP 接口模式")
    parser.add_argument("--port", type=int, default=8899, help="HTTP 模式监听端口")
    args = parser.parse_args(argv)

    if args.server:
        from server import run_server
        return run_server(port=args.port, proxy_file=args.proxy_file,
                          accounts_out=args.accounts_out)

    if not args.no_upload and not UPLOAD_ENABLED:
        print("ℹ️  未配置上传目标（REGISTER_API_URL/REGISTER_API_TOKEN），本次仅本地保存")
        print("   如需上传，请在 registerer/.env 中配置。\n")

    return do_register(
        count=args.count,
        concurrency=args.concurrency,
        delay=args.delay,
        upload=not args.no_upload,
        headed=args.headed,
        accounts_out=args.accounts_out,
        proxy_file=args.proxy_file,
    )


if __name__ == "__main__":
    raise SystemExit(main())
