#!/usr/bin/env python3
"""End-to-end functional check, driven through the real HTTP surface.

    ./scripts/e2e_check.py <admin-password>
    E2E_BASE=http://localhost:8791 E2E_DB=/path/to/portal.db ./scripts/e2e_check.py <pw>

This exists because the Go and vitest suites cannot catch a whole class of defect: they call store
methods directly, so a feature can be fully correct and still be unreachable through the API an
admin actually uses. That is not hypothetical — it is how the OU tree shipped with SetGroupParent
implemented, tested and impossible to invoke, which silently disabled every inherited setting.

Run it against a portal started on a THROWAWAY database: it creates accounts, changes the admin
password, and restarts the server.

Layers under test:
  A  first-run bootstrap + core portal (must not have regressed)
  B  ADR 0022 — OU tenancy and tree inheritance, account validity
  C  ADR 0024 — report versions, grants, visibility modes, the reader's switcher
  D  ADR 0023 — password change, 2FA, recovery codes, step-up, SSO gating
  E  state survives a restart, and the schema reconcile is idempotent
"""
import base64, hashlib, hmac, json, os, sqlite3, struct, subprocess, sys, time, urllib.error, urllib.request

BASE = os.environ.get("E2E_BASE", "http://localhost:8791")
DB = os.environ.get("E2E_DB", "/tmp/e2e/data/portal.db")
# Resolved from this script's own location, so the harness runs on any checkout rather than only
# on the machine it was written on. E2E_BIN overrides it.
BIN = os.environ.get(
    "E2E_BIN",
    os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "local-build", "report-portal"),
)
# Derived from the database path, so pointing E2E_DB at a different portal restarts THAT one.
# Hardcoding it silently restarted the wrong server and made the restart checks meaningless.
CFG = os.path.dirname(os.path.dirname(DB))

results = []


def check(layer, name, ok, detail=""):
    results.append((layer, name, bool(ok), detail))
    print(f"  {'PASS' if ok else 'FAIL'}  {name}" + (f"   [{detail}]" if detail and not ok else ""))


class Session:
    """A cookie-holding HTTP client; one per portal user."""

    def __init__(self):
        self.cookie = None

    def req(self, method, path, body=None, token=None, headers=None):
        url = BASE + path
        data = json.dumps(body).encode() if body is not None else None
        r = urllib.request.Request(url, data=data, method=method)
        if data is not None:
            r.add_header("Content-Type", "application/json")
        if self.cookie:
            r.add_header("Cookie", self.cookie)
        if token:
            r.add_header("Authorization", "Bearer " + token)
        for k, v in (headers or {}).items():
            r.add_header(k, v)
        try:
            with urllib.request.urlopen(r, timeout=20) as resp:
                sc = resp.getcode()
                raw = resp.read().decode()
                for h, v in resp.getheaders():
                    if h.lower() == "set-cookie" and "rp_session=" in v:
                        self.cookie = v.split(";")[0]
        except urllib.error.HTTPError as e:
            sc, raw = e.code, e.read().decode()
        try:
            return sc, json.loads(raw) if raw else {}
        except json.JSONDecodeError:
            return sc, {"_raw": raw}

    def login(self, u, p):
        return self.req("POST", "/api/login", {"username": u, "password": p})


def db():
    # A timeout, because the server's first-run stock-name import is a long write transaction and a
    # reader that waits forever turns a slow start into a hung test.
    return sqlite3.connect(DB, timeout=20)


def sql(q, *a):
    c = db()
    try:
        cur = c.execute(q, a)
        c.commit()
        return cur.fetchall()
    finally:
        c.close()


