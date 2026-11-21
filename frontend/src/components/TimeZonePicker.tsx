import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { createPortal } from "react-dom";

import {
  filterTimeZones,
  timeZoneChoices,
  type TimeZoneChoice,
} from "./routineText";
import { Input } from "./ui/Field";
import { CheckIcon } from "./ui/Icons";

type MenuBox = { top: number; left: number; width: number; maxHeight: number };

// The menu is portaled so the page scroller does not clip it, and fixed so
// opening it does not change the form's layout.
function placeMenu(anchor: DOMRect): MenuBox {
  const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
  const margin = 8;
  const gap = 6;
  const width = Math.min(Math.max(anchor.width, 22 * rem), window.innerWidth - margin * 2);
  let left = anchor.left;
  if (left + width > window.innerWidth - margin) left = window.innerWidth - margin - width;
  if (left < margin) left = margin;
  const below = window.innerHeight - anchor.bottom - margin - gap;
  const above = anchor.top - margin - gap;
  const preferBelow = below >= 12 * rem || below >= above;
  const space = Math.max(preferBelow ? below : above, 0);
  const maxHeight = Math.min(20 * rem, Math.max(space, 96));
  const top = preferBelow
    ? anchor.bottom + gap
    : Math.max(margin, anchor.top - gap - maxHeight);
  return { top, left, width, maxHeight };
}

function optionMeta(choice: TimeZoneChoice) {
  const bits: string[] = [];
  if (choice.local) bits.push("Your local zone");
  if (choice.time) bits.push(choice.time);
  else if (choice.savedOnly) bits.push("Saved zone");
  return bits.join(" · ");
}

function scrollOption(menu: HTMLElement, option: HTMLElement) {
  const menuRect = menu.getBoundingClientRect();
  const optionRect = option.getBoundingClientRect();
  if (optionRect.top < menuRect.top) menu.scrollTop -= menuRect.top - optionRect.top;
  else if (optionRect.bottom > menuRect.bottom) menu.scrollTop += optionRect.bottom - menuRect.bottom;
}

