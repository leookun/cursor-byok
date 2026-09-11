import type { IconifyIcon } from "@iconify/react/offline";
import { useEffect, useRef, useState, type ReactNode } from "react";
import Sortable from "sortablejs";
import type { Model, PluginDescriptor, PluginModelDescriptor } from "../../shared/api";
import { Card } from "../../shared/ui/Card";
import { Icon } from "../../shared/ui/Icon";
import { chevronDownIcon, chevronRightIcon, claudeIcon, dragIcon, editIcon, flatColorOrganizationIcon, openAiIcon } from "../../shared/ui/icons";
import { Switch } from "../../shared/ui/Switch";
import { TooltipTrigger } from "../../shared/ui/TooltipTrigger";
import { TruncatedButton } from "../../shared/ui/TruncatedButton";
import { CursorModelTestResult, type CursorModelTestState } from "./CursorModelTestResult";
import styles from "./CursorSettings.module.scss";

export type CursorModelGrouping = "flat" | "provider" | "type";

export type CursorModelGroup = {
  key: string;
  label: string;
  icon: IconifyIcon;
  /** 分组内至少有一个模型发布到 Cursor 时为 true。 */
  enabled: boolean;
  models: Model[];
};

export type CursorPluginModelGroup = {
  key: string;
  label: string;
  icon: string;
  /** 分组内至少有一个模型发布到 Cursor 时为 true。 */
  enabled: boolean;
  models: PluginModelDescriptor[];
};

type CursorModelCardsProps = {
  models: Model[];
  pluginGroups: CursorPluginModelGroup[];
  grouping: CursorModelGrouping;
  disabled: boolean;
  /** 正在切换开关的分组键;只让该分组的开关进入忙碌态,其它分组不受影响。 */
  busyGroupKey: string | null;
  testingModelHashes: Set<string>;
  testResults: Map<string, CursorModelTestState>;
  onTest: (model: Model) => void;
  onEdit: (model: Model) => void;
  onDuplicate: (model: Model) => void;
  onDelete: (model: Model) => void;
  onTestPluginModel: (model: PluginModelDescriptor) => void;
  onPluginSettings: (model: PluginModelDescriptor) => void;
  onReorder: (modelHashes: string[]) => void;
  onGroupSettings: (group: CursorModelGroup) => void;
  onSetBuiltinGroupEnabled: (group: CursorModelGroup, enabled: boolean) => void;
  onSetPluginGroupEnabled: (group: CursorPluginModelGroup, enabled: boolean) => void;
};

type ModelGridProps = Omit<CursorModelCardsProps, "grouping" | "pluginGroups" | "busyGroupKey" | "onTestPluginModel" | "onPluginSettings" | "onGroupSettings" | "onSetBuiltinGroupEnabled" | "onSetPluginGroupEnabled"> & {
  sortable: boolean;
};

/** 分组开关的忙碌键:内置分组与插件分组可能同名,用来源前缀区分。 */
export function groupToggleKey(source: "builtin" | "plugin", key: string) {
  return `${source}:${key}`;
}

export function cursorModelGroups(models: Model[], grouping: Exclude<CursorModelGrouping, "flat">): CursorModelGroup[] {
  const groups = new Map<string, CursorModelGroup>();
  for (const model of models) {
    const descriptor = grouping === "provider" ? providerGroup(model) : typeGroup(model);
    const group = groups.get(descriptor.key);
    if (group) {
      group.models.push(model);
      group.enabled ||= model.enabled;
    } else {
      groups.set(descriptor.key, { ...descriptor, enabled: model.enabled, models: [model] });
    }
  }
  return [...groups.values()];
}

/** Cursor 页显示的插件分组:每个就绪插件一组,列出该插件的全部模型(含已隐藏的),
 * 这样关闭分组开关后分组本身仍然可见,可以再次打开。 */
export function cursorPluginModelGroups(plugins: PluginDescriptor[]): CursorPluginModelGroup[] {
  return plugins.flatMap((plugin) => {
    const models = plugin.providers
      .filter((provider) => provider.configured)
      .flatMap((provider) => provider.models);
    return models.length > 0
      ? [{ key: plugin.id, label: plugin.name, icon: plugin.icon, enabled: models.some((model) => model.enabled), models }]
      : [];
  });
}

