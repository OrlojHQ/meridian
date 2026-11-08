const MAX_CONTENT = 64 * 1024;

// Control characters and bidi overrides in agent output can spoof layout or
// smuggle instructions; strip them before any transcript text is shown.
const HOSTILE_CHARS =
  /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u061c\u200b-\u200f\u202a-\u202e\u2066-\u2069\ufeff]/g;

export const sanitizeThreadText = (value = "") => value.replace(HOSTILE_CHARS, "");

export const boundedThreadText = (value = "") => {
  const safe = sanitizeThreadText(value);
  return safe.length > MAX_CONTENT
    ? `${safe.slice(0, MAX_CONTENT)}\n… content truncated …`
    : safe;
};

export const safeInline = (value = "") =>
  sanitizeThreadText(value).replace(/[\n\t]+/g, " ");