def totp_code(secret, offset=0):
    key = base64.b32decode(secret + "=" * ((8 - len(secret) % 8) % 8))
    counter = int(time.time()) // 30 + offset
    h = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    o = h[19] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def restart():
    """Restart the e2e server only. Targeted by PORT, never `pkill -f report-portal`, which would
    also take down whatever else the developer has running."""
    port = BASE.rsplit(":", 1)[1]
    pids = subprocess.run(["lsof", "-ti", f":{port}"], capture_output=True, text=True).stdout.split()
    for pid in pids:
        subprocess.run(["kill", pid], capture_output=True)
    time.sleep(1)
    subprocess.Popen(f"cd {CFG} && RP_CONFIG=./config.yaml {BIN} >> server.log 2>&1", shell=True)
    for _ in range(40):
        time.sleep(0.4)
        try:
            urllib.request.urlopen(BASE + "/healthz", timeout=1)
            return
        except Exception:
            pass
    raise SystemExit("server did not come back")


ADMIN_PW = sys.argv[1]
admin = Session()

# ---------------------------------------------------------------- A. core
print("\nA. 首启与核心门户")
sc, me = admin.login("admin", ADMIN_PW)
check("A", "管理员登录", sc == 200 and me.get("admin") is True, f"{sc} {me}")
check("A", "首启种子：27 个报告类型", len(sql("SELECT name FROM type_config")) == 27)
check("A", "首启种子：默认分组", len(sql("SELECT id FROM user_groups WHERE is_default=1")) == 1)
check("A", "首启种子：默认版本", sql("SELECT name FROM report_versions") == [("default",)])

# Tokens are stored hashed (ADR 0019), so the plaintext exists only in the creation response —
# reading the table gives a NULL `token` column. That is the design working; the harness has to
# capture it at mint time like any other client.
sc, mint = admin.req("POST", "/api/admin/tokens", {"name": "e2e", "scope": "all"})
TOKEN = mint.get("token") or mint.get("value") or ""
check("A", "创建 API 令牌并返回明文（仅此一次）", sc == 200 and bool(TOKEN), f"{sc} {mint}")

TODAY = sql("SELECT date('now','localtime')")[0][0]
machine = Session()
sc, r = machine.req("POST", "/api/v1/reports", {
    "symbol": "600519", "date": TODAY, "subtype": "估值分析", "title": "茅台估值分析",
    "body_md": "## 内部估值\n| 因子 | 权重 |\n| 护城河 | 0.35 |\nPrompt: 资深分析师…"}, token=TOKEN)
check("A", "机器入库 /api/v1/reports", sc == 200 and r.get("ok"), f"{sc} {r}")
RID_INTERNAL = r.get("id")

sc, home = admin.req("GET", "/api/home")
check("A", "首页信息流", sc == 200 and (home.get("total", 0) >= 1 or home.get("groups")), f"{sc}")
sc, stock = admin.req("GET", "/api/stock/600519")
check("A", "个股页", sc == 200 and stock.get("symbol") == "600519", f"{sc}")
sc, syms = admin.req("GET", "/api/symbols?q=600519")
check("A", "代码自动补全", sc == 200, f"{sc}")
sc, md = admin.req("GET", f"/report/{RID_INTERNAL}/md")
# Asserts the CONTENT, not just the status: this replaced a separate /api/repbody check, and the
# export is the path the product actually serves a body through.
check("A", "报告正文导出", sc == 200 and "护城河" in md.get("_raw", ""), f"{sc}")
sc, ver = admin.req("GET", "/api/version")
check("A", "版本信息接口", sc in (200, 404), f"{sc}")

# ---------------------------------------------------------------- B. ADR 0022
print("\nB. ADR 0022 — OU 租户 / 有效期 / 配额 / 运行白名单")
root = sql("SELECT id FROM user_groups WHERE is_default=1")[0][0]
sc, g = admin.req("POST", "/api/admin/groups", {"name": "客户A"})
OU = g.get("id") or sql("SELECT id FROM user_groups WHERE name='客户A'")[0][0]
sc, _ = admin.req("PUT", f"/api/admin/groups/{OU}", {"name": "客户A", "restricted": True, "parent_id": root})
check("B", "建立受限 OU", sc == 200, f"{sc}")
sc, g2 = admin.req("POST", "/api/admin/groups", {"name": "客户A-子部门"})
SUB = g2.get("id") or sql("SELECT id FROM user_groups WHERE name='客户A-子部门'")[0][0]
admin.req("PUT", f"/api/admin/groups/{SUB}", {"name": "客户A-子部门", "parent_id": OU})
sc, groups = admin.req("GET", "/api/admin/groups")
sub = [x for x in groups.get("groups", []) if x["id"] == SUB]
check("B", "受限标记沿 OU 树继承", bool(sub) and sub[0].get("restricted_effective") is True,
      json.dumps(sub, ensure_ascii=False))

