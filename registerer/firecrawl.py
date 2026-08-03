"""Firecrawl 注册流程（移植自 tavily-key-generator 的 firecrawl_browser_solver.py）。

流程：
  1. Camoufox（反检测浏览器）打开 firecrawl.dev → Sign Up
  2. 填邮箱 + 强密码 → 提交
  3. 智能判定注册结果：blocked（风控）/ exists（已注册）/ weak_password /
     sent（已发验证邮件，继续）
  4. 轮询 Temp Mail 获取验证链接 → 浏览器访问
  5. 若跳登录页则自动登录 → 提取 fc- API Key（页面 HTML / API Keys 页）
  6. 真实调用 /v2/scrape 验证 Key 可用性

返回值统一为 RegisterResult，status 供上层决定是否换代理重试。
"""
import re
import threading
import time

import requests as std_requests
from camoufox.sync_api import Camoufox

from config import API_KEY_TIMEOUT, EMAIL_CODE_TIMEOUT, HEADLESS
from mail import wait_verification_link
from proxies import Proxy

from dataclasses import dataclass


@dataclass
class RegisterResult:
    api_key: str | None
    status: str  # ok / blocked / exists / weak_password / stalled / mail_timeout / no_key / error
    message: str = ""


_SIGNUP_RESULT_TIMEOUT = 15
_KEY_RE = re.compile(r"fc-[a-zA-Z0-9_-]{20,}")


# ---- 注册提交结果判定（移植参考项目） ----

def attach_signup_feedback_tracker(page):
    """记录注册请求的关键响应，辅助判断风控或表单错误。"""
    events = []

    def handle_response(response):
        url = (response.url or "").lower()
        if not any(token in url for token in ("signin", "signup", "auth", "clerk")):
            return
        try:
            body = response.text()
        except Exception:
            body = ""
        events.append({"url": response.url, "status": response.status, "body": body[:1500]})

    page.on("response", handle_response)
    return events


def detect_signup_result(page, signup_events):
    """根据页面与网络响应判断注册提交是否成功。"""
    current_url = (page.url or "").lower()
    if "confirm-email" in current_url or "confirm_email" in current_url:
        return "sent", ""

    snapshots = []
    try:
        snapshots.append(page.locator("body").inner_text())
    except Exception:
        pass
    try:
        snapshots.append(page.content())
    except Exception:
        pass
    snapshots.extend(event.get("body", "") for event in signup_events[-6:])
    combined = "\n".join(snapshots).lower()

    if "security check failed" in combined or "suspicious activity" in combined:
        return ("blocked", "Firecrawl 风控拦截（Security check failed / suspicious activity），"
                "当前浏览器指纹或网络 IP 被拦截。")
    if "already exists" in combined or "account already exists" in combined:
        return ("exists", "这个邮箱看起来已经注册过了。")
    if "invalid email" in combined or "email address is invalid" in combined:
        return ("invalid_email", "Firecrawl 认为这个邮箱地址无效。")
    if "password is not strong enough" in combined or "at least 12 characters" in combined:
        return ("weak_password", "Firecrawl 拒绝了密码强度（需 ≥12 位含大小写数字特殊字符）。")

    success_markers = (
        "check your email", "confirm email", "confirmation link", "verify your email",
        "verification email", "email has been sent", "we sent you an email",
        "did not receive the email", "once confirmed, you may sign in",
    )
    if any(marker in combined for marker in success_markers):
        return "sent", ""
    return "", ""


