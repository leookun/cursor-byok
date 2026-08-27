import { autoUpdate, computePosition, flip, offset, shift, size } from "@floating-ui/dom";
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { VirtualList } from "../virtual/VirtualList";
import type { VirtualListApi } from "../virtual/virtualTypes";
import { Icon, type IconProps } from "./Icon";
import { checkIcon, chevronDownIcon } from "./icons";
import styles from "./Select.module.scss";

export type SelectOption = { value: string; label: string; icon?: IconProps["icon"] };

const MENU_MAX_HEIGHT = 240;
const MENU_ITEM_HEIGHT = 30;
const MENU_SEARCH_HEIGHT = 36;

export function Select({ value, options, disabled, ariaLabel, searchable, onChange }: {
  value: string;
  options: SelectOption[];
  disabled?: boolean;
  ariaLabel: string;
  searchable?: boolean;
  onChange: (value: string) => void;
}) {
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const listApi = useRef<VirtualListApi | null>(null);
  const menuId = useId();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [position, setPosition] = useState({ left: 0, top: 0, width: 0, maxHeight: MENU_MAX_HEIGHT });
  const selected = options.find((option) => option.value === value);
  const showSearch = Boolean(searchable) && options.length > 6;
  const filtered = useMemo(() => {
    if (!showSearch) return options;
    const normalized = query.trim().toLocaleLowerCase();
    if (!normalized) return options;
    return options.filter((option) => option.label.toLocaleLowerCase().includes(normalized) || option.value.toLocaleLowerCase().includes(normalized));
  }, [options, query, showSearch]);

  useLayoutEffect(() => {
    if (!open || !button.current || !menu.current) return;
    return autoUpdate(button.current, menu.current, () => void computePosition(button.current!, menu.current!, {
      placement: "bottom-start",
      middleware: [offset(5), flip({ padding: 10 }), shift({ padding: 10 }), size({ padding: 10, apply: ({ rects, availableHeight }) => setPosition((current) => ({ ...current, width: rects.reference.width, maxHeight: Math.min(MENU_MAX_HEIGHT, Math.max(120, availableHeight)) })) })],
    }).then(({ x, y }) => setPosition((current) => ({ ...current, left: x, top: y }))));
  }, [open, filtered.length, showSearch]);
  useEffect(() => {
    if (open && active >= 0) listApi.current?.scrollToIndex(active);
  }, [active, open, filtered.length]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => { if (!button.current?.contains(event.target as Node) && !menu.current?.contains(event.target as Node)) setOpen(false); };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);
  useEffect(() => {
    if (open && showSearch) search.current?.focus();
  }, [open, showSearch]);

  const move = (step: number) => {
    if (!filtered.length) return;
    setOpen(true);
    setActive((index) => (index + step + filtered.length) % filtered.length);
  };
  const choose = (option: SelectOption) => { onChange(option.value); setOpen(false); setQuery(""); button.current?.focus(); };
  const listHeight = Math.min(filtered.length * MENU_ITEM_HEIGHT, Math.max(MENU_ITEM_HEIGHT, position.maxHeight - 8 - (showSearch ? MENU_SEARCH_HEIGHT : 0)));
  const onListKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    if (event.key === "ArrowDown") { event.preventDefault(); move(1); }
    if (event.key === "ArrowUp") { event.preventDefault(); move(-1); }
    if (event.key === "Enter" && open && filtered[active]) { event.preventDefault(); choose(filtered[active]); }
    if (event.key === "Escape") { event.preventDefault(); setOpen(false); button.current?.focus(); }
  };
  return <>
    <button ref={button} type="button" className={styles.trigger} aria-label={ariaLabel} aria-haspopup="listbox" aria-controls={open ? menuId : undefined} aria-expanded={open} disabled={disabled} onClick={() => { const nextOpen = !open; setOpen(nextOpen); if (nextOpen) { setQuery(""); setActive(Math.max(0, options.findIndex((option) => option.value === value))); } }} onKeyDown={(event) => {
      if (showSearch && open) return;
      onListKeyDown(event);
    }}><span className={styles.optionContent}>{selected?.icon && <Icon icon={selected.icon} />}<span>{selected?.label ?? value}</span></span><Icon icon={chevronDownIcon} size="1.1em" className={[styles.dropdownIcon, open && styles.dropdownIconOpen].filter(Boolean).join(" ")} /></button>
    {open && createPortal(<div id={menuId} ref={menu} className={styles.menu} role="listbox" style={{ left: position.left, top: position.top, width: position.width }} onPointerDown={(event) => event.stopPropagation()}>
      {showSearch && <input ref={search} className={styles.search} value={query} placeholder={t("搜索账号")} aria-label={t("搜索账号")} onChange={(event) => { setQuery(event.target.value); setActive(0); }} onKeyDown={onListKeyDown} />}
      {filtered.length ? <VirtualList items={filtered} itemKey="value" estimatedItemHeight={MENU_ITEM_HEIGHT} onReady={(api) => { listApi.current = api; api.scrollToIndex(active); }} style={{ height: listHeight }}>
        {(option, index) => <button type="button" role="option" aria-selected={option.value === value} data-active={index === active || undefined} onMouseEnter={() => setActive(index)} onClick={() => choose(option)}><span className={styles.optionContent}>{option.icon && <Icon icon={option.icon} />}<span>{option.label}</span></span></button>}
      </VirtualList> : <div className={styles.empty}>{t("无匹配项")}</div>}
    </div>, document.body)}
  </>;
}

