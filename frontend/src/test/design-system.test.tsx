import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { ActivityDot } from "../shell/AppShell";
import { Badge } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { ProgressBar, Skeleton } from "../components/ui/loading";
import { applyTheme, readTheme, rememberTheme } from "../components/ui/theme";

describe("design system", () => {
  afterEach(() => {
    cleanup();
    applyTheme("system");
    localStorage.clear();
  });

  it("keeps button variants available to existing controls", () => {
    render(
      <>
        <Button variant="secondary">Decline</Button>
        <Button variant="primary">Production</Button>
        <Button variant="danger">Delete project</Button>
      </>,
    );
    expect(screen.getByRole("button", { name: "Decline" })).toHaveClass("secondary");
    expect(screen.getByRole("button", { name: "Production" })).toHaveClass("primary-button");
    expect(screen.getByRole("button", { name: "Production" })).not.toHaveClass("secondary");
    expect(screen.getByRole("button", { name: "Delete project" })).toHaveClass("danger");
  });

  it("tones badges and activity dots from the shared components", () => {
    const view = render(<Badge tone="ready">Ready</Badge>);
    expect(screen.getByText("Ready")).toHaveClass("badge", "state-ready", "ui-badge");
    view.unmount();
    render(<ActivityDot group="working" />);
    expect(document.querySelector(".activity-dot-working")).toBeTruthy();
  });

  it("exposes preparation progress and a skeleton status", () => {
    render(
      <>
        <ProgressBar value={46} max={100} label="Preparation progress" />
        <div role="status">
          <Skeleton className="skeleton-line" />
          <span className="sr-only">Loading Capsule…</span>
        </div>
      </>,
    );
    expect(screen.getByRole("progressbar", { name: "Preparation progress" })).toHaveAttribute(
      "aria-valuenow",
      "46",
    );
    expect(screen.getByRole("status")).toHaveTextContent("Loading Capsule");
    expect(document.querySelector(".skeleton")).toBeTruthy();
  });

  it("switches the document between light and dark themes", async () => {
    rememberTheme("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(readTheme()).toBe("dark");
    rememberTheme("light");
    expect(document.documentElement.dataset.theme).toBe("light");
    rememberTheme("system");
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(readTheme()).toBe("system");

    function ThemeButton() {
      return (
        <button type="button" onClick={() => applyTheme("dark")}>
          Use dark theme
        </button>
      );
    }
    render(<ThemeButton />);
    await userEvent.click(screen.getByRole("button", { name: "Use dark theme" }));
    expect(document.documentElement.dataset.theme).toBe("dark");
  });
});