# Created through the admin API, not the adduser CLI: the CLI opens the same SQLite file the
# running server holds, and blocks on its write lock.
sc, _ = admin.req("POST", "/api/admin/users",
                  {"username": "ext", "password": "external-pass-1234", "role": "user",
                   "primary_group": OU})
check("B", "管理端建账号并归入 OU", sc == 200, f"{sc}")
ext = Session()
sc, _ = ext.login("ext", "external-pass-1234")
check("B", "外部账号登录", sc == 200, f"{sc}")

sql("UPDATE users SET expires_at=date('now','localtime','-1 day') WHERE username='ext'")
expired = Session()
sc, _ = expired.login("ext", "external-pass-1234")
check("B", "过期账号无法登录", sc != 200, f"{sc}")
sc, _ = ext.req("GET", "/api/me")
check("B", "过期后既有会话立即失效", sc == 401, f"{sc}")
sql("UPDATE users SET expires_at=NULL WHERE username='ext'")
ext = Session()
ext.login("ext", "external-pass-1234")
sc, _ = ext.req("GET", "/api/me")
check("B", "清除有效期后恢复", sc == 200, f"{sc}")

# ---------------------------------------------------------------- C. ADR 0024
print("\nC. ADR 0024 — 报告版本")
sc, r = machine.req("POST", "/api/v1/reports", {
    "symbol": "600519", "date": TODAY, "subtype": "估值分析", "title": "茅台估值结论",
    "version": "对外版", "body_md": "## 结论\n综合评分 78/100。"}, token=TOKEN)
RID_PUBLIC = r.get("id")
check("C", "同代码/日期/小类的另一版本不覆盖", sc == 200 and RID_PUBLIC != RID_INTERNAL, f"{sc} {r}")
check("C", "两行都在库里",
      len(sql("SELECT id FROM reports WHERE symbol='600519' AND rdate=? AND rtype='估值分析'", TODAY)) == 2)

sc, _ = ext.req("GET", f"/api/v1/reports/{RID_INTERNAL}")
check("C", "外部：未授权版本不可读（内部版）", sc == 404, f"{sc}")
sc, _ = ext.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "外部：已授权但非本人申请，不可读", sc == 404, f"{sc}")

sc, _ = admin.req("POST", "/api/admin/versions", {
    "name": "对外版", "label": "对外版", "ord": 1, "visibility": "owner",
    "grants": [f"g:{OU}"]})
check("C", "管理端保存版本与授权", sc == 200, f"{sc}")
sql("INSERT OR IGNORE INTO report_viewers(principal,rdate,report_id) VALUES(?,?,?)",
    f"u:ext", TODAY, RID_PUBLIC)
sc, rep = ext.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "外部：本人申请过 → 可读", sc == 200, f"{sc}")
if sc == 200:
    md = json.dumps(rep, ensure_ascii=False)
    check("C", "外部读到的正文不含内部内容", "护城河" not in md and "Prompt" not in md)
sc, _ = ext.req("GET", f"/api/v1/reports/{RID_INTERNAL}")
check("C", "外部：内部版仍不可读", sc == 404, f"{sc}")

sc, sw = ext.req("GET", f"/api/report/{RID_PUBLIC}/versions")
check("C", "外部切换器只列可读版本", sc == 200 and len(sw.get("versions", [])) == 1, f"{sc} {sw}")
sc, sw = admin.req("GET", f"/api/report/{RID_PUBLIC}/versions")
check("C", "管理员切换器列出两个版本（标题不同也能归组）",
      sc == 200 and len(sw.get("versions", [])) == 2, f"{sc} {sw}")

