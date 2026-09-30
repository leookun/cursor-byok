import { autoUpdate, computePosition, flip, offset, shift, size } from "@floating-ui/dom";
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon, type IconProps } from "./Icon";
import { checkIcon, chevronDownIcon, windowCloseIcon } from "./icons";
import type { VirtualListApi } from "../virtual/virtualTypes";
import { VirtualList } from "../virtual/VirtualList";
import styles from "./ModelSelect.module.scss";

export type ModelSelectOption = {
  value: string;
  label: string;
  group: string;
  icon?: IconProps["icon"];
  iconSrc?: string;
  metadata?: string;
  searchText?: string;
  disabled?: boolean;
};

type ModelSelectProps = {
  label: string;
  options: ModelSelectOption[];
  searchable?: boolean;
  disabled?: boolean;
} & (
  | { mode: "single"; value: string; onChange: (value: string) => void }
  | { mode: "multiple"; value: string[]; onChange: (value: string[]) => void }
);

type MenuItem =
  | { kind: "group"; key: string; label: string; options: ModelSelectOption[] }
  | { kind: "option"; key: string; option: ModelSelectOption };

function groupedItems(options: ModelSelectOption[]): MenuItem[] {
  const groups = new Map<string, ModelSelectOption[]>();
  for (const option of options) {
    const group = groups.get(option.group);
    if (group) group.push(option);
    else groups.set(option.group, [option]);
  }
  return [...groups].flatMap(([group, groupOptions]) => [
    { kind: "group" as const, key: `group:${group}`, label: group, options: groupOptions },
    ...groupOptions.map((option) => ({ kind: "option" as const, key: `option:${option.value}`, option })),
  ]);
}