export function CursorModelCards(props: CursorModelCardsProps) {
  const builtinBusy = (key: string) => props.disabled || props.busyGroupKey === groupToggleKey("builtin", key);
  const builtins = props.grouping === "flat"
    ? <div style={{ paddingTop: "10px" }}><ModelGrid {...props} sortable /></div>
    : <div className={styles.modelGroups}>
      {cursorModelGroups(props.models, props.grouping).map((group) => <CollapsibleGroup
        key={group.key}
        label={group.label}
        icon={group.icon}
        defaultOpen={false}
        enabled={group.enabled}
        busy={builtinBusy(group.key)}
        onToggleEnabled={props.grouping === "provider" ? (enabled) => props.onSetBuiltinGroupEnabled(group, enabled) : undefined}
        onSettings={props.grouping === "provider" ? () => props.onGroupSettings(group) : undefined}
      >
        {group.models.map((model) => <ModelListRow
          key={model.model_hash}
          model={model}
          disabled={props.disabled}
          testing={props.testingModelHashes.has(model.model_hash)}
          result={props.testResults.get(model.model_hash)}
          onTest={() => props.onTest(model)}
          onEdit={() => props.onEdit(model)}
          onDuplicate={() => props.onDuplicate(model)}
          onDelete={() => props.onDelete(model)}
        />)}
      </CollapsibleGroup>)}
    </div>;
  return <div className={styles.modelGroups}>
    {builtins}
    {props.pluginGroups.map((group) => <CollapsibleGroup
      key={`${props.grouping}:${group.key}`}
      label={group.label}
      iconSrc={group.icon}
      defaultOpen={props.grouping === "flat"}
      enabled={group.enabled}
      busy={props.disabled || props.busyGroupKey === groupToggleKey("plugin", group.key)}
      onToggleEnabled={(enabled) => props.onSetPluginGroupEnabled(group, enabled)}
    >
      {group.models.map((model) => <PluginModelRow
        key={model.id}
        model={model}
        disabled={props.disabled}
        testing={props.testingModelHashes.has(model.id)}
        result={props.testResults.get(model.id)}
        onTest={() => props.onTestPluginModel(model)}
        onSettings={() => props.onPluginSettings(model)}
      />)}
    </CollapsibleGroup>)}
  </div>;
}

