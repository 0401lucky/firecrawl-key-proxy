"""Resin 代理池 SSH 隧道：本地端口转发到服务器 resin（2260）。

背景：resin 公网 2260 端口被云安全组拦截（22 可通），用 SSH 隧道
将本地 127.0.0.1:22260 转发到服务器 127.0.0.1:2260。

用法：
  python resin_tunnel.py           # 前台运行（Ctrl+C 退出）
  cmd /c "start /b python resin_tunnel.py > tunnel.log 2>&1"

长期方案：在云控制台放行 2260 后无需隧道。
"""
import select
import socket
import socketserver
import sys
import threading
import time

import paramiko

HOST = "43.159.170.225"
USER = "ubuntu"
PASSWORD = "BtY+[Iacz0GL=6)@ea"
LOCAL_PORT = 22260
REMOTE_HOST = "127.0.0.1"
REMOTE_PORT = 2260

_ssh_client = None
_ssh_lock = threading.Lock()


def _get_transport():
    global _ssh_client
    with _ssh_lock:
        if _ssh_client is None or not _ssh_client.get_transport().is_active():
            client = paramiko.SSHClient()
            client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
            client.connect(HOST, username=USER, password=PASSWORD, timeout=20)
            _ssh_client = client
            print(f"SSH 已连接 {HOST}")
        return _ssh_client.get_transport()


class ForwardHandler(socketserver.BaseRequestHandler):
    def handle(self):
        try:
            transport = _get_transport()
            chan = transport.open_channel(
                "direct-tcpip",
                (REMOTE_HOST, REMOTE_PORT),
                self.request.getpeername(),
            )
        except Exception as exc:
            print(f"转发通道建立失败: {exc}")
            return
        if chan is None:
            print("转发通道被拒绝")
            return
        try:
            while True:
                r, _, _ = select.select([self.request, chan], [], [], 30)
                if not r:
                    continue
                if self.request in r:
                    data = self.request.recv(65536)
                    if not data:
                        break
                    chan.sendall(data)
                if chan in r:
                    data = chan.recv(65536)
                    if not data:
                        break
                    self.request.sendall(data)
        except (ConnectionError, OSError):
            pass
        finally:
            try:
                chan.close()
            except Exception:
                pass
            try:
                self.request.close()
            except Exception:
                pass


class ForwardServer(socketserver.ThreadingTCPServer):
    daemon_threads = True
    allow_reuse_address = True


def keepalive():
    while True:
        time.sleep(30)
        try:
            t = _get_transport()
            t.send_ignore()
        except Exception:
            print("SSH 连接异常，等待下次请求自动重连")


def main():
    _get_transport()
    threading.Thread(target=keepalive, daemon=True).start()
    server = ForwardServer(("127.0.0.1", LOCAL_PORT), ForwardHandler)
    print(f"隧道监听中: http://127.0.0.1:{LOCAL_PORT} → {HOST}:{REMOTE_PORT}（Ctrl+C 退出）")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\n隧道已关闭")
    finally:
        if _ssh_client:
            _ssh_client.close()


if __name__ == "__main__":
    main()
