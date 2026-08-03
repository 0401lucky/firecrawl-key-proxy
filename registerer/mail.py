"""Temp Mail（dreamhunter2333/cloudflare_temp_email）邮箱 provider。

两个职责：
1. create_email() —— 通过 admin API 创建邮箱，启用随机子域名
   （地址形如 fc-xxxx@<8位随机子域名>.lucky0506.shop，需通配 MX 生效）。
2. wait_verification_link() —— 轮询 admin 邮件 API，解析 raw RFC822
   并提取注册服务的邮箱验证链接。

认证注意：本模块全部走 admin 认证（x-admin-auth），不使用地址 JWT
（/api/mails 的 Authorization: Bearer）——admin 侧按 address 过滤即可，
免去维护每个邮箱的 JWT。
"""
import html
import random
import re
import string
import threading
import time
from email import policy
from email.parser import BytesParser

import requests as std_requests

from config import (
    EMAIL_CODE_TIMEOUT,
    EMAIL_POLL_INTERVAL,
    TEMP_MAIL_ADMIN_PASSWORD,
    TEMP_MAIL_API_URL,
    TEMP_MAIL_DOMAIN,
    TEMP_MAIL_DOMAINS,
)

_HEADERS = {"x-admin-auth": TEMP_MAIL_ADMIN_PASSWORD, "Content-Type": "application/json"}

# 多域名轮询游标：默认从 /open_api/settings 自动拉取（randomSubdomainDomains），
# 拉取失败时回退 TEMP_MAIL_DOMAINS / TEMP_MAIL_DOMAIN。
_domain_lock = threading.Lock()
_domain_cursor = 0
_domains_cache: list[str] | None = None
_domains_cache_time = 0.0
_DOMAINS_TTL = 300  # 秒


def fetch_domains() -> list[str]:
    """从 /open_api/settings 拉取支持随机子域名的域名列表（无需认证）。"""
    try:
        resp = std_requests.get(
            f"{TEMP_MAIL_API_URL.rstrip('/')}/open_api/settings", timeout=10)
        resp.raise_for_status()
        data = resp.json()
        domains = data.get("randomSubdomainDomains") or data.get("domains") or []
        return [d for d in domains if isinstance(d, str) and d.strip()]
    except Exception as exc:
        print(f"⚠️  域名列表拉取失败（{exc}），使用本地配置")
        return []


def _pick_domain() -> str:
    """round-robin 选择收件域名：显式配置优先，否则自动拉取（缓存 5 分钟）。"""
    global _domain_cursor, _domains_cache, _domains_cache_time
    configured = TEMP_MAIL_DOMAINS or ([TEMP_MAIL_DOMAIN] if TEMP_MAIL_DOMAIN else [])

    if not configured:
        now = time.time()
        if _domains_cache is None or now - _domains_cache_time > _DOMAINS_TTL:
            fetched = fetch_domains()
            _domains_cache = fetched or []
            _domains_cache_time = now
        domains = _domains_cache
    else:
        domains = configured

    if not domains:
        raise RuntimeError("无法获取域名列表（检查 TEMP_MAIL_API_URL）且未配置 TEMP_MAIL_DOMAIN")
    with _domain_lock:
        domain = domains[_domain_cursor % len(domains)]
        _domain_cursor += 1
    return domain

# 验证链接的启发式优先级（移植自 tavily-key-generator，Firecrawl 是 Clerk 体系）。
_PRIMARY_LINK_HINTS = ("verif", "confirm", "magic", "auth", "callback", "signin", "signup")
_PRIMARY_HOST_HINTS = ("firecrawl", "clerk", "stytch", "auth", "login")
_MESSAGE_HINTS = ("verify", "verification", "confirm", "magic link", "sign in", "firecrawl")


def rand_str(n: int = 8) -> str:
    return "".join(random.choices(string.ascii_lowercase + string.digits, k=n))


def _strong_password() -> str:
    """12 位强密码：大小写 + 数字 + 特殊字符（Firecrawl 最低要求）。"""
    return f"Tv{rand_str(6)}{random.randint(100, 999)}!A"


