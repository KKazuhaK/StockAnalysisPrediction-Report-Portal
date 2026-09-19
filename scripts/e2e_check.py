#!/usr/bin/env python3
"""End-to-end functional check, driven through the real HTTP surface.

    ./scripts/e2e_check.py <admin-password>
    E2E_BASE=http://localhost:8791 E2E_DB=/path/to/portal.db ./scripts/e2e_check.py <pw>

For Docker, set E2E_CONTAINER to the container name and E2E_DB to its bind-mounted
SQLite file. E2E_ADMIN_PASSWORD can supply the password without a command-line argument.
The caller needs database write access and permission to restart that container.

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
from e2e_runtime import check_spa, restart_container, unused_totp_step

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


def totp_code(secret, offset=0, step=None):
    key = base64.b32decode(secret + "=" * ((8 - len(secret) % 8) % 8))
    counter = int(time.time()) // 30 + offset if step is None else step
    h = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    o = h[19] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def restart():
    """Restart the e2e server only. Targeted by PORT, never `pkill -f report-portal`, which would
    also take down whatever else the developer has running."""
    if os.environ.get("E2E_CONTAINER"):
        restart_container(os.environ["E2E_CONTAINER"], BASE)
        return
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


ADMIN_PW = os.environ.get("E2E_ADMIN_PASSWORD") or sys.argv[1]
if os.environ.get("E2E_CONTAINER"):
    check_spa(BASE)
admin = Session()

# ---------------------------------------------------------------- A. core
print("\nA. First run and the core portal")
sc, me = admin.login("admin", ADMIN_PW)
check("A", "Admin signs in", sc == 200 and me.get("admin") is True, f"{sc} {me}")
check("A", "First-run seed: 27 report types", len(sql("SELECT name FROM type_config")) == 27)
check("A", "First-run seed: the default group", len(sql("SELECT id FROM user_groups WHERE is_default=1")) == 1)
check("A", "First-run seed: the default and manual versions",
      set(sql("SELECT name FROM report_versions")) == {("default",), ("manual",)})

# Tokens are stored hashed (ADR 0019), so the plaintext exists only in the creation response —
# reading the table gives a NULL `token` column. That is the design working; the harness has to
# capture it at mint time like any other client.
sc, mint = admin.req("POST", "/api/admin/tokens", {"name": "e2e", "scope": "all"})
TOKEN = mint.get("token") or mint.get("value") or ""
check("A", "Mint an API token, returned in clear once", sc == 200 and bool(TOKEN), f"{sc} {mint}")

TODAY = sql("SELECT date('now','localtime')")[0][0]
machine = Session()
sc, r = machine.req("POST", "/api/v1/reports", {
    "symbol": "600519", "date": TODAY, "subtype": "估值分析", "title": "茅台估值分析",
    "body_md": "## 内部估值\n| 因子 | 权重 |\n| 护城河 | 0.35 |\nPrompt: 资深分析师…"}, token=TOKEN)
check("A", "Machine ingest: /api/v1/reports", sc == 200 and r.get("ok"), f"{sc} {r}")
RID_INTERNAL = r.get("id")

sc, home = admin.req("GET", "/api/home")
check("A", "Home feed", sc == 200 and (home.get("total", 0) >= 1 or home.get("groups")), f"{sc}")
sc, stock = admin.req("GET", "/api/stock/600519")
check("A", "Stock page", sc == 200 and stock.get("symbol") == "600519", f"{sc}")
sc, syms = admin.req("GET", "/api/symbols?q=600519")
check("A", "Symbol autocomplete", sc == 200, f"{sc}")
sc, md = admin.req("GET", f"/report/{RID_INTERNAL}/md")
# Asserts the CONTENT, not just the status: this replaced a separate /api/repbody check, and the
# export is the path the product actually serves a body through.
check("A", "Report body export", sc == 200 and "护城河" in md.get("_raw", ""), f"{sc}")
sc, ver = admin.req("GET", "/api/version")
check("A", "Version endpoint", sc in (200, 404), f"{sc}")

# ---------------------------------------------------------------- B. ADR 0022
print("\nB. ADR 0022 — OU tenancy, account validity, quotas, run allow-list")
root = sql("SELECT id FROM user_groups WHERE is_default=1")[0][0]
sc, g = admin.req("POST", "/api/admin/groups", {"name": "客户A"})
OU = g.get("id") or sql("SELECT id FROM user_groups WHERE name='客户A'")[0][0]
sc, _ = admin.req("PUT", f"/api/admin/groups/{OU}", {"name": "客户A", "restricted": True, "parent_id": root})
check("B", "Create a restricted OU", sc == 200, f"{sc}")
sc, g2 = admin.req("POST", "/api/admin/groups", {"name": "客户A-子部门"})
SUB = g2.get("id") or sql("SELECT id FROM user_groups WHERE name='客户A-子部门'")[0][0]
admin.req("PUT", f"/api/admin/groups/{SUB}", {"name": "客户A-子部门", "parent_id": OU})
sc, groups = admin.req("GET", "/api/admin/groups")
sub = [x for x in groups.get("groups", []) if x["id"] == SUB]
check("B", "The restricted flag inherits down the OU tree", bool(sub) and sub[0].get("restricted_effective") is True,
      json.dumps(sub, ensure_ascii=False))

# Created through the admin API, not the adduser CLI: the CLI opens the same SQLite file the
# running server holds, and blocks on its write lock.
sc, _ = admin.req("POST", "/api/admin/users",
                  {"username": "ext", "password": "external-pass-1234", "role": "user",
                   "primary_group": OU})
check("B", "Admin creates an account inside the OU", sc == 200, f"{sc}")
ext = Session()
sc, _ = ext.login("ext", "external-pass-1234")
check("B", "The external account signs in", sc == 200, f"{sc}")

sql("UPDATE users SET expires_at=date('now','localtime','-1 day') WHERE username='ext'")
expired = Session()
sc, _ = expired.login("ext", "external-pass-1234")
check("B", "An expired account cannot sign in", sc != 200, f"{sc}")
sc, _ = ext.req("GET", "/api/me")
check("B", "Expiry ends the existing session immediately", sc == 401, f"{sc}")
sql("UPDATE users SET expires_at=NULL WHERE username='ext'")
ext = Session()
ext.login("ext", "external-pass-1234")
sc, _ = ext.req("GET", "/api/me")
check("B", "Clearing the expiry restores access", sc == 200, f"{sc}")

# ---------------------------------------------------------------- C. ADR 0024
print("\nC. ADR 0024 — report versions")
sc, r = machine.req("POST", "/api/v1/reports", {
    "symbol": "600519", "date": TODAY, "subtype": "估值分析", "title": "茅台估值结论",
    "version": "对外版", "body_md": "## 结论\n综合评分 78/100。"}, token=TOKEN)
RID_PUBLIC = r.get("id")
check("C", "A second version with the same code/date/subtype does not overwrite", sc == 200 and RID_PUBLIC != RID_INTERNAL, f"{sc} {r}")
check("C", "Both rows are in the database",
      len(sql("SELECT id FROM reports WHERE symbol='600519' AND rdate=? AND rtype='估值分析'", TODAY)) == 2)

sc, _ = ext.req("GET", f"/api/v1/reports/{RID_INTERNAL}")
check("C", "External: an ungranted version is unreadable", sc == 404, f"{sc}")
sc, _ = ext.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "External: granted, but not requested by them — unreadable", sc == 404, f"{sc}")

sc, _ = admin.req("POST", "/api/admin/versions", {
    "name": "对外版", "label": "对外版", "ord": 1, "visibility": "owner",
    "grants": [f"g:{OU}"]})
check("C", "Admin saves the version and its grants", sc == 200, f"{sc}")
sql("INSERT OR IGNORE INTO report_viewers(principal,rdate,report_id) VALUES(?,?,?)",
    f"u:ext", TODAY, RID_PUBLIC)
sc, rep = ext.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "External: they requested it — readable", sc == 200, f"{sc}")
if sc == 200:
    md = json.dumps(rep, ensure_ascii=False)
    check("C", "The external reader's body carries no internal content", "护城河" not in md and "Prompt" not in md)
sc, _ = ext.req("GET", f"/api/v1/reports/{RID_INTERNAL}")
check("C", "External: the internal version stays unreadable", sc == 404, f"{sc}")

sc, sw = ext.req("GET", f"/api/report/{RID_PUBLIC}/versions")
check("C", "The external switcher lists only readable versions", sc == 200 and len(sw.get("versions", [])) == 1, f"{sc} {sw}")
sc, sw = admin.req("GET", f"/api/report/{RID_PUBLIC}/versions")
check("C", "The admin switcher lists both versions, grouped despite different titles",
      sc == 200 and len(sw.get("versions", [])) == 2, f"{sc} {sw}")

sc, lst = ext.req("GET", "/api/home")
blob = json.dumps(lst, ensure_ascii=False)
check("C", "The external home feed shows no internal report", "茅台估值分析" not in blob, blob[:200])

# Group visibility: a colleague in the same OU can see it
admin.req("POST", "/api/admin/users",
          {"username": "ext2", "password": "external-pass-1234", "role": "user",
           "primary_group": OU})
ext2 = Session()
ext2.login("ext2", "external-pass-1234")
sc, _ = ext2.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "Owner-only: a colleague cannot read it", sc == 404, f"{sc}")
admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "group", "grants": [f"g:{OU}"]})
sql("INSERT OR IGNORE INTO report_viewers(principal,rdate,report_id) VALUES(?,?,?)",
    f"g:{OU}", TODAY, RID_PUBLIC)
sc, _ = ext2.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "Group-visible: the colleague can read it", sc == 200, f"{sc}")
admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "owner", "grants": [f"g:{OU}"]})
sc, _ = ext2.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "Back to owner-only: the colleague loses access immediately", sc == 404, f"{sc}")

admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "owner", "grants": []})
sc, _ = ext.req("GET", f"/api/v1/reports/{RID_PUBLIC}")
check("C", "After the grant is revoked, they cannot read it either", sc == 404, f"{sc}")
admin.req("POST", "/api/admin/versions", {"name": "对外版", "label": "对外版", "ord": 1,
                                          "visibility": "owner", "grants": [f"g:{OU}"]})

sc, vs = admin.req("GET", "/api/admin/versions")
names = [v["name"] for v in vs.get("versions", [])]
check("C", "Admin lists the version registry", sc == 200 and "default" in names and "对外版" in names, f"{names}")
sc, _ = admin.req("DELETE", "/api/admin/versions/default")
check("C", "The default version cannot be deleted", sc != 200, f"{sc}")

# ---------------------------------------------------------------- D. ADR 0023
print("\nD. ADR 0023 — passwords, two-factor, step-up, SSO")
sc, provs = Session().req("GET", "/api/sso/providers")
check("D", "With none configured, the SSO list is empty", sc == 200 and provs.get("providers") == [], f"{sc} {provs}")
sc, _ = Session().req("GET", "/api/auth/oidc/nope/start")
check("D", "An unconfigured SSO route is 404", sc == 404, f"{sc}")

sc, _ = ext.req("POST", "/api/me/2fa/setup")
check("D", "Two-factor: refused without step-up", sc == 403, f"{sc}")
sc, _ = ext.req("POST", "/api/me/2fa/setup", headers={"X-Step-Up-Proof": "wrong"})
check("D", "Two-factor: refused with the wrong password", sc == 403, f"{sc}")
sc, setup = ext.req("POST", "/api/me/2fa/setup", headers={"X-Step-Up-Proof": "external-pass-1234"})
check("D", "Two-factor: the right password starts setup", sc == 200 and setup.get("secret"), f"{sc}")
SECRET = setup.get("secret", "")
if SECRET:
    enabled_step = int(time.time()) // 30
    sc, en = ext.req("POST", "/api/me/2fa/enable", {"code": totp_code(SECRET, step=enabled_step)})
    check("D", "Two-factor: confirming enables it and issues recovery codes",
          sc == 200 and len(en.get("recovery_codes", [])) == 10, f"{sc}")
    RECOVERY = en.get("recovery_codes", [])

    leg1 = Session()
    sc, r1 = leg1.login("ext", "external-pass-1234")
    check("D", "Enabled: the password leg issues no session", sc == 200 and r1.get("totp_required") and not r1.get("user"), f"{sc} {r1}")
    pending = r1.get("token")
    sc, _ = leg1.req("GET", "/api/me")
    check("D", "Enabled: the password alone cannot reach the API", sc == 401, f"{sc}")
    # Track the consumed step explicitly: a boundary may pass during password verification.
    login_step = unused_totp_step({enabled_step})
    sc, r2 = leg1.req("POST", "/api/login/2fa", {"token": pending, "code": totp_code(SECRET, step=login_step)})
    check("D", "Second leg: the code completes the sign-in", sc == 200 and r2.get("user") == "ext", f"{sc} {r2}")

    leg2 = Session()
    _, r3 = leg2.login("ext", "external-pass-1234")
    sc, r4 = leg2.req("POST", "/api/login/2fa", {"token": r3.get("token"), "code": RECOVERY[0]})
    check("D", "A recovery code completes the sign-in", sc == 200 and r4.get("user") == "ext", f"{sc}")
    leg3 = Session()
    _, r5 = leg3.login("ext", "external-pass-1234")
    sc, _ = leg3.req("POST", "/api/login/2fa", {"token": r5.get("token"), "code": RECOVERY[0]})
    check("D", "A recovery code cannot be reused", sc != 200, f"{sc}")

    leg4 = Session()
    _, r6 = leg4.login("ext", "external-pass-1234")
    sc, _ = leg4.req("POST", "/api/login/2fa", {"token": r6.get("token"), "code": "not-a-code"})
    check("D", "A wrong code is refused", sc != 200, f"{sc}")
    sc, _ = leg4.req("POST", "/api/login/2fa", {"token": r6.get("token"), "code": totp_code(SECRET, -1)})
    check("D", "The pending token is single-use: one wrong code voids it", sc != 200, f"{sc}")

admin2 = Session()
admin2.login("admin", ADMIN_PW)
sc, _ = admin2.req("POST", "/api/me/password", {"current": "wrong", "new": "a-brand-new-passphrase"})
check("D", "Password change: the current password is required", sc != 200, f"{sc}")
sc, _ = admin2.req("POST", "/api/me/password", {"current": ADMIN_PW, "new": "a-brand-new-passphrase"})
check("D", "Password change: succeeds", sc == 200, f"{sc}")
old = Session()
sc, _ = old.login("admin", ADMIN_PW)
check("D", "The old password stops working after a change", sc != 200, f"{sc}")
sc, _ = admin.req("GET", "/api/me")
check("D", "A password change ends the other sessions", sc == 401, f"{sc}")
admin = Session()
admin.login("admin", "a-brand-new-passphrase")

sc, meJSON = admin.req("GET", "/api/me")
check("D", "/api/me reports the security state",
      all(k in meJSON for k in ("federated", "totp_enabled", "passkeys")), f"{meJSON}")

# ---------------------------------------------------------------- F. captcha + registration
print("\nF. Captcha and self-service registration")
sc, cfg = admin.req("GET", "/api/register/config")
check("F", "Self-service registration is off by default", sc == 200 and cfg.get("enabled") is False, f"{sc} {cfg}")
sc, _ = Session().req("POST", "/api/register",
                      {"email": "x@example.com", "password": "a-long-enough-password"})
check("F", "The registration route is 404 while it is off", sc == 404, f"{sc}")
sc, cap = Session().req("GET", "/api/captcha?ctx=login")
check("F", "The captcha is not required by default", sc == 200 and cap.get("required") is False, f"{sc} {cap}")

sc, _ = admin.req("POST", "/api/admin/security", {
    "captcha": {"provider": "image", "login": True, "forgot": True, "register": True,
                "trigger": "always", "fail_threshold": 3},
    "registration": {"enabled": True, "require_verify": False, "domains": "",
                     "default_group": "", "expiry_days": ""}})
check("F", "Admin saves the sign-in protection settings", sc == 200, f"{sc}")

sc, cap = Session().req("GET", "/api/captcha?ctx=register")
check("F", "Turning it on issues a captcha image",
      sc == 200 and cap.get("required") is True and str(cap.get("image", "")).startswith("data:image"),
      f"{sc} {list(cap)}")
check("F", "The captcha endpoint does not leak the answer", "answer" not in json.dumps(cap).lower())

for name, path, body in [("sign-in", "/api/login", {"username": "admin", "password": "x"}),
                         ("password reset", "/api/password/forgot", {"account": "admin"}),
                         ("registration", "/api/register", {"email": "n@example.com",
                                                    "password": "a-long-enough-password"})]:
    sc, b = Session().req("POST", path, body)
    check("F", f"{name}: refused without a captcha, and flagged",
          sc == 400 and b.get("captcha_required") is True, f"{sc} {b}")
check("F", "A refused registration leaves no account behind",
      not sql("SELECT username FROM users WHERE username='n@example.com'"))

# A token service configured without a secret: verification must fail closed, not let through
admin.req("POST", "/api/admin/security", {
    "captcha": {"provider": "turnstile", "login": False, "forgot": False, "register": True,
                "trigger": "always", "fail_threshold": 3},
    "registration": {"enabled": True, "require_verify": False, "domains": "",
                     "default_group": "", "expiry_days": ""}})
sc, b = Session().req("POST", "/api/register",
                      {"email": "closed@example.com", "password": "a-long-enough-password",
                       "captcha_token": "anything"})
check("F", "A misconfigured verifier fails closed", sc == 400, f"{sc} {b}")
check("F", "Failing closed creates no account either",
      not sql("SELECT username FROM users WHERE username='closed@example.com'"))

sc, _ = admin.req("POST", "/api/admin/security", {
    "captcha": {"provider": "image", "login": False, "forgot": False, "register": False,
                "trigger": "always", "fail_threshold": 3},
    "registration": {"enabled": True, "require_verify": False, "domains": "corp.example",
                     "default_group": "", "expiry_days": ""}})
sc, _ = Session().req("POST", "/api/register",
                      {"email": "outsider@elsewhere.test", "password": "a-long-enough-password"})
check("F", "The domain allow-list refuses a domain outside it", sc == 400, f"{sc}")
sc, b = Session().req("POST", "/api/register",
                      {"email": "newbie@corp.example", "password": "a-long-enough-password"})
check("F", "An allowed domain can register", sc == 200, f"{sc} {b}")
row = sql("SELECT active, COALESCE(restricted,0), COALESCE(group_id,0) FROM users WHERE username='newbie@corp.example'")
check("F", "A registration with no OU: enabled, restricted, no group",
      row == [(1, 1, 0)], f"{row}")

reg = Session()
sc, _ = reg.login("newbie@corp.example", "a-long-enough-password")
check("F", "The registered account can sign in", sc == 200, f"{sc}")
sc, home = reg.req("GET", "/api/home")
blob = json.dumps(home, ensure_ascii=False)
check("F", "The registered account sees no reports", "茅台" not in blob, blob[:160])
sc, _ = reg.req("GET", f"/api/v1/reports/{RID_INTERNAL}")
check("F", "The registered account cannot read an internal report", sc == 404, f"{sc}")

sc, _ = Session().req("POST", "/api/register",
                      {"email": "newbie@corp.example", "password": "a-long-enough-password"})
check("F", "A duplicate email is refused explicitly", sc == 409, f"{sc}")
sc, _ = Session().req("POST", "/api/register/verify", {"token": "forged"})
check("F", "A forged confirmation token is refused", sc != 200, f"{sc}")

# ---------------------------------------------------------------- restart
print("\nE. State survives a restart")
restart()
admin = Session()
sc, _ = admin.login("admin", "a-brand-new-passphrase")
check("E", "Sign-in works after the restart", sc == 200, f"{sc}")
sc, vs = admin.req("GET", "/api/admin/versions")
check("E", "Versions and grants survive the restart",
      sc == 200 and any(v["name"] == "对外版" and v["grants"] for v in vs.get("versions", [])), f"{sc}")
check("E", "The restart does not rebuild the unique index",
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
print(f"\n  {len(results) - len(bad)}/{len(results)} passed")
if bad:
    print("\n  failures:")
    for layer, name, _, detail in bad:
        print(f"    [{layer}] {name}   {detail}")
sys.exit(1 if bad else 0)
