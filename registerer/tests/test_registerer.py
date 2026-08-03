"""轻量单测（标准库 unittest，无需 pytest）。

运行：python -m unittest discover -s tests -v
"""
import email
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from proxies import Proxy, ProxyPool, _parse_line  # noqa: E402


class TestParseLine(unittest.TestCase):
    def test_http_with_auth(self):
        p = _parse_line("http://user:pass@1.2.3.4:8080")
        self.assertEqual(p.server, "http://1.2.3.4:8080")
        self.assertEqual(p.username, "user")
        self.assertEqual(p.password, "pass")

    def test_socks5_no_auth(self):
        p = _parse_line("socks5://5.6.7.8:1080")
        self.assertEqual(p.server, "socks5://5.6.7.8:1080")
        self.assertEqual(p.username, "")

    def test_no_scheme_defaults_http(self):
        p = _parse_line("9.9.9.9:3128")
        self.assertEqual(p.server, "http://9.9.9.9:3128")

    def test_comment_and_blank_ignored(self):
        self.assertIsNone(_parse_line(""))
        self.assertIsNone(_parse_line("# comment"))
        self.assertIsNone(_parse_line("   "))

    def test_camofox_dict(self):
        p = _parse_line("http://user:pass@1.2.3.4:8080")
        self.assertEqual(p.camofox_dict(),
                         {"server": "http://1.2.3.4:8080", "username": "user", "password": "pass"})
        p2 = _parse_line("socks5://5.6.7.8:1080")
        self.assertEqual(p2.camofox_dict(), {"server": "socks5://5.6.7.8:1080"})


class TestProxyPool(unittest.TestCase):
    def test_round_robin(self):
        pool = ProxyPool([Proxy("http://a:1", "", ""), Proxy("http://b:2", "", "")])
        self.assertEqual(pool.next().server, "http://a:1")
        self.assertEqual(pool.next().server, "http://b:2")
        self.assertEqual(pool.next().server, "http://a:1")

    def test_mark_bad_skips(self):
        pool = ProxyPool([Proxy("http://a:1", "", ""), Proxy("http://b:2", "", "")])
        pool.mark_bad("http://a:1")
        self.assertEqual(pool.next().server, "http://b:2")
        self.assertEqual(pool.next().server, "http://b:2")

    def test_all_bad_returns_none(self):
        pool = ProxyPool([Proxy("http://a:1", "", "")])
        pool.mark_bad("http://a:1")
        self.assertIsNone(pool.next())

    def test_reset_bad(self):
        pool = ProxyPool([Proxy("http://a:1", "", "")])
        pool.mark_bad("http://a:1")
        pool.reset_bad()
        self.assertEqual(pool.next().server, "http://a:1")


class TestMailParsing(unittest.TestCase):
    def test_results_container(self):
        """admin/mails 返回 {results: [...]} 容器时能正确解析。"""
        import mail
        raw = (
            "From: Firecrawl <no-reply@firecrawl.dev>\r\n"
            "Subject: Verify your email\r\n"
            "Content-Type: text/plain; charset=utf-8\r\n\r\n"
            "Click: https://clerk.firecrawl.dev/verify?t=1"
        )
        payload = {"results": [{"id": 7, "raw": raw}], "count": 1}
        messages = list(mail._iter_messages_from_payload(payload))
        self.assertEqual(len(messages), 1)
        self.assertEqual(messages[0]["id"], 7)
        self.assertEqual(messages[0]["subject"], "Verify your email")

    def _parse_raw(self, raw: str):
        from email import policy
        from email.parser import BytesParser
        from mail import _extract_verification_link, _msg_html, _msg_text

        msg = BytesParser(policy=policy.default).parsebytes(raw.encode())
        message = {
            "id": 1,
            "subject": str(msg.get("Subject", "")),
            "from": str(msg.get("From", "")),
            "html": _msg_html(msg),
            "text": _msg_text(msg),
        }
        return message, _extract_verification_link

    def test_extract_clerk_link_from_html(self):
        raw = (
            "From: Firecrawl <no-reply@firecrawl.dev>\r\n"
            "Subject: Verify your email\r\n"
            "Content-Type: text/html; charset=utf-8\r\n\r\n"
            '<a href="https://accounts.firecrawl.dev/v1/verify?token=abc123">'
            "Confirm</a>"
        )
        message, extract = self._parse_raw(raw)
        link = extract(message)
        self.assertIsNotNone(link)
        self.assertIn("verify", link)

    def test_extract_link_from_plain_text(self):
        raw = (
            "From: Clerk <no-reply@clerk.dev>\r\n"
            "Subject: Confirm your email address\r\n"
            "Content-Type: text/plain; charset=utf-8\r\n\r\n"
            "Click here: https://clerk.firecrawl.dev/signup/verify?token=xyz"
        )
        message, extract = self._parse_raw(raw)
        link = extract(message)
        self.assertIn("clerk.firecrawl.dev", link)

    def test_non_verification_mail_returns_none(self):
        raw = (
            "From: news@example.com\r\n"
            "Subject: Weekly newsletter\r\n"
            "Content-Type: text/plain; charset=utf-8\r\n\r\n"
            "Hello https://example.com/read/1"
        )
        message, extract = self._parse_raw(raw)
        self.assertIsNone(extract(message))