def create_email() -> tuple[str, str]:
    """创建带随机子域名的邮箱地址，返回 (address, password)。域名多选时轮询。"""
    password = _strong_password()
    name = f"fc-{rand_str()}"
    domain = _pick_domain()

    for attempt in range(1, 6):
        resp = std_requests.post(
            f"{TEMP_MAIL_API_URL.rstrip('/')}/admin/new_address",
            json={
                "enablePrefix": True,
                "name": name,
                "domain": domain,
                "enableRandomSubdomain": True,
            },
            headers=_HEADERS,
            timeout=15,
        )
        if resp.status_code == 200:
            data = resp.json()
            address = data.get("address") or ""
            if address:
                print(f"✅ 邮箱已创建: {address}")
                return address, password
            raise RuntimeError(f"创建邮箱成功但响应缺少 address: {data}")

        if resp.status_code in (409, 422):
            print(f"⚠️  邮箱创建冲突（{resp.status_code}），换前缀重试 {attempt}/5")
            name = f"fc-{rand_str()}"
            continue
        raise RuntimeError(
            f"创建邮箱失败 HTTP {resp.status_code}: {resp.text[:200]}")

    raise RuntimeError("创建邮箱失败：随机前缀连续冲突")


# ---- 邮件轮询与解析 ----

def _iter_messages_from_payload(payload) -> list[dict]:
    """把 /admin/mails 响应解析为易读字段的邮件列表。

    兼容三种返回结构：{"results": [...]} / {"mails": [...]} / 直接数组。
    raw 为 RFC822 原始内容（可能 gzip 已由服务端解压）。
    """
    if isinstance(payload, dict):
        items = payload.get("results")
        if items is None:
            items = payload.get("mails")
    else:
        items = payload
    if not isinstance(items, list):
        return []

    messages = []
    for item in items:
        raw = item.get("raw") or item.get("source") or ""
        if not raw:
            continue
        try:
            msg = BytesParser(policy=policy.default).parsebytes(
                raw.encode("utf-8", errors="replace"))
        except Exception:
            continue
        messages.append({
            "id": item.get("id"),
            "subject": str(msg.get("Subject", "")),
            "from": str(msg.get("From", "")),
            "html": _msg_html(msg),
            "text": _msg_text(msg),
        })
    return messages


def _iter_messages(email: str):
    """拉取指定地址的邮件列表（admin API，raw RFC822 解析为易读字段）。"""
    resp = std_requests.get(
        f"{TEMP_MAIL_API_URL.rstrip('/')}/admin/mails",
        params={"limit": 50, "offset": 0, "address": email},
        headers=_HEADERS,
        timeout=10,
    )
    resp.raise_for_status()
    yield from _iter_messages_from_payload(resp.json())


def _msg_text(msg) -> str:
    parts = []
    for part in msg.walk():
        ctype = part.get_content_type()
        if ctype == "text/plain" and not part.is_attachment():
            try:
                parts.append(part.get_content())
            except Exception:
                continue
    return "\n".join(parts)


def _msg_html(msg) -> str:
    parts = []
    for part in msg.walk():
        ctype = part.get_content_type()
        if ctype == "text/html" and not part.is_attachment():
            try:
                parts.append(part.get_content())
            except Exception:
                continue
    return "\n".join(parts)


def _extract_verification_link(message) -> str | None:
    """从一封邮件中提取验证链接（移植 tavily-key-generator 启发式）。"""
    subject = (message.get("subject") or "").lower()
    sender = (message.get("from") or "").lower()
    content = f"{message.get('html') or ''} {message.get('text') or ''}"
    urls = [
        html.unescape(raw).rstrip(").,;")
        for raw in re.findall(r'https://[^\s<>"\']+', content, re.IGNORECASE)
    ]

    for url in urls:
        lowered = url.lower()
        if (any(t in lowered for t in _PRIMARY_LINK_HINTS)
                and any(h in lowered for h in _PRIMARY_HOST_HINTS)):
            return url

    combined = f"{sender} {subject} {content[:4000]}".lower()
    if not any(token in combined for token in _MESSAGE_HINTS):
        return None

    for url in urls:
        lowered = url.lower()
        if any(t in lowered for t in _PRIMARY_LINK_HINTS):
            return url
    return None


def wait_verification_link(email: str, timeout: int = EMAIL_CODE_TIMEOUT) -> str | None:
    """轮询邮箱直到出现验证链接，超时返回 None。"""
    print(f"📧 等待验证邮件（最多 {timeout} 秒）...", end="", flush=True)
    deadline = time.time() + timeout
    seen_ids: set = set()

    while time.time() < deadline:
        try:
            for message in _iter_messages(email):
                mid = message.get("id")
                if mid is not None and mid in seen_ids:
                    continue
                if mid is not None:
                    seen_ids.add(mid)
                link = _extract_verification_link(message)
                if link:
                    print(f"\n✅ 收到验证链接: {link[:60]}...")
                    return link
        except Exception as exc:
            print(f"\n⚠️  检查邮件失败: {exc}")

        time.sleep(EMAIL_POLL_INTERVAL)
        print(".", end="", flush=True)

    print("\n❌ 验证邮件超时（检查通配 MX 记录与 Email Routing 是否覆盖随机子域名）")
    return None
