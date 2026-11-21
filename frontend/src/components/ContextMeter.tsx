import { harnessName } from "../shell/capsuleActivity";
import { contextDetail, contextLabel, contextPercent, type ContextUsage } from "./contextUsage";
import { safeInline } from "./threadText";

// The plain-Thread context meter. It appears from half full and names what the
// operator can do: compact through the harness's own command, or start fresh.
export function ContextMeter({ usage, harness }: { usage: ContextUsage; harness: string }) {
  const percent = contextPercent(usage);
  const label = contextLabel(usage);
  const agent = safeInline(harnessName(harness)) || "the agent";
  return (
    <span
      className={percent >= 85 ? "context-meter context-meter-high" : "context-meter"}
      role="meter"
      aria-label="Context used"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
      aria-valuetext={label}
      title={`${contextDetail(usage)}. Compact with ${agent}'s command in the / menu, or Start fresh.`}
    >
      <span className="context-meter-bar" aria-hidden="true">
        <span style={{ width: `${percent}%` }} />
      </span>
      <span className="context-meter-label">{label}</span>
    </span>
  );
}
