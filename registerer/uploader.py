"""上传注册成功的 API Key 到 Go 代理（POST /api/register/keys）。"""
import requests as std_requests

from config import REGISTER_API_TOKEN, REGISTER_API_URL, UPLOAD_ENABLED


def upload_key(name: str, api_key: str) -> bool:
    """上传单个 Key。返回 True 表示代理已接收（201 或已存在 409）。"""
    if not UPLOAD_ENABLED:
        print("ℹ️  未配置上传（REGISTER_API_URL/REGISTER_API_TOKEN），跳过上传")
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
