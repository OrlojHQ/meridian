// Recognizes a subagent's report to its lead in its own transcript, so the
// thread can show the report as text instead of a raw tool line. Adapters pass
// the call's input through as the tool summary: MCP harnesses send the
// meridian_report arguments as JSON, and Pi runs `meridian-coord report`
// through its bash tool with the summary on stdin.
//
// This is display only. The report text is untrusted agent output, the same
// as the rest of the transcript; the lead receives what the mailbox recorded,
// not what this parser reads.

export type LeadReportStatus = "done" | "blocked" | "failed";

export type LeadReportCall = {
  status?: LeadReportStatus;
  summary: string;
};

// How each harness names the tool: Claude Code `mcp__meridian-coord__…`,
// Antigravity `mcp_meridian-coord_…`, OpenCode `meridian-coord_…`, Codex
// `meridian-coord.…`.
const MCP_REPORT_TOOL = /^(?:mcp__meridian-coord__|mcp_meridian-coord_|meridian-coord[._])?meridian_report$/;
const CLI_REPORT = /(?:^|[\s|;&(])meridian-coord\s+report\b/;
const CLI_STATUS = /--status(?:=|\s+)['"]?(done|blocked|failed)\b/;
// printf '%s' '…' | meridian-coord report, or echo '…' | meridian-coord report.
const CLI_PIPED =
  /(?:printf\s+(?:'[^']*%s[^']*'|"[^"]*%s[^"]*")\s+|echo\s+(?:-[neE]+\s+)?)'((?:[^']|'\\'')*)'\s*\|\s*meridian-coord\s+report\b/;
// A heredoc on the report line: meridian-coord report … <<'EOF'.
const CLI_HEREDOC = /<<-?\s*(['"]?)([A-Za-z_][A-Za-z0-9_]*)\1[^\n]*\n([\s\S]*?)\n[ \t]*\2[ \t]*(?:\n|$)/;

const isStatus = (value: unknown): value is LeadReportStatus =>
  value === "done" || value === "blocked" || value === "failed";

// The JSON object in a summary. ACP harnesses put the tool title before it.
function jsonObject(summary: string): Record<string, unknown> | undefined {
  const start = summary.indexOf("{");
  if (start < 0) return undefined;
  try {
    const value: unknown = JSON.parse(summary.slice(start));
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : undefined;
  } catch {
    return undefined;
  }
}

function cliReport(command: string): LeadReportCall | undefined {
  if (!CLI_REPORT.test(command)) return undefined;
  const status = CLI_STATUS.exec(command)?.[1];
  const piped = CLI_PIPED.exec(command);
  const heredoc = piped ? undefined : CLI_HEREDOC.exec(command);
  const summary = piped ? piped[1].replace(/'\\''/g, "'") : (heredoc?.[3] ?? "");
  return { status: isStatus(status) ? status : undefined, summary: summary.trim() };
}

export function leadReportCall(toolName: string, summary: string): LeadReportCall | undefined {
  if (MCP_REPORT_TOOL.test(toolName)) {
    const input = jsonObject(summary);
    return {
      status: isStatus(input?.status) ? input.status : undefined,
      summary: typeof input?.summary === "string" ? input.summary.trim() : "",
    };
  }
  if (/^(?:bash|shell)$/i.test(toolName)) {
    const command = jsonObject(summary)?.command;
    return typeof command === "string" ? cliReport(command) : undefined;
  }
  return undefined;
}

// Whether the mailbox accepted the report. The tool answers with the
// coordination response, whose JSON says "ok": true. Codex nests that JSON in
// a string, so its quotes arrive escaped.
export const leadReportAccepted = (output = "") => /\\?"ok\\?"\s*:\s*true\b/.test(output);
