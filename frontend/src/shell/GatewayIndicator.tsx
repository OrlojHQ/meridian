import { useQuery } from "@tanstack/react-query";

import { queries } from "../api/queries";
import { GatewayIcon } from "../components/ui/Icons";
import { desktopBridge, type DesktopGateway } from "./desktop";

function validGateway(value: unknown): DesktopGateway | undefined {
  if (!value || typeof value !== "object") return undefined;
  const { label, local } = value as Record<string, unknown>;
  if (typeof label !== "string" || label.trim() === "" || label.length > 300) return undefined;
  return { label, local: local === true };
}

// Names the gateway this desktop window is on, and in team mode who is
// signed in, so work is not created on the wrong installation. Clicking it
// opens the shell's gateway picker. In a browser the address bar already
// says where the page is, and there is no picker, so this renders nothing.
export function GatewayIndicator() {
  const bridge = desktopBridge();
  const read = bridge?.gateway;
  const gateway = useQuery({
    queryKey: ["desktop-gateway"],
    queryFn: async () => validGateway(await read?.()) ?? null,
    enabled: Boolean(read),
    staleTime: Infinity,
    retry: false,
  });
  const mode = useQuery({ ...queries.authenticationMode(), enabled: Boolean(read) });
  const team = mode.data?.mode === "team";
  const member = useQuery({ ...queries.currentMember(), enabled: Boolean(read) && team, retry: false });
  if (!read || !gateway.data) return null;

  const who = team && member.data ? member.data.displayName || member.data.email : undefined;
  const text = who ? `${gateway.data.label} · ${who}` : gateway.data.label;
  const title = `Gateway: ${gateway.data.label}${who ? `, signed in as ${who}` : ""}`;
  const open = bridge?.openGatewaySettings;
  if (!open) {
    return (
      <span className="status-gateway" title={title}>
        <GatewayIcon aria-hidden="true" />
        <span className="status-gateway-label">{text}</span>
      </span>
    );
  }
  return (
    <button
      type="button"
      className="status-gateway"
      title={`${title}. Switch gateway`}
      aria-label={`${title}. Switch gateway`}
      onClick={() => open()}
    >
      <GatewayIcon aria-hidden="true" />
      <span className="status-gateway-label">{text}</span>
    </button>
  );
}