sc, lst = ext.req("GET", "/api/home")
blob = json.dumps(lst, ensure_ascii=False)
check("C", "外部首页不出现内部报告", "茅台估值分析" not in blob, blob[:200])

# 组可见性：同 OU 同事能看到
admin.req("POST", "/api/admin/users",
          {"username": "ext2", "password": "external-pass-1234", "role": "user",
           "primary_group": OU})
ext2 = Session()
ext2.login("ext2", "external-pass-1234")
sc, _ = ext2.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "仅本人模式：同事不可读", sc == 404, f"{sc}")
admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "group", "grants": [f"g:{OU}"]})
sql("INSERT OR IGNORE INTO report_viewers(principal,rdate,report_id) VALUES(?,?,?)",
    f"g:{OU}", TODAY, RID_PUBLIC)
sc, _ = ext2.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "改为本组可见：同事可读", sc == 200, f"{sc}")
admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "owner", "grants": [f"g:{OU}"]})
sc, _ = ext2.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "改回仅本人：同事立即失去访问", sc == 404, f"{sc}")

admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "owner", "grants": []})
sc, _ = ext.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "撤销授权后本人也不可读", sc == 404, f"{sc}")
admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "owner", "grants": [f"g:{OU}"]})

sc, vs = admin.req("GET", "/api/admin/versions")
names = [v["name"] for v in vs.get("versions", [])]
check("C", "管理端列出版本注册表", sc == 200 and "default" in names and "对外版" in names, f"{names}")
sc, _ = admin.req("DELETE", "/api/admin/versions/default")
check("C", "默认版本不可删除", sc != 200, f"{sc}")

# ---------------------------------------------------------------- D. ADR 0023
print("\nD. ADR 0023 — 密码 / 两步验证 / 步进验证 / SSO")
sc, provs = Session().req("GET", "/api/sso/providers")
check("D", "未配置时 SSO 列表为空", sc == 200 and provs.get("providers") == [], f"{sc} {provs}")
sc, _ = Session().req("GET", "/api/auth/oidc/nope/start")
check("D", "未配置的 SSO 路由 404", sc == 404, f"{sc}")

sc, _ = ext.req("POST", "/api/me/2fa/setup")
check("D", "两步验证：无步进验证被拒", sc == 403, f"{sc}")
sc, _ = ext.req("POST", "/api/me/2fa/setup", headers={"X-Step-Up-Proof": "wrong"})
check("D", "两步验证：错误凭证被拒", sc == 403, f"{sc}")
sc, setup = ext.req("POST", "/api/me/2fa/setup", headers={"X-Step-Up-Proof": "external-pass-1234"})
check("D", "两步验证：正确凭证可开始配置", sc == 200 and setup.get("secret"), f"{sc}")
SECRET = setup.get("secret", "")
if SECRET:
    sc, en = ext.req("POST", "/api/me/2fa/enable", {"code": totp_code(SECRET)})
    check("D", "两步验证：确认后启用并发放恢复码",
          sc == 200 and len(en.get("recovery_codes", [])) == 10, f"{sc}")
    RECOVERY = en.get("recovery_codes", [])

    leg1 = Session()
    sc, r1 = leg1.login("ext", "external-pass-1234")
    check("D", "开启后：密码这一腿不发会话", sc == 200 and r1.get("totp_required") and not r1.get("user"), f"{sc} {r1}")
    pending = r1.get("token")
    sc, _ = leg1.req("GET", "/api/me")
    check("D", "开启后：仅密码不能访问", sc == 401, f"{sc}")
    # enable() burned the current time step, so use the previous one — inside the +/-1 tolerance
    # window and not yet spent. Avoids a 31-second wait without weakening what is being tested.
    sc, r2 = leg1.req("POST", "/api/login/2fa", {"token": pending, "code": totp_code(SECRET, -1)})
    check("D", "第二腿：验证码完成登录", sc == 200 and r2.get("user") == "ext", f"{sc} {r2}")

    leg2 = Session()
    _, r3 = leg2.login("ext", "external-pass-1234")
    sc, r4 = leg2.req("POST", "/api/login/2fa", {"token": r3.get("token"), "code": RECOVERY[0]})
    check("D", "恢复码可完成登录", sc == 200 and r4.get("user") == "ext", f"{sc}")
    leg3 = Session()
    _, r5 = leg3.login("ext", "external-pass-1234")
    sc, _ = leg3.req("POST", "/api/login/2fa", {"token": r5.get("token"), "code": RECOVERY[0]})
    check("D", "恢复码不可重复使用", sc != 200, f"{sc}")

    leg4 = Session()
    _, r6 = leg4.login("ext", "external-pass-1234")
    sc, _ = leg4.req("POST", "/api/login/2fa", {"token": r6.get("token"), "code": "000000"})
    check("D", "错误验证码被拒", sc != 200, f"{sc}")
    sc, _ = leg4.req("POST", "/api/login/2fa", {"token": r6.get("token"), "code": totp_code(SECRET, -1)})
    check("D", "待验令牌一次性（错一次即作废）", sc != 200, f"{sc}")

