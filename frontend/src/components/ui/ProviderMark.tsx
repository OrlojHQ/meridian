import type { ProviderConnectionType } from "../../api/generated/types.gen";
// Official Claude mark, unchanged, from https://claude.com/icon.svg. It
// identifies Claude connections only; Anthropic's own mark is not used.
import claudeMark from "../../assets/claude-mark.svg";
import { harnessLogos } from "./HarnessMark";

// ProviderMark identifies the service behind a provider connection. Claude
// connections use Claude's mark. OpenAI and ChatGPT connections use OpenAI's
// Blossom under the same terms as the Codex harness mark (black or white
// only, with its clear space). Self-hosted endpoints are not a brand, so they
// get a neutral server glyph.
export function ProviderMark({
  provider,
  size = "md",
}: {
  provider: ProviderConnectionType;
  size?: "sm" | "md";
}) {
  const className = `provider-mark provider-mark-${size}`;
  if (provider === "anthropic") {
    return (
      <span className={`${className} provider-mark-claude`} aria-hidden="true">
        <img src={claudeMark} alt="" />
      </span>
    );
  }
  if (provider === "openai" || provider === "chatgpt") {
    const blossom = harnessLogos.codex;
    return (
      <span className={`${className} provider-mark-openai`} aria-hidden="true">
        <svg viewBox={blossom.viewBox} focusable="false">
          <path d={blossom.path} fill="currentColor" />
        </svg>
      </span>
    );
  }
  return (
    <span className={`${className} provider-mark-endpoint`} aria-hidden="true">
      <svg
        viewBox="0 0 20 20"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        focusable="false"
      >
        <rect x="3.5" y="3.5" width="13" height="5" rx="1.25" />
        <rect x="3.5" y="11.5" width="13" height="5" rx="1.25" />
        <path d="M6.5 6h.01M6.5 14h.01M10 6h3.5M10 14h3.5" />
      </svg>
    </span>
  );
}