def wait_for_signup_result(page, signup_events, timeout=_SIGNUP_RESULT_TIMEOUT):
    """等待注册提交后的明确结果，避免被风控时继续盲等验证邮件。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        status, message = detect_signup_result(page, signup_events)
        if status:
            return status, message
        time.sleep(1)

    current_url = (page.url or "").lower()
    if "confirm-email" in current_url or "confirm_email" in current_url:
        return "sent", ""
    if "view=signup" in current_url or current_url.rstrip("/").endswith("/signin"):
        return ("stalled", "提交后页面仍停留在注册页，Firecrawl 没有确认已发送验证邮件。")
    return "", ""


# ---- 表单与提取（移植参考项目） ----

def fill_first_input(page, selectors, value):
    for selector in selectors:
        if page.query_selector(selector):
            page.fill(selector, value)
            return selector
    return None


def submit_form(page, input_selector=None):
    button_selectors = [
        'button[type="submit"]', 'button:has-text("Sign up")',
        'button:has-text("Continue")', 'button:has-text("Register")',
    ]
    for selector in button_selectors:
        if page.query_selector(selector):
            try:
                page.click(selector, timeout=3000)
                return True
            except Exception:
                continue
    if input_selector and page.query_selector(input_selector):
        try:
            page.press(input_selector, "Enter")
            return True
        except Exception:
            return False
    return False


def extract_api_key_from_page(page):
    """从 API Keys 页面提取 fc- 开头的 API Key。"""
    try:
        time.sleep(3)
        selectors = [
            'code:has-text("fc-")', '[data-testid="api-key"]', '.api-key',
            'input[value^="fc-"]', 'span:has-text("fc-")',
        ]
        for selector in selectors:
            for element in page.query_selector_all(selector):
                text = (element.inner_text() or element.get_attribute("value") or "")
                match = _KEY_RE.search(text)
                if match:
                    return match.group(0)
        match = _KEY_RE.search(page.content())
        if match:
            return match.group(0)
        return None
    except Exception:
        return None


def create_api_key(page):
    """在 Dashboard 中创建新的 API Key（页面未预置时兜底）。"""
    try:
        for selector in ('button:has-text("Create")', 'button:has-text("New API Key")',
                         'button:has-text("Generate")', '[data-testid="create-api-key"]'):
            if page.query_selector(selector):
                page.click(selector)
                time.sleep(2)
                break
        name_input = page.query_selector('input[name="name"], input[placeholder*="name" i]')
        if name_input:
            page.fill('input[name="name"], input[placeholder*="name" i]', "auto-generated-key")
            time.sleep(1)
        for selector in ('button:has-text("Create")', 'button:has-text("Generate")',
                         'button:has-text("Confirm")', 'button[type="submit"]'):
            if page.query_selector(selector):
                page.click(selector)
                time.sleep(3)
                break
        return True
    except Exception:
        return False


def verify_api_key(api_key: str, proxy: Proxy | None = None, timeout: int = API_KEY_TIMEOUT):
    """真实调用 Firecrawl v2 API 验证 Key。

    返回：True 可用 / False 不可用（401/403/402 等终态）/
          None 网络层异常（TLS/连接/超时，Key 存疑但保留）。
    """
    transient = (std_requests.exceptions.SSLError,
                 std_requests.exceptions.ConnectionError,
                 std_requests.exceptions.Timeout)
    last_error = None
    proxies = None
    if proxy and proxy.server:
        proxies = {"http": proxy.server, "https": proxy.server}

    for attempt in range(1, 4):
        try:
            response = std_requests.post(
                "https://api.firecrawl.dev/v2/scrape",
                json={"url": "https://example.com"},
                headers={"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"},
                timeout=timeout,
                proxies=proxies,
            )
            break
        except transient as exc:
            last_error = exc
            if attempt < 3:
                print(f"⚠️  Key 验证网络异常，重试 ({attempt}/3): {exc}")
                time.sleep(attempt)
                continue
            print(f"⚠️  Key 验证网络异常（TLS/连接问题，不代表 Key 无效）: {exc}")
            return None
        except Exception as exc:
            print(f"❌ Key 验证异常: {exc}")
            return False
    else:
        print(f"⚠️  Key 验证未获得响应: {last_error}")
        return None

    if response.status_code == 200:
        print("✅ API Key 真实调用验证通过")
        return True
    preview = response.text.strip().replace("\n", " ")[:160]
    print(f"❌ API Key 验证失败: HTTP {response.status_code} 响应: {preview}")
    return False


# ---- 主流程 ----

def register_with_browser(email: str, password: str, proxy: Proxy | None = None,
                          headed: bool = False) -> RegisterResult:
    """用 Camoufox 注册一个 Firecrawl 账号，返回 RegisterResult。"""
    print(f"🌐 注册 Firecrawl: {email}  代理: {proxy.server if proxy else '直连'}")
    proxy_dict = proxy.camofox_dict() if proxy else None

    try:
        with Camoufox(headless=not headed and HEADLESS, proxy=proxy_dict) as browser:
            page = browser.new_page()
            signup_events = attach_signup_feedback_tracker(page)
            verify_url = ""

            # 1. 进入注册页
            page.goto("https://firecrawl.dev/", wait_until="networkidle", timeout=30000)
            time.sleep(2)
            for selector in ('a:has-text("Sign up")', 'a:has-text("Sign Up")',
                             'button:has-text("Sign up")', 'a[href*="signup"]',
                             'a[href*="register"]'):
                if page.query_selector(selector):
                    page.click(selector)
                    time.sleep(3)
                    break

            # 2. 填表
            email_selector = fill_first_input(
                page, ['input[name="email"]', 'input[type="email"]',
                       'input[placeholder*="email" i]'], email)
            if not email_selector:
                return RegisterResult(None, "error", "未找到邮箱输入框")
            time.sleep(1)
            if not fill_first_input(page, ['input[name="password"]', 'input[type="password"]'], password):
                return RegisterResult(None, "error", "未找到密码输入框")
            time.sleep(1)

            # 3. 提交并判定
            submit_form(page, email_selector)
            status, message = wait_for_signup_result(page, signup_events)
            # Firecrawl 页面反馈可能慢于判定窗口：stalled 时先等一会验证邮件，
            # 收到链接说明注册实际已成功，避免不必要的重试与重复提交。
            if status in ("stalled", ""):
                early_link = wait_verification_link(email, timeout=25)
                if early_link:
                    status, message, verify_url = "sent", "", early_link
            if status != "sent":
                if message:
                    print(f"❌ {message}")
                if status in {"blocked", "stalled"} and not headed and HEADLESS:
                    print("💡 建议：--headed 前台重试，或更换更干净的代理后重试。")
                return RegisterResult(None, status if status else "stalled", message)

            # 4. 等验证链接（已提前拿到则跳过）
            if not verify_url:
                verify_url = wait_verification_link(email, timeout=EMAIL_CODE_TIMEOUT)
            if not verify_url:
                return RegisterResult(None, "mail_timeout", "未收到验证邮件")

            # 5. 访问验证链接
            page.goto(verify_url, wait_until="networkidle", timeout=60000)
            time.sleep(5)

            # 6. 若跳登录页则自动登录
            current_url = (page.url or "").lower()
            if "login" in current_url or "signin" in current_url:
                print("🔐 验证后需要登录，自动填表...")
                fill_first_input(page, ['input[name="email"]', 'input[type="email"]'], email)
                time.sleep(1)
                fill_first_input(page, ['input[name="password"]', 'input[type="password"]'], password)
                time.sleep(1)
                submit_form(page)
                time.sleep(5)

            # 7. 提取 API Key（当前页 → API Keys 页 → 创建）
            api_key = extract_api_key_from_page(page)
            if api_key:
                print(f"✅ 当前页面已拿到 API Key: {api_key[:20]}...")
            else:
                print("ℹ️  当前页面未拿到 Key，尝试导航到 API Keys 页...")
                found = False
                for selector in ('a:has-text("API Keys")', 'a[href*="api-key"]',
                                 'a[href*="apikey"]', 'a[href*="keys"]',
                                 'button:has-text("API Keys")'):
                    if page.query_selector(selector):
                        page.click(selector)
                        time.sleep(3)
                        found = True
                        break
                if not found:
                    for url in ("https://www.firecrawl.dev/app/api-keys",
                                "https://www.firecrawl.dev/app/settings",
                                "https://www.firecrawl.dev/app",
                                "https://firecrawl.dev/dashboard/api-keys",
                                "https://firecrawl.dev/api-keys",
                                "https://app.firecrawl.dev/api-keys"):
                        try:
                            page.goto(url, wait_until="networkidle", timeout=15000)
                            time.sleep(3)
                            if "api" in (page.url or "").lower() and "key" in (page.url or "").lower():
                                found = True
                                break
                        except Exception:
                            continue
                api_key = extract_api_key_from_page(page)
                if not api_key:
                    print("💡 尝试创建新 API Key...")
                    if create_api_key(page):
                        api_key = extract_api_key_from_page(page)

            if not api_key:
                return RegisterResult(None, "no_key", "无法获取 API Key")

            # 8. 验证 Key
            verify = verify_api_key(api_key, proxy=proxy)
            if verify is False:
                return RegisterResult(api_key, "ok",
                                      "Key 已提取但真实调用验证失败（可能未激活）")
            return RegisterResult(api_key, "ok", "")

    except Exception as exc:
        import traceback
        traceback.print_exc()
        return RegisterResult(None, "error", str(exc))