admin2 = Session()
admin2.login("admin", ADMIN_PW)
sc, _ = admin2.req("POST", "/api/me/password", {"current": "wrong", "new": "a-brand-new-passphrase"})
check("D", "改密码：需要当前密码", sc != 200, f"{sc}")
sc, _ = admin2.req("POST", "/api/me/password", {"current": ADMIN_PW, "new": "a-brand-new-passphrase"})
check("D", "改密码：成功", sc == 200, f"{sc}")
old = Session()
sc, _ = old.login("admin", ADMIN_PW)
check("D", "改密码后旧密码失效", sc != 200, f"{sc}")
sc, _ = admin.req("GET", "/api/me")
check("D", "改密码使其他会话下线", sc == 401, f"{sc}")
admin = Session()
admin.login("admin", "a-brand-new-passphrase")

sc, meJSON = admin.req("GET", "/api/me")
check("D", "/api/me 报告安全状态",
      all(k in meJSON for k in ("federated", "totp_enabled", "passkeys")), f"{meJSON}")

# ---------------------------------------------------------------- F. captcha + registration
print("\nF. 验证码与自助注册")
sc, cfg = admin.req("GET", "/api/register/config")
check("F", "自助注册默认关闭", sc == 200 and cfg.get("enabled") is False, f"{sc} {cfg}")
sc, _ = Session().req("POST", "/api/register",
                      {"email": "x@example.com", "password": "a-long-enough-password"})
check("F", "关闭时注册路由 404", sc == 404, f"{sc}")
sc, cap = Session().req("GET", "/api/captcha?ctx=login")
check("F", "验证码默认不要求", sc == 200 and cap.get("required") is False, f"{sc} {cap}")

sc, _ = admin.req("POST", "/api/admin/security", {
    "captcha": {"provider": "image", "login": True, "forgot": True, "register": True,
                "trigger": "always", "fail_threshold": 3},
    "registration": {"enabled": True, "require_verify": False, "domains": "",
                     "default_group": "", "expiry_days": ""}})
check("F", "管理端保存登录保护设置", sc == 200, f"{sc}")

sc, cap = Session().req("GET", "/api/captcha?ctx=register")
check("F", "开启后签发图形验证码",
      sc == 200 and cap.get("required") is True and str(cap.get("image", "")).startswith("data:image"),
      f"{sc} {list(cap)}")
check("F", "验证码接口不泄露答案", "answer" not in json.dumps(cap).lower())

for name, path, body in [("登录", "/api/login", {"username": "admin", "password": "x"}),
                         ("找回密码", "/api/password/forgot", {"account": "admin"}),
                         ("注册", "/api/register", {"email": "n@example.com",
                                                    "password": "a-long-enough-password"})]:
    sc, b = Session().req("POST", path, body)
    check("F", f"{name}：缺验证码被拒且带标记",
          sc == 400 and b.get("captcha_required") is True, f"{sc} {b}")
check("F", "被拒的注册没有留下账号",
      not sql("SELECT username FROM users WHERE username='n@example.com'"))