class TestConfigPlaceholder(unittest.TestCase):
    def test_is_placeholder(self):
        from config import is_placeholder
        self.assertTrue(is_placeholder("replace-with-admin-password"))
        self.assertTrue(is_placeholder("https://your-temp-mail-worker.example.com"))
        self.assertFalse(is_placeholder(""))
        self.assertFalse(is_placeholder("https://mail.lucky0506.shop"))
        self.assertFalse(is_placeholder("my-secret-token"))

    def test_domains_list_parsing(self):
        from config import TEMP_MAIL_DOMAINS
        # 依赖真实 env 的值不可控，这里验证配置模块不报错即可。
        self.assertIsInstance(TEMP_MAIL_DOMAINS, list)

    def test_pick_domain_round_robin(self):
        import mail
        # 拉取失败时回退配置
        mail.fetch_domains = lambda: []
        mail.TEMP_MAIL_DOMAINS = ["a.com", "b.com", "c.com"]
        mail.TEMP_MAIL_DOMAIN = "x.com"
        mail._domain_cursor = 0
        mail._domains_cache = None
        picked = [mail._pick_domain() for _ in range(4)]
        self.assertEqual(picked, ["a.com", "b.com", "c.com", "a.com"])
        # 未配置多域名时回退单域名。
        mail.TEMP_MAIL_DOMAINS = []
        mail._domain_cursor = 0
        mail._domains_cache = None
        self.assertEqual(mail._pick_domain(), "x.com")

    def test_pick_domain_fetch_priority(self):
        import mail
        # 拉取成功时优先用拉取结果（即使配置了单域名兜底）
        mail.fetch_domains = lambda: ["a.com", "b.com"]
        mail.TEMP_MAIL_DOMAINS = []
        mail.TEMP_MAIL_DOMAIN = "x.com"
        mail._domain_cursor = 0
        mail._domains_cache = None
        mail._domains_cache_time = 0
        picked = [mail._pick_domain() for _ in range(3)]
        self.assertEqual(picked, ["a.com", "b.com", "a.com"])

    def test_fetch_domains_from_settings(self):
        """open_api/settings 返回 randomSubdomainDomains 时能正确拉取。"""
        import mail
        mail.TEMP_MAIL_API_URL = "https://fake.example.com"
        original = mail.std_requests
        calls = []

        class FakeResponse:
            def raise_for_status(self):
                pass

            def json(self):
                return {"domains": ["a.com", "b.com", "c.com"],
                        "randomSubdomainDomains": ["b.com", "c.com"]}

        class FakeRequests:
            @staticmethod
            def get(url, timeout=None):
                calls.append(url)
                return FakeResponse()

        mail.std_requests = FakeRequests
        try:
            domains = mail.fetch_domains()
        finally:
            mail.std_requests = original
        self.assertEqual(domains, ["b.com", "c.com"])
        self.assertTrue(calls[0].startswith("https://fake.example.com/open_api/settings"))


if __name__ == "__main__":
    unittest.main()
