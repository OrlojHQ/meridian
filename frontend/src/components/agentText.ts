const CONTROL = /[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g;
const BIDI = /[\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/g;

// Agent-supplied text shown outside the transcript (request kinds, summaries,
// option labels) is one plain line: control characters become spaces,
// bidirectional controls that could reorder what the operator reads are
// removed, and the result is capped at max characters.
export function plainAgentText(value: string | undefined, max: number) {
  const characters = Array.from(
    (value ?? "").replace(CONTROL, " ").replace(BIDI, "").replace(/\s+/g, " ").trim(),
  );
  return characters.length > max
    ? `${characters.slice(0, Math.max(max - 1, 0)).join("")}…`
    : characters.join("");
}