# 配置一个 token 服务但不给密钥 —— 验证失败必须闭合，而不是放行
admin.req("POST", "/api/admin/security", {
    "captcha": {"provider": "turnstile", "login": False, "forgot": False, "register": True,
                "trigger": "always", "fail_threshold": 3},
    "registration": {"enabled": True, "require_verify": False, "domains": "",
                     "default_group": "", "expiry_days": ""}})
sc, b = Session().req("POST", "/api/register",
                      {"email": "closed@example.com", "password": "a-long-enough-password",
                       "captcha_token": "anything"})
check("F", "验证器配置错误时闭合（不放行）", sc == 400, f"{sc} {b}")
check("F", "闭合时同样没有建账号",
      not sql("SELECT username FROM users WHERE username='closed@example.com'"))

sc, _ = admin.req("POST", "/api/admin/security", {
    "captcha": {"provider": "image", "login": False, "forgot": False, "register": False,
                "trigger": "always", "fail_threshold": 3},
    "registration": {"enabled": True, "require_verify": False, "domains": "corp.example",
                     "default_group": "", "expiry_days": ""}})
sc, _ = Session().req("POST", "/api/register",
                      {"email": "outsider@elsewhere.test", "password": "a-long-enough-password"})
check("F", "域名白名单拒绝表外域名", sc == 400, f"{sc}")
sc, b = Session().req("POST", "/api/register",
                      {"email": "newbie@corp.example", "password": "a-long-enough-password"})
check("F", "允许的域名可以注册", sc == 200, f"{sc} {b}")
row = sql("SELECT active, COALESCE(restricted,0), COALESCE(group_id,0) FROM users WHERE username='newbie@corp.example'")
check("F", "未分配 OU 的注册账号：启用但受限、无分组",
      row == [(1, 1, 0)], f"{row}")

reg = Session()
sc, _ = reg.login("newbie@corp.example", "a-long-enough-password")
check("F", "注册账号可以登录", sc == 200, f"{sc}")
sc, home = reg.req("GET", "/api/home")
blob = json.dumps(home, ensure_ascii=False)
check("F", "注册账号看不到任何报告", "茅台" not in blob, blob[:160])
sc, _ = reg.req("GET", f"/api/v1/reports/{RID_INTERNAL}")
check("F", "注册账号读不到内部报告", sc == 404, f"{sc}")

sc, _ = Session().req("POST", "/api/register",
                      {"email": "newbie@corp.example", "password": "a-long-enough-password"})
check("F", "重复邮箱明确拒绝", sc == 409, f"{sc}")
sc, _ = Session().req("POST", "/api/register/verify", {"token": "forged"})
check("F", "伪造的确认令牌被拒", sc != 200, f"{sc}")

# ---------------------------------------------------------------- restart
print("\nE. 重启后状态保持")
restart()
admin = Session()
sc, _ = admin.login("admin", "a-brand-new-passphrase")
check("E", "重启后可登录", sc == 200, f"{sc}")
sc, vs = admin.req("GET", "/api/admin/versions")
check("E", "重启后版本与授权仍在",
      sc == 200 and any(v["name"] == "对外版" and v["grants"] for v in vs.get("versions", [])), f"{sc}")
check("E", "重启不重建唯一索引（幂等）",
      "version" in sql("SELECT sql FROM sqlite_master WHERE name='idx_reports_ident'")[0][0])

# ---------------------------------------------------------------- report
print("\n" + "=" * 64)
bad = [r for r in results if not r[2]]
by = {}
for layer, _, ok, _ in results:
    a, b = by.get(layer, (0, 0))
    by[layer] = (a + (1 if ok else 0), b + 1)
for layer in sorted(by):
    p, n = by[layer]
    print(f"  {layer}: {p}/{n}")
print(f"\n  总计 {len(results) - len(bad)}/{len(results)} 通过")
if bad:
    print("\n  失败项：")
    for layer, name, _, detail in bad:
        print(f"    [{layer}] {name}   {detail}")
sys.exit(1 if bad else 0)
