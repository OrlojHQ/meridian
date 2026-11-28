import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
} from "react";

import { CheckIcon } from "./Icons";

export type RichSelectOption<T extends string> = {
  value: T;
  label: string;
  description?: string;
  icon?: ReactNode;
};

function Chevron() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.6">
      <path d="m4.5 6.25 3.5 3.5 3.5-3.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

// RichSelect is a single-choice select whose options carry an icon and a
// short description. The desktop app renders native <select> menus with the
// platform's own popup, which cannot show images, so choices that are best
// told apart by a brand mark use this listbox instead. It follows the ARIA
// select-only combobox pattern: focus stays on the button and the active
// option is announced through aria-activedescendant.
//
// The menu is fixed to the viewport, placed from the button, so a dialog's
// or panel's scroll area cannot clip it. It opens upward when there is more
// room above, and follows the button while anything scrolls.
export function RichSelect<T extends string>({
  value,
  options,
  onChange,
  disabled,
  labelledBy,
  ariaLabel,
  name,
  align = "start",
  className,
}: {
  value: T;
  options: RichSelectOption<T>[];
  onChange: (value: T) => void;
  disabled?: boolean;
  // Name the field with labelledBy (a visible label's id) or ariaLabel.
  labelledBy?: string;
  ariaLabel?: string;
  // name submits the value with an enclosing form, like a select's name.
  name?: string;
  // align "end" opens the menu from the field's right edge, for fields at
  // the end of a row.
  align?: "start" | "end";
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [placement, setPlacement] = useState<CSSProperties>();
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const selectedIndex = Math.max(
    0,
    options.findIndex((option) => option.value === value),
  );
  const selected = options[selectedIndex];
  const optionId = (index: number) => `${id}-option-${index}`;
  // Rows without an icon keep the text aligned with rows that have one.
  const iconColumn = options.some((option) => option.icon !== undefined);

  useEffect(() => {
    if (!open) return;
    const close = (event: MouseEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    window.addEventListener("mousedown", close);
    return () => window.removeEventListener("mousedown", close);
  }, [open]);

  useLayoutEffect(() => {
    if (!open) return;
    const place = () => {
      const field = button.current?.getBoundingClientRect();
      if (!field) return;
      const gap = 6;
      const margin = 8;
      const below = window.innerHeight - field.bottom - gap - margin;
      const above = field.top - gap - margin;
      const wanted = list.current?.scrollHeight ?? 0;
      const upward = wanted > below && above > below;
      const next: CSSProperties = { maxHeight: Math.min(384, upward ? above : below) };
      if (upward) next.bottom = window.innerHeight - field.top + gap;
      else next.top = field.bottom + gap;
      if (align === "end") {
        next.right = window.innerWidth - field.right;
        next.minWidth = Math.max(field.width, 272);
      } else {
        next.left = field.left;
        next.width = field.width;
      }
      setPlacement(next);
    };
    place();
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [open, align]);

  useEffect(() => {
    if (!open) return;
    list.current
      ?.querySelector<HTMLElement>(`#${CSS.escape(optionId(active))}`)
      ?.scrollIntoView?.({ block: "nearest" });
  });

  const show = (index = selectedIndex) => {
    setActive(index);
    setOpen(true);
  };
  const choose = (index: number) => {
    const option = options[index];
    setOpen(false);
    button.current?.focus();
    if (option && option.value !== value) onChange(option.value);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    const last = options.length - 1;
    if (!open) {
      if (["ArrowDown", "ArrowUp", "Enter", " "].includes(event.key)) {
        event.preventDefault();
        show();
      }
      return;
    }
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        setActive((index) => Math.min(last, index + 1));
        return;
      case "ArrowUp":
        event.preventDefault();
        setActive((index) => Math.max(0, index - 1));
        return;
      case "Home":
        event.preventDefault();
        setActive(0);
        return;
      case "End":
        event.preventDefault();
        setActive(last);
        return;
      case "Enter":
      case " ":
        event.preventDefault();
        choose(active);
        return;
      case "Escape":
        event.preventDefault();
        event.stopPropagation();
        setOpen(false);
        return;
      case "Tab":
        setOpen(false);
        return;
    }
    // Type-ahead: jump to the next option whose label starts with the key.
    if (event.key.length === 1 && /\S/.test(event.key)) {
      const key = event.key.toLowerCase();
      for (let step = 1; step <= options.length; step++) {
        const index = (active + step) % options.length;
        if (options[index].label.toLowerCase().startsWith(key)) {
          setActive(index);
          return;
        }
      }
    }
  };

  return (
    <div
      ref={root}
      className={["rich-select", open && "open", className].filter(Boolean).join(" ")}
    >
      {name !== undefined && <input type="hidden" name={name} value={value} />}
      <button
        ref={button}
        type="button"
        role="combobox"
        className="rich-select-button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={`${id}-list`}
        aria-labelledby={labelledBy ? `${labelledBy} ${id}-value` : undefined}
        aria-label={labelledBy ? undefined : ariaLabel}
        aria-activedescendant={open ? optionId(active) : undefined}
        disabled={disabled}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={onKeyDown}
      >
        {selected?.icon}
        <span id={`${id}-value`} className="rich-select-value">
          {selected?.label}
        </span>
        <span className="rich-select-chevron">
          <Chevron />
        </span>
      </button>
      {open && (
        <ul
          ref={list}
          id={`${id}-list`}
          role="listbox"
          aria-labelledby={labelledBy}
          aria-label={labelledBy ? undefined : ariaLabel}
          className="rich-select-menu"
          style={placement}
          tabIndex={-1}
        >
          {options.map((option, index) => (
            <li
              key={option.value}
              id={optionId(index)}
              role="option"
              aria-selected={option.value === value}
              aria-labelledby={`${optionId(index)}-label`}
              aria-describedby={option.description ? `${optionId(index)}-description` : undefined}
              className={index === active ? "rich-select-option active" : "rich-select-option"}
              onMouseDown={(event) => event.preventDefault()}
              onMouseMove={() => setActive(index)}
              onClick={() => choose(index)}
            >
              {option.icon ??
                (iconColumn && <span className="rich-select-icon-blank" aria-hidden="true" />)}
              <span className="rich-select-option-text">
                <span id={`${optionId(index)}-label`} className="rich-select-option-label">
                  {option.label}
                </span>
                {option.description && (
                  <span
                    id={`${optionId(index)}-description`}
                    className="rich-select-option-description"
                  >
                    {option.description}
                  </span>
                )}
              </span>
              <span className="rich-select-check">
                {option.value === value && <CheckIcon />}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