function CollapsibleGroup({ label, icon, iconSrc, defaultOpen = true, enabled, busy, onToggleEnabled, onSettings, children }: {
  label: string;
  icon?: IconifyIcon;
  iconSrc?: string;
  defaultOpen?: boolean;
  enabled?: boolean;
  busy?: boolean;
  onToggleEnabled?: (enabled: boolean) => void;
  onSettings?: () => void;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return <Card className={styles.groupCard}>
    <div className={styles.groupHeader}>
      <button
        type="button"
        className={styles.groupToggle}
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
      >
        {icon && <Icon icon={icon} size="1.1em" />}
        {iconSrc && <Icon src={iconSrc} size="1.1em" />}
        <span className={styles.groupLabel}>{label}</span>
      </button>
      {onToggleEnabled && <TooltipTrigger label={t("关闭后该分组的模型不再出现在 Cursor 模型选择栏中")}>
        <Switch
          size="small"
          checked={enabled ?? true}
          disabled={busy}
          label={t("在 Cursor 模型选择栏中显示该分组")}
          onChange={onToggleEnabled}
        />
      </TooltipTrigger>}
      {onSettings && <button type="button" className={styles.groupSettings} onClick={onSettings}>
        <Icon icon={editIcon} size="1em" />
        {t("分组设置")}
      </button>}
      <button
        type="button"
        className={styles.groupChevron}
        tabIndex={-1}
        aria-hidden="true"
        onClick={() => setOpen((current) => !current)}
      >
        <Icon icon={open ? chevronDownIcon : chevronRightIcon} size="1em" />
      </button>
    </div>
    {open && <div className={styles.modelList}>{children}</div>}
  </Card>;
}

function ModelListRow({ model, disabled, testing, result, onTest, onEdit, onDuplicate, onDelete }: {
  model: Model;
  disabled: boolean;
  testing: boolean;
  result: CursorModelTestState | undefined;
  onTest: () => void;
  onEdit: () => void;
  onDuplicate: () => void;
  onDelete: () => void;
}) {
  return <div className={styles.modelRow}>
    <div className={styles.modelRowName}>
      <span className={styles.modelRowNameText}>{model.display_name}</span>
      <span className={styles.modelRowModelId}>{model.model_id}</span>
    </div>
    <CursorModelTestResult compact state={result} testing={testing} />
    <div className={styles.modelCardActions}>
      <TruncatedButton size="small" disabled={disabled && !testing} label={testing ? t("取消测试") : t("测试")} onClick={onTest} />
      <TruncatedButton size="small" disabled={disabled} label={t("编辑")} onClick={onEdit} />
      <TruncatedButton size="small" disabled={disabled} label={t("复制")} onClick={onDuplicate} />
      <TruncatedButton size="small" className={styles.deleteButton} disabled={disabled} label={t("删除")} onClick={onDelete} />
    </div>
  </div>;
}

function PluginModelRow({ model, disabled, testing, result, onTest, onSettings }: {
  model: PluginModelDescriptor;
  disabled: boolean;
  testing: boolean;
  result: CursorModelTestState | undefined;
  onTest: () => void;
  onSettings: () => void;
}) {
  return <div className={styles.modelRow}>
    <div className={styles.modelRowName}>
      <span className={styles.modelRowNameText}>{model.displayName}</span>
      <span className={styles.modelRowModelId}>{model.modelId}</span>
    </div>
    <CursorModelTestResult compact state={result} testing={testing} />
    <div className={styles.modelCardActions}>
      <TruncatedButton size="small" disabled={disabled && !testing} label={testing ? t("取消测试") : t("测试")} onClick={onTest} />
      <TruncatedButton size="small" disabled={disabled} label={t("设置")} onClick={onSettings} />
    </div>
  </div>;
}

function ModelGrid({
  models,
  sortable: sortableEnabled,
  disabled,
  testingModelHashes,
  testResults,
  onTest,
  onEdit,
  onDuplicate,
  onDelete,
  onReorder,
}: ModelGridProps) {
  const grid = useRef<HTMLDivElement>(null);
  const sortable = useRef<Sortable | null>(null);
  const currentModels = useRef(models);
  const reorder = useRef(onReorder);
  currentModels.current = models;
  reorder.current = onReorder;

  useEffect(() => {
    if (!sortableEnabled || !grid.current) return;
    sortable.current = Sortable.create(grid.current, {
      animation: 160,
      dataIdAttr: "data-model-hash",
      draggable: `.${styles.modelCard}`,
      handle: `.${styles.sortHandle}`,
      ghostClass: styles.sortGhost,
      chosenClass: styles.sortChosen,
      dragClass: styles.sortDragging,
      forceFallback: true,
      fallbackOnBody: true,
      fallbackTolerance: 3,
      onEnd: (event) => {
        const oldIndex = event.oldDraggableIndex ?? event.oldIndex;
        const newIndex = event.newDraggableIndex ?? event.newIndex;
        if (typeof oldIndex !== "number"
          || typeof newIndex !== "number"
          || oldIndex === newIndex) {
          sortable.current?.sort(currentModels.current.map((model) => model.model_hash), false);
          return;
        }
        const reordered = currentModels.current.slice();
        const [moved] = reordered.splice(oldIndex, 1);
        if (!moved || newIndex < 0 || newIndex > reordered.length) {
          sortable.current?.sort(currentModels.current.map((model) => model.model_hash), false);
          return;
        }
        reordered.splice(newIndex, 0, moved);
        reorder.current(reordered.map((model) => model.model_hash));
      },
    });
    return () => {
      sortable.current?.destroy();
      sortable.current = null;
    };
  }, [sortableEnabled]);

  useEffect(() => {
    sortable.current?.option("disabled", disabled);
    sortable.current?.sort(models.map((model) => model.model_hash), false);
  }, [disabled, models]);

  return <div ref={grid} className={styles.modelGrid}>
    {models.map((model) => {
      const result = testResults.get(model.model_hash);
      const testing = testingModelHashes.has(model.model_hash);
      return <Card className={styles.modelCard} data-model-hash={model.model_hash} key={model.model_hash}>
        {sortableEnabled && <button type="button" className={styles.sortHandle} disabled={disabled} aria-label={t("拖动排序")} title={t("拖动排序")} onClick={(event) => event.stopPropagation()}>
          <Icon icon={dragIcon} size="1.25em" />
        </button>}
        <div className={styles.modelCardContent}>
          <div className={styles.modelCardTop}>
            <div className={styles.modelCardName}>
              <span className={styles.modelCardNameText}>{model.display_name}</span>
              <span className={styles.modelCardModelId}>{model.model_id}</span>
            </div>
            <span className={styles.modelTypeBadge}>
              <Icon icon={model.type === "anthropic" ? claudeIcon : openAiIcon} />
              {model.type === "anthropic" ? "Anthropic" : "OpenAI"}
            </span>
          </div>
          <div className={styles.modelCardTest}>
            <CursorModelTestResult state={result} testing={testing} />
          </div>
          <div className={styles.modelCardActions}>
            <TruncatedButton size="small" disabled={disabled && !testing} label={testing ? t("取消测试") : t("测试")} onClick={() => onTest(model)} />
            <TruncatedButton size="small" disabled={disabled} label={t("编辑")} onClick={() => onEdit(model)} />
            <TruncatedButton size="small" disabled={disabled} label={t("复制")} onClick={() => onDuplicate(model)} />
            <TruncatedButton size="small" className={styles.deleteButton} disabled={disabled} label={t("删除")} onClick={() => onDelete(model)} />
          </div>
        </div>
      </Card>;
    })}
  </div>;
}

function providerGroup(model: Model) {
  const key = providerDomain(model.base_url);
  const label = model.group_name?.trim() || key;
  return { key, label, icon: flatColorOrganizationIcon };
}

function providerDomain(baseUrl: string) {
  const value = baseUrl.trim();
  try {
    return new URL(value).hostname.toLowerCase() || value;
  } catch {
    try {
      return new URL(`https://${value}`).hostname.toLowerCase() || value;
    } catch {
      return value;
    }
  }
}

function typeGroup(model: Model) {
  if (model.type === "anthropic") return { key: "anthropic", label: "Anthropic", icon: claudeIcon };
  if (model.openai_endpoint === "/v1/chat/completions") return { key: "openai-chat", label: "OpenAI Chat", icon: openAiIcon };
  return { key: "openai-responses", label: "OpenAI Responses", icon: openAiIcon };
}
