"""--server 模式：轻量 HTTP 接口，供面板/定时任务触发注册。

  POST /register   {"count": 3, "concurrency": 2, "delay": 10} → 202 + job_id
  GET  /status/<id>  → 任务状态（running / done + 统计）

单 worker：同一时刻只允许一个注册任务（防重入），并发任务返回 409。
"""
import json
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from cli import do_register


class _Registrar:
    def __init__(self, proxy_file=None, accounts_out="accounts.csv"):
        self._lock = threading.Lock()
        self._jobs: dict[str, dict] = {}
        self.proxy_file = proxy_file
        self.accounts_out = accounts_out

    def submit(self, count, concurrency, delay, upload) -> str:
        with self._lock:
            if any(j["status"] == "running" for j in self._jobs.values()):
                raise RuntimeError("已有注册任务在运行")
            job_id = uuid.uuid4().hex[:8]
        self._jobs[job_id] = {"status": "running", "started": time.time(), "result": None}
        threading.Thread(target=self._run, args=(job_id, count, concurrency, delay, upload),
                         daemon=True).start()
        return job_id

    def _run(self, job_id, count, concurrency, delay, upload):
        try:
            code = do_register(count, concurrency, delay, upload, headed=False,
                               accounts_out=self.accounts_out, proxy_file=self.proxy_file)
            self._jobs[job_id].update({"status": "done", "code": code})
        except Exception as exc:
            self._jobs[job_id].update({"status": "error", "error": str(exc)})

    def status(self, job_id):
        job = self._jobs.get(job_id)
        if not job:
            return None
        return {"status": job["status"], "code": job.get("code"),
                "error": job.get("error")}


class _Handler(BaseHTTPRequestHandler):
    registrar: _Registrar

    def _json(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        if self.path != "/register":
            self._json(404, {"error": "not_found"})
            return
        try:
            length = int(self.headers.get("Content-Length", 0))
            payload = json.loads(self.rfile.read(length) or b"{}")
        except Exception:
            self._json(400, {"error": "invalid_body"})
            return
        count = int(payload.get("count", 1))
        concurrency = int(payload.get("concurrency", 2))
        delay = int(payload.get("delay", 10))
        upload = bool(payload.get("upload", True))
        try:
            job_id = self.registrar.submit(count, concurrency, delay, upload)
        except RuntimeError as exc:
            self._json(409, {"error": "busy", "message": str(exc)})
            return
        self._json(202, {"job_id": job_id})

    def do_GET(self):
        prefix = "/status/"
        if not self.path.startswith(prefix):
            self._json(404, {"error": "not_found"})
            return
        status = self.registrar.status(self.path[len(prefix):])
        if status is None:
            self._json(404, {"error": "not_found"})
            return
        self._json(200, status)

    def log_message(self, *args):
        pass


def run_server(port: int = 8899, proxy_file=None, accounts_out="accounts.csv") -> int:
    registrar = _Registrar(proxy_file=proxy_file, accounts_out=accounts_out)
    handler = type("Handler", (_Handler,), {"registrar": registrar})
    server = ThreadingHTTPServer(("127.0.0.1", port), handler)
    print(f"🌐 注册器 HTTP 接口已启动: http://127.0.0.1:{port}（POST /register）")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    return 0
