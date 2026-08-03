"""账号落盘：CSV 追加写入（线程安全）。"""
import csv
import threading
import time
from pathlib import Path


class AccountSaver:
    def __init__(self, path: str = "accounts.csv"):
        self._path = Path(path)
        self._lock = threading.Lock()
        self._init_header()

    def _init_header(self):
        if not self._path.exists():
            with self._lock:
                if not self._path.exists():
                    with self._path.open("w", newline="", encoding="utf-8") as f:
                        csv.writer(f).writerow(
                            ["email", "password", "api_key", "status", "time"])

    def save(self, email: str, password: str, api_key: str, status: str):
        with self._lock:
            with self._path.open("a", newline="", encoding="utf-8") as f:
                csv.writer(f).writerow(
                    [email, password, api_key or "", status,
                     time.strftime("%Y-%m-%d %H:%M:%S")])