export function Combobox({ value, options = [], placeholder, disabled, append, onChange }: { value: string; options?: string[]; placeholder?: string; disabled?: boolean; append?: ReactNode; onChange: (value: string) => void }) {
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const listApi = useRef<VirtualListApi | null>(null);
  const menuId = useId();
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const [position, setPosition] = useState({ left: 0, top: 0, width: 0, maxHeight: 280 });
  const filtered = useMemo(() => {
    const query = value.trim().toLocaleLowerCase();
    return options.filter((option) => !query || option.toLocaleLowerCase().includes(query));
  }, [options, value]);

  useLayoutEffect(() => {
    if (!open || !root.current || !menu.current) return;
    return autoUpdate(root.current, menu.current, () => void computePosition(root.current!, menu.current!, {
      placement: "bottom-start",
      middleware: [offset(6), flip({ padding: 12 }), shift({ padding: 12 }), size({ padding: 12, apply: ({ rects, availableHeight }) => setPosition((current) => ({ ...current, width: rects.reference.width, maxHeight: Math.max(160, availableHeight) })) })],
    }).then(({ x, y }) => setPosition((current) => ({ ...current, left: x, top: y }))));
  }, [open, filtered.length]);

  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node) && !menu.current?.contains(event.target as Node)) setOpen(false); };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);
  useEffect(() => {
    if (open && active >= 0) listApi.current?.scrollToIndex(active);
  }, [active, open]);

  const openMenu = () => {
    if (disabled || options.length === 0) return;
    setOpen(true);
    const selected = filtered.indexOf(value);
    setActive(selected >= 0 ? selected : 0);
  };
  const choose = (option: string) => { onChange(option); setOpen(false); input.current?.focus(); };
  const move = (step: number) => {
    if (!open) { openMenu(); return; }
    if (!filtered.length) return;
    setActive((index) => (index < 0 ? 0 : (index + step + filtered.length) % filtered.length));
  };
  return <div className={styles.comboRow}><div ref={root} className={styles.combo}>
    <input ref={input} value={value} placeholder={placeholder} disabled={disabled} role="combobox" aria-haspopup="listbox" aria-controls={open ? menuId : undefined} aria-expanded={open} aria-autocomplete="list" onFocus={() => { if (options.length) openMenu(); }} onBlur={() => window.setTimeout(() => setOpen(false), 100)} onChange={(event) => { onChange(event.target.value); setActive(0); if (options.length) setOpen(true); }} onKeyDown={(event) => {
      if (event.key === "ArrowDown") { event.preventDefault(); move(1); }
      if (event.key === "ArrowUp") { event.preventDefault(); move(-1); }
      if (event.key === "Enter" && open && filtered[active]) { event.preventDefault(); choose(filtered[active]); }
      if (event.key === "Escape") setOpen(false);
    }} />
    <button type="button" className={styles.comboToggle} disabled={disabled} aria-label={t("打开模型列表")} aria-expanded={open} tabIndex={-1} onMouseDown={(event) => event.preventDefault()} onClick={() => { if (open) setOpen(false); else { openMenu(); input.current?.focus(); } }}><Icon icon={chevronDownIcon} size="1.1em" className={[styles.dropdownIcon, open && styles.dropdownIconOpen].filter(Boolean).join(" ")} /></button>
    {open && createPortal(<div id={menuId} ref={menu} className={styles.menu} role="listbox" style={{ left: position.left, top: position.top, width: position.width }}>
      <VirtualList items={filtered} itemKey={(option) => option} estimatedItemHeight={30} onReady={(api) => { listApi.current = api; if (active >= 0) api.scrollToIndex(active); }} style={{ height: Math.min(filtered.length * 30, Math.max(30, position.maxHeight - 8)) }}>
        {(option, index) => <button type="button" role="option" aria-selected={option === value} data-active={index === active || undefined} onMouseEnter={() => setActive(index)} onMouseDown={(event) => event.preventDefault()} onClick={() => choose(option)}>{option}</button>}
      </VirtualList>
    </div>, document.body)}
  </div>{append}</div>;
}

