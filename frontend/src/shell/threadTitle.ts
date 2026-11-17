import { sanitizeThreadText, safeInline } from "../components/threadText";

const GENERATED_NAME = /^thread-[0-9a-f]{6,64}$/i;
const TITLE_LIMIT = 48;

export function isGeneratedThreadName(name: string) {
  return GENERATED_NAME.test(name.trim());
}

// A short label from the operator's first message. No model is involved:
// collapse whitespace, drop light markup, and cut on a word.
export function threadTitleFromMessage(content: string, limit = TITLE_LIMIT) {
  const text = safeInline(content)
    .replace(/^#+\s*/, "")
    .replace(/[`*_~>]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (!text) return "";
  const sentence = text.split(/(?<=[.!?])\s/)[0] ?? text;
  const source = sentence.length <= limit ? sentence : text;
  const clipped = source.length <= limit
    ? source
    : source.slice(0, limit);
  const word = clipped.length === source.length || clipped.lastIndexOf(" ") <= 16
    ? clipped
    : clipped.slice(0, clipped.lastIndexOf(" "));
  return word.replace(/[.,:;!-]+$/g, "").trim();
}

export function displayThreadTitle(capsuleName: string, firstMessage = "") {
  if (isGeneratedThreadName(capsuleName)) {
    const title = threadTitleFromMessage(firstMessage);
    if (title) return title;
  }
  return capsuleName;
}

export function pullRequestTitleFromMessage(content: string, fallback: string) {
  const title = threadTitleFromMessage(content, 72) || fallback.trim();
  return title.replace(/[\r\n]+/g, " ").slice(0, 120);
}

export function pullRequestBodyFromMessage(content: string) {
  return sanitizeThreadText(content).replace(/\u0000/g, "").trim().slice(0, 4000);
}

export function firstUserText(
  blocks: { role?: string; kind?: string; content?: string }[] = [],
) {
  for (const block of blocks) {
    if (block.role === "user" && block.kind === "text" && block.content?.trim()) {
      return block.content;
    }
  }
  return "";
}