function GroupCheckbox({ label, options, selected, onChange }: {
  label: string;
  options: ModelSelectOption[];
  selected: Set<string>;
  onChange: (checked: boolean) => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  const selectedCount = options.filter((option) => selected.has(option.value)).length;
  const checked = options.length > 0 && selectedCount === options.length;
  const indeterminate = selectedCount > 0 && !checked;

  useEffect(() => {
    if (input.current) input.current.indeterminate = indeterminate;
  }, [indeterminate]);

  return <label className={styles.group}>
    <input
      ref={input}
      type="checkbox"
      disabled={options.length === 0}
      checked={checked}
      aria-checked={indeterminate ? "mixed" : checked}
      onChange={(event) => onChange(event.target.checked)}
    />
    <span className={styles.checkbox} data-indeterminate={indeterminate || undefined} aria-hidden="true">
      {checked && <Icon icon={checkIcon} size="0.875em" />}
    </span>
    <span>{label}</span>
  </label>;
}

export function ModelSelect(props: ModelSelectProps) {
  const { label, options, disabled, mode } = props;
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const firstOption = useRef<HTMLLabelElement>(null);
  const listApi = useRef<VirtualListApi | null>(null);
  const pendingFocus = useRef<number | null>(null);
  const [focusedIndex, setFocusedIndex] = useState(-1);
  const menuId = useId();
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ left: 0, top: 0, width: 220, maxHeight: 280 });
  const [query, setQuery] = useState("");
  const searchInput = useRef<HTMLInputElement>(null);
  const visibleOptions = useMemo(() => options.filter((option) => !props.searchable || `${option.label} ${option.group} ${option.searchText ?? ""}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase())), [options, props.searchable, query]);
  const items = useMemo(() => groupedItems(visibleOptions), [visibleOptions]);
  const rowHeight = options.some((option) => option.metadata) ? 56 : 32;
  const selected = useMemo(
    () => new Set(mode === "multiple" ? props.value : [props.value]),
    [mode, props.value],
  );

  useLayoutEffect(() => {
    if (!open || !root.current || !menu.current) return;
    return autoUpdate(root.current, menu.current, () => void computePosition(root.current!, menu.current!, {
      placement: "bottom-start",
      middleware: [offset(5), flip({ padding: 10 }), shift({ padding: 10 }), size({
        padding: 10,
        apply: ({ rects, availableHeight }) => setPosition((current) => ({
          ...current,
          width: rects.reference.width,
          maxHeight: Math.max(120, availableHeight),
        })),
      })],
    }).then(({ x, y }) => setPosition((current) => ({ ...current, left: x, top: y }))));
  }, [open]);

  const close = () => {
    setOpen(false);
    trigger.current?.focus();
  };

  useEffect(() => {
    if (!open) return;
    if (props.searchable) searchInput.current?.focus();
    else firstOption.current?.focus();
    const closeOutside = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!root.current?.contains(target) && !menu.current?.contains(target)) close();
    };
    document.addEventListener("pointerdown", closeOutside);
    return () => document.removeEventListener("pointerdown", closeOutside);
  }, [open]);

  const summary = mode === "multiple"
    ? props.value.length === 0 ? t("全部") : t("已选 {count} 项", { count: props.value.length })
    : options.find((option) => option.value === props.value)?.label ?? props.value;

  const choose = (option: ModelSelectOption) => {
    if (option.disabled) return;
    if (mode === "single") {
      props.onChange(option.value);
      close();
      return;
    }
    props.onChange(selected.has(option.value)
      ? props.value.filter((item) => item !== option.value)
      : [...props.value, option.value]);
  };

  const toggleGroup = (groupOptions: ModelSelectOption[], checked: boolean) => {
    if (mode !== "multiple") return;
    const groupValues = new Set(groupOptions.filter((option) => !option.disabled).map((option) => option.value));
    props.onChange(checked
      ? [...props.value, ...groupOptions.filter((option) => !option.disabled && !selected.has(option.value)).map((option) => option.value)]
      : props.value.filter((value) => !groupValues.has(value)));
  };

  const openMenu = () => {
    if (disabled) return;
    const width = root.current?.getBoundingClientRect().width;
    if (width) setPosition((current) => ({ ...current, width }));
    setQuery("");
    setFocusedIndex(-1);
    setOpen(true);
  };

  return <>
    <div ref={root} className={styles.target} data-open={open || undefined} data-disabled={disabled || undefined}>
      <button
        ref={trigger}
        type="button"
        className={styles.trigger}
        disabled={disabled}
        aria-label={t("筛选项：{label}，{summary}", { label, summary })}
        aria-haspopup="listbox"
        aria-controls={open ? menuId : undefined}
        aria-expanded={open}
        onClick={() => { if (open) close(); else openMenu(); }}
        onKeyDown={(event) => { if (event.key === "ArrowDown") { event.preventDefault(); openMenu(); } }}
      >
        <span>{label}</span><span className={styles.summary} aria-hidden="true">{summary}</span><Icon icon={chevronDownIcon} size="1.1em" />
      </button>
      {mode === "multiple" && props.value.length > 0 && <button
        type="button"
        className={styles.clear}
        aria-label={t("清除{label}筛选", { label })}
        onClick={() => props.onChange([])}
      ><Icon icon={windowCloseIcon} size="1.1em" /></button>}
    </div>
    {open && createPortal(<div
      id={menuId}
      ref={menu}
      className={styles.menu}
      aria-label={label}
      style={position}
      onPointerDown={(event) => event.stopPropagation()}
      onKeyDown={(event) => {
        if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); close(); }
        if (event.key === "Tab") {
          const controls = [...menu.current!.querySelectorAll<HTMLElement>("input:not([disabled]):not([tabindex='-1']), button:not([disabled]), [role='option']:not([aria-disabled='true'])")];
          if ((event.shiftKey && document.activeElement === controls[0]) || (!event.shiftKey && document.activeElement === controls.at(-1))) close();
          event.stopPropagation();
        }
        if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key) && (event.target !== searchInput.current || event.key.startsWith("Arrow"))) {
          event.preventDefault(); event.stopPropagation();
          const indices = items.flatMap((item, index) => item.kind === "option" && !item.option.disabled ? [index] : []);
          const next = event.key === "Home" ? indices[0] : event.key === "End" ? indices.at(-1) : event.key === "ArrowDown" ? indices.find((index) => index > focusedIndex) ?? indices[0] : [...indices].reverse().find((index) => index < focusedIndex) ?? indices.at(-1);
          if (next !== undefined) {
            pendingFocus.current = next;
            setFocusedIndex(next);
            listApi.current?.scrollToIndex(next, "auto");
            const node = menu.current?.querySelector<HTMLElement>(`[data-option-index='${next}']`);
            if (node) { node.focus(); pendingFocus.current = null; }
          }
        }
      }}
    >
      {props.searchable && <input ref={searchInput} className={styles.search} aria-label={t("搜索来源或模型")} placeholder={t("搜索来源或模型")} value={query} onChange={(event) => setQuery(event.target.value)} />}
      <div
        className={styles.listFrame}
        role="listbox"
        aria-label={label}
        aria-multiselectable={mode === "multiple" ? "true" : undefined}
        style={{ height: Math.min(items.length * rowHeight, Math.max(32, position.maxHeight - (mode === "multiple" ? 45 : 0) - (props.searchable ? 40 : 0))) }}
      ><VirtualList
          onReady={(value) => { listApi.current = value; }}
          items={items}
          itemKey="key"
          className={styles.list}
          style={{ height: "100%" }}
          estimatedItemHeight={rowHeight}
          overscan={4}
          empty={<div className={styles.empty}>{t("暂无选项")}</div>}
        >{(item, index) => item.kind === "group"
          ? mode === "multiple"
            ? <GroupCheckbox
                label={item.label}
                options={item.options.filter((option) => !option.disabled)}
                selected={selected}
                onChange={(checked) => toggleGroup(item.options, checked)}
              />
            : <div className={styles.groupLabel}>{item.label}</div>
          : <label
              ref={(node) => {
                if (index === 1) firstOption.current = node;
                if (node && pendingFocus.current === index) { node.focus(); pendingFocus.current = null; }
              }}
              data-option-index={index}
              onFocus={() => setFocusedIndex(index)}
              className={styles.optionRow}
              role="option"
              aria-selected={selected.has(item.option.value)}
              aria-disabled={item.option.disabled || undefined}
              tabIndex={item.option.disabled ? -1 : 0}
              onKeyDown={(event) => {
                if (event.key === " " || event.key === "Enter") {
                  event.preventDefault();
                  choose(item.option);
                }
              }}
            >
              <input
                type="checkbox"
                disabled={item.option.disabled}
                checked={selected.has(item.option.value)}
                tabIndex={-1}
                onChange={() => choose(item.option)}
              />
              <span className={styles.checkbox} aria-hidden="true">
                {selected.has(item.option.value) && <Icon icon={checkIcon} size="0.875em" />}
              </span>
              <span className={styles.option}>
                {(item.option.icon || item.option.iconSrc) && <Icon icon={item.option.icon} src={item.option.iconSrc} />}
                <span>{item.option.label}{item.option.metadata && <small className={styles.metadata}>{item.option.metadata}</small>}</span>
              </span>
            </label>}</VirtualList></div>
      {mode === "multiple" && options.length > 0 && <div className={styles.footer}>
        <button type="button" onClick={() => props.onChange(options.filter((option) => !option.disabled).map((option) => option.value))}>{t("全选")}</button>
        <button type="button" onClick={() => props.onChange([])}>{t("全不选")}</button>
      </div>}
    </div>, document.body)}
  </>;
}