export function MultiCombobox({ value, options = [], placeholder, disabled, append, onChange }: {
  value: string[];
  options?: string[];
  placeholder?: string;
  disabled?: boolean;
  append?: ReactNode;
  onChange: (value: string[]) => void;
}) {
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const listApi = useRef<VirtualListApi | null>(null);
  const menuId = useId();
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [position, setPosition] = useState({ left: 0, top: 0, width: 0, maxHeight: 280 });
  const selected = useMemo(() => new Set(value), [value]);
  const filtered = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase();
    return options.filter((option) => !normalized || option.toLocaleLowerCase().includes(normalized));
  }, [options, query]);

  useLayoutEffect(() => {
    if (!open || !root.current || !menu.current) return;
    return autoUpdate(root.current, menu.current, () => void computePosition(root.current!, menu.current!, {
      placement: "bottom-start",
      middleware: [offset(6), flip({ padding: 12 }), shift({ padding: 12 }), size({ padding: 12, apply: ({ rects, availableHeight }) => setPosition((current) => ({ ...current, width: rects.reference.width, maxHeight: Math.max(160, availableHeight) })) })],
    }).then(({ x, y }) => setPosition((current) => ({ ...current, left: x, top: y }))));
  }, [filtered.length, open]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node) && !menu.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);
  useEffect(() => {
    if (open && active >= 0) listApi.current?.scrollToIndex(active);
  }, [active, open]);

  const add = (modelId: string) => {
    const normalized = modelId.trim();
    if (normalized && !selected.has(normalized)) onChange([...value, normalized]);
    setQuery("");
  };
  const toggle = (modelId: string) => {
    onChange(selected.has(modelId) ? value.filter((item) => item !== modelId) : [...value, modelId]);
    setQuery("");
    input.current?.focus();
  };
  const move = (step: number) => {
    if (!filtered.length) return;
    setOpen(true);
    setActive((index) => (index + step + filtered.length) % filtered.length);
  };

  return <div className={styles.comboRow}><div ref={root} className={styles.multiCombo} data-open={open || undefined}>
    <div className={styles.multiValues}>
      {value.length > 0 && <span className={styles.multiCount}>{t("已选择 {count} 个", { count: value.length })}</span>}
      <input ref={input} value={query} placeholder={value.length ? t("继续选择或输入") : placeholder} disabled={disabled} role="combobox" aria-haspopup="listbox" aria-controls={open ? menuId : undefined} aria-expanded={open} aria-autocomplete="list" onFocus={() => { if (options.length) setOpen(true); }} onBlur={() => add(query)} onChange={(event) => { setQuery(event.target.value); setActive(0); if (options.length) setOpen(true); }} onKeyDown={(event) => {
        if (event.key === "ArrowDown") { event.preventDefault(); move(1); }
        if (event.key === "ArrowUp") { event.preventDefault(); move(-1); }
        if (event.key === "Enter") {
          event.preventDefault();
          const normalized = query.trim();
          const exact = filtered.find((option) => option.toLocaleLowerCase() === normalized.toLocaleLowerCase());
          if (normalized && !exact) add(normalized);
          else if (open && filtered[active]) toggle(filtered[active]);
        }
        if (event.key === "Backspace" && !query && value.length) onChange(value.slice(0, -1));
        if (event.key === "Escape") { setOpen(false); input.current?.focus(); }
      }} />
    </div>
    <button type="button" className={styles.comboToggle} disabled={disabled || !options.length} aria-label={t("打开模型列表")} aria-expanded={open} tabIndex={-1} onMouseDown={(event) => event.preventDefault()} onClick={() => { setOpen(!open); input.current?.focus(); }}><Icon icon={chevronDownIcon} size="1.1em" className={[styles.dropdownIcon, open && styles.dropdownIconOpen].filter(Boolean).join(" ")} /></button>
    {open && createPortal(<div id={menuId} ref={menu} className={styles.menu} role="listbox" aria-multiselectable="true" style={{ left: position.left, top: position.top, width: position.width }} onPointerDown={(event) => event.stopPropagation()} onKeyDown={(event) => { if (event.key === "Escape") { event.preventDefault(); setOpen(false); input.current?.focus(); } }}>
      {filtered.length ? <VirtualList items={filtered} itemKey={(option) => option} estimatedItemHeight={30} onReady={(api) => { listApi.current = api; api.scrollToIndex(active); }} style={{ height: Math.min(filtered.length * 30, Math.max(30, position.maxHeight - 8)) }}>
        {(option, index) => <button type="button" className={styles.multiOption} role="option" aria-selected={selected.has(option)} data-active={index === active || undefined} onMouseEnter={() => setActive(index)} onMouseDown={(event) => event.preventDefault()} onClick={() => toggle(option)}><span>{option}</span>{selected.has(option) && <Icon icon={checkIcon} size="1.1em" />}</button>}
      </VirtualList> : <div className={styles.empty}>{t("输入模型名称后按 Enter 添加")}</div>}
    </div>, document.body)}
  </div>{append}</div>;
}