// Time zone combobox. Typing filters the list. A zone is saved only by
// choosing an option, so a typed name never becomes the routine's zone.
export function TimeZonePicker({
  value,
  onChange,
}: {
  value: string;
  onChange: (zone: string) => void;
}) {
  const listId = useId();
  const root = useRef<HTMLDivElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const followCursor = useRef(false);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const [box, setBox] = useState<MenuBox | null>(null);
  const choices = useMemo(() => timeZoneChoices(new Date(), value), [value]);
  const filtered = useMemo(
    () => filterTimeZones(choices, open ? query : ""),
    [choices, open, query],
  );
  const active = filtered.length === 0 ? 0 : Math.min(cursor, filtered.length - 1);

  function openMenu() {
    const index = choices.findIndex((choice) => choice.id === value);
    followCursor.current = true;
    setQuery("");
    setCursor(index >= 0 ? index : 0);
    setOpen(true);
  }
  function closeMenu() {
    setQuery("");
    setOpen(false);
  }
  function choose(id: string) {
    onChange(id);
    closeMenu();
  }

  useLayoutEffect(() => {
    if (!open) return;
    const update = () => {
      const anchor = root.current?.getBoundingClientRect();
      if (!anchor) return;
      const next = placeMenu(anchor);
      setBox((current) =>
        current &&
        current.top === next.top &&
        current.left === next.left &&
        current.width === next.width &&
        current.maxHeight === next.maxHeight
          ? current
          : next,
      );
    };
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [open]);

  useLayoutEffect(() => {
    const list = menu.current;
    if (!open || !list || !followCursor.current) return;
    followCursor.current = false;
    const option = document.getElementById(`${listId}-option-${active}`);
    if (option) scrollOption(list, option);
    else list.scrollTop = 0;
  }, [open, box, active, query, listId]);

  useEffect(() => {
    if (!open) return;
    const onPointer = (event: PointerEvent) => {
      const target = event.target as Node;
      if (root.current?.contains(target) || menu.current?.contains(target)) return;
      closeMenu();
    };
    document.addEventListener("pointerdown", onPointer);
    return () => document.removeEventListener("pointerdown", onPointer);
  }, [open]);

  function onKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    // While the menu is closed the field shows the chosen zone. The first
    // typed character starts a search instead of editing that name.
    if (!open && event.key.length === 1 && !event.altKey && !event.ctrlKey && !event.metaKey) {
      event.preventDefault();
      followCursor.current = true;
      setQuery(event.key);
      setCursor(0);
      setOpen(true);
      return;
    }
    if (!open && event.key === "Backspace") {
      event.preventDefault();
      followCursor.current = true;
      setQuery("");
      setCursor(0);
      setOpen(true);
      return;
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (!open) {
        openMenu();
        return;
      }
      if (filtered.length === 0) return;
      const next = event.key === "ArrowDown" ? active + 1 : active - 1;
      followCursor.current = true;
      setCursor(Math.min(Math.max(next, 0), filtered.length - 1));
      return;
    }
    if (event.key === "Home" && open) {
      event.preventDefault();
      followCursor.current = true;
      setCursor(0);
      return;
    }
    if (event.key === "End" && open) {
      event.preventDefault();
      followCursor.current = true;
      setCursor(Math.max(filtered.length - 1, 0));
      return;
    }
    if (event.key === "Enter") {
      if (!open) return;
      event.preventDefault();
      const choice = filtered[active];
      if (choice) choose(choice.id);
      return;
    }
    if (event.key === "Escape" && open) {
      event.preventDefault();
      event.stopPropagation();
      closeMenu();
    }
  }

  const local = filtered.filter((choice) => choice.local);
  const saved = filtered.filter((choice) => choice.savedOnly && !choice.local);
  const rest = filtered.filter((choice) => !choice.local && !choice.savedOnly);
  const groups: { title: string; choices: TimeZoneChoice[] }[] = [];
  for (const choice of rest) {
    const title = choice.region;
    const current = groups[groups.length - 1];
    if (current && current.title === title) current.choices.push(choice);
    else groups.push({ title, choices: [choice] });
  }
  const indexOf = new Map(filtered.map((choice, index) => [choice.id, index]));

  function renderOption(choice: TimeZoneChoice) {
    const index = indexOf.get(choice.id) ?? 0;
    const selected = choice.id === value;
    return (
      <div
        key={choice.id}
        id={`${listId}-option-${index}`}
        role="option"
        data-zone={choice.id}
        aria-selected={selected}
        className={index === active ? "tz-picker-option active" : "tz-picker-option"}
        onMouseEnter={() => setCursor((current) => (current === index ? current : index))}
        onMouseDown={(event) => {
          event.preventDefault();
          choose(choice.id);
        }}
      >
        <span className="tz-picker-option-text">
          <span className="tz-picker-option-name">{choice.id}</span>
          {choice.offset && <span className="tz-picker-option-offset">{choice.offset}</span>}
          <span className="tz-picker-option-meta">{optionMeta(choice)}</span>
        </span>
        <span className="tz-picker-check">{selected && <CheckIcon />}</span>
      </div>
    );
  }

  return (
    <div className="tz-picker" ref={root}>
      <Input
        role="combobox"
        aria-label="Time zone"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-autocomplete="list"
        aria-activedescendant={open && filtered[active] ? `${listId}-option-${active}` : undefined}
        className="tz-picker-input"
        placeholder={open ? "Search time zones" : undefined}
        autoComplete="off"
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        value={open ? query : value}
        title={open ? undefined : value}
        onFocus={() => {
          if (!open) openMenu();
        }}
        onMouseDown={(event) => {
          if (document.activeElement !== event.currentTarget || open) return;
          openMenu();
        }}
        onChange={(event) => {
          followCursor.current = true;
          setQuery(event.target.value);
          setCursor(0);
          setOpen(true);
        }}
        onBlur={(event) => {
          const next = event.relatedTarget as Node | null;
          if (next && (root.current?.contains(next) || menu.current?.contains(next))) return;
          if (!next) return;
          closeMenu();
        }}
        onKeyDown={onKeyDown}
      />
      {open && box && createPortal(
        <div
          ref={menu}
          id={listId}
          role="listbox"
          aria-label="Time zones"
          className="tz-picker-menu"
          style={{ top: box.top, left: box.left, width: box.width, maxHeight: box.maxHeight }}
        >
          {filtered.length === 0 ? (
            <p className="tz-picker-empty">No time zones match “{query.trim()}”</p>
          ) : (
            <>
              {local.length > 0 && <div className="tz-picker-options">{local.map(renderOption)}</div>}
              {saved.length > 0 && (
                <div className="tz-picker-section" role="group" aria-label="Saved">
                  <p className="tz-picker-group">Saved</p>
                  <div className="tz-picker-options">{saved.map(renderOption)}</div>
                </div>
              )}
              {groups.map((group) => (
                <div
                  key={group.title || "utc"}
                  className="tz-picker-section"
                  role="group"
                  aria-label={group.title || "UTC"}
                >
                  {group.title && <p className="tz-picker-group">{group.title}</p>}
                  <div className="tz-picker-options">{group.choices.map(renderOption)}</div>
                </div>
              ))}
            </>
          )}
        </div>,
        document.body,
      )}
    </div>
  );
}
