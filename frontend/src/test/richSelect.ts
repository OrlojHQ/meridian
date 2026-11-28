import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

type User = {
  click: (element: Element) => Promise<void>;
  keyboard: (text: string) => Promise<unknown>;
};

// choose opens a RichSelect and picks the option with the given label.
export async function choose(field: HTMLElement, label: string, user: User = userEvent) {
  await user.click(field);
  await user.click(screen.getByRole("option", { name: label }));
}

// optionLabels opens a RichSelect, reads its option labels, and closes it.
export async function optionLabels(field: HTMLElement, user: User = userEvent) {
  await user.click(field);
  const labels = screen
    .getAllByRole("option")
    .map((option) => option.querySelector(".rich-select-option-label")?.textContent ?? "");
  await user.keyboard("{Escape}");
  return labels;
}
