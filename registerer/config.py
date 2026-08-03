"""Firecrawl 自动注册器 — 配置加载。

优先读取环境变量；若 registerer/.env 存在则先载入（不覆盖已有环境变量）。
"""
import os
from pathlib import Path

_PLACEHOLDER_HINTS = (
    "replace-with",
    "your-",
    ".example.com",
    "example.com",
)


def _load_dotenv() -> None:
    env_path = Path(__file__).resolve().with_name(".env")
    if not env_path.exists():
        return
    for raw_line in env_path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {"'", '"'}:
            value = value[1:-1]
        os.environ.setdefault(key, value)


def _get_str(name: str, default: str = "") -> str:
    return os.getenv(name, default).strip()


def _get_int(name: str, default: int) -> int:
    value = os.getenv(name)
    if value is None or value.strip() == "":
        return default
    try:
        return int(value)
    except ValueError:
        return default


def _get_bool(name: str, default: bool) -> bool:
    value = os.getenv(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "on"}


def is_placeholder(value: str) -> bool:
    """判断配置值是否仍是占位符（.env.example 原样拷贝未改）。"""
    normalized = (value or "").strip().lower()
    if not normalized:
        return False
    return any(hint in normalized for hint in _PLACEHOLDER_HINTS)


_load_dotenv()

TEMP_MAIL_API_URL = _get_str("TEMP_MAIL_API_URL")
TEMP_MAIL_ADMIN_PASSWORD = _get_str("TEMP_MAIL_ADMIN_PASSWORD")
TEMP_MAIL_DOMAIN = _get_str("TEMP_MAIL_DOMAIN")
TEMP_MAIL_DOMAINS = [d.strip() for d in _get_str("TEMP_MAIL_DOMAINS").split(",") if d.strip()]

REGISTER_API_URL = _get_str("REGISTER_API_URL")
REGISTER_API_TOKEN = _get_str("REGISTER_API_TOKEN")

PROXY_FILE = _get_str("PROXY_FILE")

HEADLESS = _get_bool("HEADLESS", True)
DEFAULT_COUNT = _get_int("DEFAULT_COUNT", 1)
DEFAULT_CONCURRENCY = _get_int("DEFAULT_CONCURRENCY", 2)
DEFAULT_DELAY = _get_int("DEFAULT_DELAY", 10)
EMAIL_CODE_TIMEOUT = _get_int("EMAIL_CODE_TIMEOUT", 90)
EMAIL_POLL_INTERVAL = _get_int("EMAIL_POLL_INTERVAL", 3)
API_KEY_TIMEOUT = _get_int("API_KEY_TIMEOUT", 20)

# 上传与代理是否启用
UPLOAD_ENABLED = bool(REGISTER_API_URL and REGISTER_API_TOKEN and not (
    is_placeholder(REGISTER_API_URL) or is_placeholder(REGISTER_API_TOKEN)))
PROXY_ENABLED = bool(PROXY_FILE and not is_placeholder(PROXY_FILE))
