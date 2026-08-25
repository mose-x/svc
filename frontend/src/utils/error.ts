import i18n from '../i18n'

// Matches backend apperr markers: "[svc:<code>]{json-params}". The Go
// backend has no localization layer — user-facing errors carry a stable
// code (internal/apperr) that the frontend translates from its i18n
// catalog. Unknown codes or malformed payloads fall back to the raw text so
// nothing is ever swallowed.
const SVC_ERROR_RE = /^\[svc:([a-z0-9-]+)\](.*)$/s

// Centralizes extraction of a display message from an unknown thrown value
// (Wails binding errors, Error objects, {error: ...} payloads, plain
// strings) so call sites stop repeating the `e?.message || e` idiom.
// Returns '' when nothing printable can be extracted.
export const errMsg = (e: unknown): string => {
  const raw = extractMessage(e)
  const match = raw.match(SVC_ERROR_RE)
  if (!match) return raw
  const [, code, rest] = match
  const key = `errors.${code}`
  if (!i18n.exists(key)) return raw
  let params: Record<string, string> = {}
  if (rest) {
    try {
      params = JSON.parse(rest)
    } catch {
      return raw
    }
  }
  return i18n.t(key, params)
}

const extractMessage = (e: unknown): string => {
  if (e instanceof Error) return e.message
  if (typeof e === 'string') return e
  const obj = e as { message?: unknown; error?: unknown } | null
  if (obj && typeof obj.message === 'string' && obj.message) return obj.message
  if (obj && typeof obj.error === 'string' && obj.error) return obj.error
  if (e == null || typeof e === 'object') return ''
  return String(e)
}
