// Centralizes extraction of a display message from an unknown thrown value
// (Wails binding errors, Error objects, {error: ...} payloads, plain
// strings) so call sites stop repeating the `e?.message || e` idiom.
// Returns '' when nothing printable can be extracted.
export const errMsg = (e: unknown): string => {
  if (e instanceof Error) return e.message
  if (typeof e === 'string') return e
  const obj = e as { message?: unknown; error?: unknown } | null
  if (obj && typeof obj.message === 'string' && obj.message) return obj.message
  if (obj && typeof obj.error === 'string' && obj.error) return obj.error
  if (e == null || typeof e === 'object') return ''
  return String(e)
}
