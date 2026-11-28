import { cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { leadReportCall, leadReportRefused } from "../components/leadReport";
import { buildThreadChat } from "../components/threadChat";
import { transcript } from "./threadFixtures";

const accepted = '{\n  "protocol": "meridian.coord.v1",\n  "ok": true\n}';
const input = JSON.stringify({ status: "done", summary: "A Roman walks into a bar." });

afterEach(cleanup);

describe("Lead report parsing", () => {
  it("reads the report from each harness's tool name and input", () => {
    for (const name of [
      "meridian_report",
      "mcp__meridian-coord__meridian_report",
      "mcp_meridian-coord_meridian_report",
      "meridian-coord_meridian_report",
      "meridian-coord.meridian_report",
    ]) {
      expect(leadReportCall(name, input)).toEqual({ status: "done", summary: "A Roman walks into a bar." });
    }
    // ACP harnesses put the tool title before the input.
    expect(leadReportCall("mcp_meridian-coord_meridian_report", `Run mcp_meridian-coord_meridian_report\n${input}`))
      .toMatchObject({ summary: "A Roman walks into a bar." });
    expect(leadReportCall("other_meridian_report", input)).toBeUndefined();
    expect(leadReportCall("meridian_spawn", input)).toBeUndefined();
    // Truncated input still marks the report, without text.
    expect(leadReportCall("meridian_report", input.slice(0, 20))).toEqual({ status: undefined, summary: "" });
  });

  it("reads Pi's CLI report from its bash command", () => {
    const command = (value: string) => JSON.stringify({ command: value });
    expect(
      leadReportCall("bash", command("printf '%s' 'Fixed it, didn'\\''t touch tests.' | meridian-coord report --status done")),
    ).toEqual({ status: "done", summary: "Fixed it, didn't touch tests." });
    expect(
      leadReportCall("bash", command("meridian-coord report --status=blocked <<'EOF'\nNeed a decision\non the schema.\nEOF\n")),
    ).toEqual({ status: "blocked", summary: "Need a decision\non the schema." });
    expect(leadReportCall("bash", command("meridian-coord report --status failed < /tmp/summary"))).toEqual({
      status: "failed",
      summary: "",
    });
    expect(leadReportCall("bash", command("meridian-coord whoami"))).toBeUndefined();
    expect(leadReportCall("bash", command("grep meridian-coordreport x"))).toBeUndefined();
  });

  it("counts a report as refused only when its output says so", () => {
    expect(leadReportRefused(accepted)).toBe(false);
    // Antigravity may pass back only a status, or nothing readable.
    expect(leadReportRefused("completed")).toBe(false);
    expect(leadReportRefused("")).toBe(false);
    expect(leadReportRefused('{"ok": false, "error": {"code": "invalid_request"}}')).toBe(true);
    expect(leadReportRefused('{"content":"{\\"ok\\": false}"}')).toBe(true);
    expect(leadReportRefused('{"isError":true,"content":[]}')).toBe(true);
    expect(leadReportRefused("invalid arguments: unknown field")).toBe(true);
    expect(leadReportRefused("failed")).toBe(true);
  });
});

describe("Lead report in the subagent thread", () => {
  it("shows the report as its own item and marks the reply after it as not sent", () => {
    const t = transcript();
    t.user("Tell a joke and return it in your final response.");
    t.status("running");
    t.final("a1", "Here it is.");
    t.tool("r1", "mcp_meridian-coord_meridian_report", input);
    t.result("r1", accepted);
    t.final("a2", "A Roman walks into a bar.");
    t.status("idle");
    const items = buildThreadChat(t.blocks, "Running").items;
    expect(items.map((item) => item.kind)).toEqual(["user", "assistant", "lead_report", "assistant"]);
    expect(items[1]).not.toHaveProperty("afterReport");
    expect(items[2]).toMatchObject({ status: "done", summary: "A Roman walks into a bar.", sent: true });
    expect(items[3]).toMatchObject({ afterReport: true });

    // A follow-up starts a new turn: its reply becomes the next report.
    t.user("Another one?");
    t.status("running");
    t.final("a3", "Sure.");
    expect(buildThreadChat(t.blocks, "Running").items.at(-1)).not.toHaveProperty("afterReport");
  });

  it("does not mark replies after a refused or unfinished report", () => {
    const t = transcript();
    t.user("go");
    t.status("running");
    t.tool("r1", "meridian_report", input);
    t.final("a1", "Waiting on the report.");
    expect(buildThreadChat(t.blocks, "Running").items).toMatchObject([
      { kind: "user" },
      { kind: "lead_report", sent: false },
      { kind: "assistant", text: "Waiting on the report." },
    ]);
    expect(buildThreadChat(t.blocks, "Running").items[2]).not.toHaveProperty("afterReport");
    t.result("r1", '{"ok": false}');
    expect(buildThreadChat(t.blocks, "Running").items[1]).toMatchObject({ sent: false });
    expect(buildThreadChat(t.blocks, "Running").items[2]).not.toHaveProperty("afterReport");
  });

  it("counts a finished report as sent when the harness returns only a status", () => {
    const t = transcript();
    t.user("go");
    t.status("running");
    t.tool("r1", "mcp_meridian-coord_meridian_report", input);
    t.result("r1", "completed");
    t.final("a1", "Report submitted.");
    const items = buildThreadChat(t.blocks, "Running").items;
    expect(items[1]).toMatchObject({ kind: "lead_report", sent: true });
    expect(items[2]).toMatchObject({ afterReport: true });
  });

  it("keeps a report out of a collapsed tool group", () => {
    const t = transcript();
    t.user("go");
    t.status("running");
    t.tool("t1", "shell", "ls");
    t.tool("t2", "shell", "pwd");
    t.tool("r1", "meridian_report", input);
    t.tool("t3", "shell", "true");
    const items = buildThreadChat(t.blocks, "Running").items;
    expect(items.map((item) => item.kind)).toEqual(["user", "tools", "lead_report", "tools"]);
  });
});
