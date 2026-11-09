// Signature check examples shown after a webhook secret is created or
// rotated (ADR 0024 section 7). The header is
//   X-LogSage-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256 of "t.body">
// and a receiver rejects timestamps more than 5 minutes away. The tests run
// this exact code against the backend's known vectors.

export const NODE_EXAMPLE = `const crypto = require('node:crypto')

// secret: the channel's signing secret (lsw_...), from your secret store.
// header: the X-LogSage-Signature header. body: the raw request body (a Buffer).
function verifyLogSage(secret, header, body, nowSeconds = Math.floor(Date.now() / 1000)) {
  const parts = Object.fromEntries(header.split(',').map((p) => p.trim().split('=', 2)))
  const t = Number(parts.t)
  if (!Number.isInteger(t) || !/^[0-9a-f]{64}$/.test(parts.v1 || '')) return false
  if (Math.abs(nowSeconds - t) > 300) return false // older than 5 minutes
  const want = crypto.createHmac('sha256', secret).update(parts.t + '.').update(body).digest()
  return crypto.timingSafeEqual(want, Buffer.from(parts.v1, 'hex'))
}

// Express: app.post('/logsage', express.raw({ type: 'application/json' }), (req, res) => {
//   if (!verifyLogSage(process.env.LOGSAGE_SECRET, req.get('X-LogSage-Signature') || '', req.body)) return res.sendStatus(401)
//   res.sendStatus(204)
// })`

export const PYTHON_EXAMPLE = `import hashlib, hmac, re, time

# secret: the channel's signing secret (lsw_...), from your secret store.
# header: the X-LogSage-Signature header. body: the raw request body (bytes).
def verify_logsage(secret, header, body, now=None):
    parts = dict(p.strip().split("=", 1) for p in header.split(",") if "=" in p)
    t, v1 = parts.get("t", ""), parts.get("v1", "")
    if not t.isdigit() or not re.fullmatch(r"[0-9a-f]{64}", v1):
        return False
    now = int(time.time()) if now is None else now
    if abs(now - int(t)) > 300:  # older than 5 minutes
        return False
    want = hmac.new(secret.encode(), t.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, v1)`
