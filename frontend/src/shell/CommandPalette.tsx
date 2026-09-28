import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";

import { HarnessMark } from "../components/ui/HarnessMark";
import { CloseIcon, SearchIcon } from "../components/ui/Icons";
import {
  keyboardShortcuts,
  paletteSections,
  type PaletteCommand,
} from "./paletteCommands";

// Modal surfaces return focus to wherever it was when they opened.
function useRestoreFocus() {
  const [previous] = useState(() => document.activeElement);
  useEffect(
    () => () => {
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    },
    [previous],
  );
}

function Keys({ keys }: { keys: string[] }) {
  return (
    <span className="key-combo" aria-hidden="true">
      {keys.map((key) => (
        <kbd key={key}>{key}</kbd>
      ))}
    </span>
  );
}

export function CommandPalette({
  commands,
  recent,
  onRun,
  onClose,
}: {
  commands: PaletteCommand[];
  recent: string[];
  onRun: (command: PaletteCommand) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const baseId = useId();
  const listId = `${baseId}-list`;
  const optionPrefix = `${baseId}-option-`;
  const optionId = (index: number) => `${optionPrefix}${index}`;
  useRestoreFocus();

  const sections = useMemo(
    () => paletteSections(commands, query, recent),
    [commands, query, recent],
  );
  const flat = useMemo(() => sections.flatMap((section) => section.commands), [sections]);
  const activeIndex = Math.min(active, flat.length - 1);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    if (activeIndex < 0) return;
    document
      .getElementById(`${optionPrefix}${activeIndex}`)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [activeIndex, optionPrefix]);

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      onClose();
      return;
    }
    if (event.key === "Tab") {
      event.preventDefault();
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      const command = flat[activeIndex];
      if (command) onRun(command);
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    event.preventDefault();
    if (flat.length === 0) return;
    const step = event.key === "ArrowDown" ? 1 : -1;
    setActive((Math.max(activeIndex, 0) + step + flat.length) % flat.length);
  };

  let index = -1;
  return (
    <div className="dialog-backdrop palette-backdrop" onMouseDown={onClose}>
      <section
        className="command-palette"
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="palette-search">
          <SearchIcon />
          <input
            ref={inputRef}
            role="combobox"
            aria-label="Search commands"
            aria-expanded="true"
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={activeIndex >= 0 ? optionId(activeIndex) : undefined}
            autoComplete="off"
            spellCheck={false}
            placeholder="Jump to a Capsule or run a command…"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setActive(0);
            }}
            onKeyDown={onKeyDown}
          />
          <kbd aria-hidden="true">Esc</kbd>
        </div>
        <div className="palette-results" id={listId} role="listbox" aria-label="Commands">
          {sections.map((section, sectionIndex) => (
            <div
              key={section.group}
              role="group"
              aria-labelledby={`${baseId}-group-${sectionIndex}`}
              className="palette-group"
            >
              <div
                id={`${baseId}-group-${sectionIndex}`}
                role="presentation"
                className="palette-group-label"
              >
                {section.group}
              </div>
              {section.commands.map((command) => {
                index += 1;
                const position = index;
                const selected = position === activeIndex;
                return (
                  <div
                    key={command.id}
                    id={optionId(position)}
                    role="option"
                    aria-selected={selected}
                    aria-keyshortcuts={command.shortcut?.join("+")}
                    className={`palette-option${selected ? " active" : ""}${
                      command.danger ? " danger" : ""
                    }`}
                    onMouseDown={(event) => event.preventDefault()}
                    onMouseMove={() => {
                      if (!selected) setActive(position);
                    }}
                    onClick={() => onRun(command)}
                  >
                    <span className="palette-option-mark" aria-hidden="true">
                      {command.activity && (
                        <span className={`state-dot activity-dot-${command.activity}`} />
                      )}
                      {command.harness && <HarnessMark harness={command.harness} />}
                    </span>
                    <span className="palette-option-copy">
                      <span className="palette-option-label">{command.label}</span>
                      {command.detail && (
                        <small className="palette-option-detail">{command.detail}</small>
                      )}
                    </span>
                    {command.shortcut && <Keys keys={command.shortcut} />}
                  </div>
                );
              })}
            </div>
          ))}
        </div>
        {flat.length === 0 && (
          <p className="palette-empty" role="status">
            No matching commands
          </p>
        )}
        <footer className="palette-footer" aria-hidden="true">
          <span><kbd>↑</kbd><kbd>↓</kbd> move</span>
          <span><kbd>Enter</kbd> run</span>
          <span><kbd>Esc</kbd> close</span>
        </footer>
      </section>
    </div>
  );
}

export function KeyboardShortcutsDialog({ onClose }: { onClose: () => void }) {
  const closeRef = useRef<HTMLButtonElement>(null);
  useRestoreFocus();
  useEffect(() => {
    closeRef.current?.focus();
  }, []);
  return (
    <div className="dialog-backdrop" onMouseDown={onClose}>
      <section
        className="launch-dialog shortcuts-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="shortcuts-title"
        onMouseDown={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            onClose();
          } else if (event.key === "Tab") {
            event.preventDefault();
          }
        }}
      >
        <header className="dialog-header">
          <h1 id="shortcuts-title">Keyboard shortcuts</h1>
          <button
            ref={closeRef}
            type="button"
            className="icon-button"
            aria-label="Close keyboard shortcuts"
            onClick={onClose}
          >
            <CloseIcon />
          </button>
        </header>
        <div className="dialog-body">
          <dl className="shortcut-list">
            {keyboardShortcuts().map(({ keys, description }) => (
              <div key={description}>
                <dt>
                  {keys.map((combo, position) => (
                    <span key={combo.join("+")}>
                      {position > 0 && <span className="shortcut-or"> or </span>}
                      <Keys keys={combo} />
                      <span className="sr-only">{combo.join(" ")}</span>
                    </span>
                  ))}
                </dt>
                <dd>{description}</dd>
              </div>
            ))}
          </dl>
          <p className="muted-copy">
            Single-key shortcuts apply only while focus is outside terminals and
            form fields. Inside a terminal, Ctrl+K stays with the shell; on
            macOS, ⌘K opens the palette from anywhere.
          </p>
        </div>
      </section>
    </div>
  );
}
