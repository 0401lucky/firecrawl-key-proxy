"""上传注册成功的 API Key 到 Go 代理（POST /api/register/keys）。

上传前用真实 API 调用验证 Key 可用性（用户需求：不可用的 key 不进池——
实测有注册时验证通过、上传后却 401 的 key，可能是 Firecrawl 风控吊销）。
"""
import requests as std_requests

from config import API_KEY_TIMEOUT, REGISTER_API_TOKEN, REGISTER_API_URL, UPLOAD_ENABLED
from firecrawl import verify_api_key
from proxies import Proxy


def upload_key(name: str, api_key: str, proxy: Proxy | None = None) -> bool:
    """上传单个 Key：先真实验证，通过才 POST。返回 True 表示代理已接收。"""
    if not UPLOAD_ENABLED:
        print("ℹ️  未配置上传（REGISTER_API_URL/REGISTER_API_TOKEN），跳过上传")
        return False

    # 上传前真实调用验证（用户要求：存在注册时通过、上传后 401 的 key）
    verify = verify_api_key(api_key, proxy=proxy, timeout=API_KEY_TIMEOUT)
    if verify is not True:
        print(f"❌ 上传前验证未通过（{'网络不确定' if verify is None else 'Key 不可用'}），"
              f"不上传 {name}（已记入 accounts.csv）")
        return False

    try:
        resp = std_requests.post(
            f"{REGISTER_API_URL.rstrip('/')}/api/register/keys",
            json={"name": name, "api_key": api_key},
            headers={"X-Register-Token": REGISTER_API_TOKEN,
                     "Content-Type": "application/json"},
            timeout=15,
        )
    except Exception as exc:
        print(f"⚠️  上传失败（网络）: {exc}")
        return False

    if resp.status_code in (200, 201):
        print(f"✅ 已上传到代理池: {name}")
        return True
    if resp.status_code == 409:
        print("ℹ️  该 Key 已存在于池中（跳过）")
        return True
    print(f"⚠️  上传失败 HTTP {resp.status_code}: {resp.text[:120]}")
    return False
