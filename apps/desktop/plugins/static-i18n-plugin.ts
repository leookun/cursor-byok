import { createHash } from "node:crypto"
import fs from "node:fs"
import path from "node:path"
import { parse as parseJavaScript, type ParserPlugin } from "@babel/parser"
import traverseModule from "@babel/traverse"
import { normalizePath, type Plugin } from "vite"

// @ts-expect-error - CJS/ESM 模块互操作。
const traverse = traverseModule.default ?? traverseModule

const SOURCE_LOCALE = "zh-CN"
const SUPPORTED_LOCALES = ["zh-CN", "en-US", "pt-BR"]
const CHINESE_SOURCE_PATTERN = /[\u3400-\u9fff]/u
const PLACEHOLDER_PATTERN = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g
const AUTO_IMPORT_NAME = "__staticI18nT"
const AUTO_IMPORT = `import { t as ${AUTO_IMPORT_NAME} } from "/src/i18n/runtime";\n`

const BABEL_PLUGINS = [
  "jsx",
  "typescript",
  ["classProperties", { decoratorsBeforeExport: false }],
  ["classPrivateProperties", { decoratorsBeforeExport: false }],
  "classPrivateMethods",
  "topLevelAwait",
  "importAttributes"
] as unknown as ParserPlugin[]

interface SourceRef {
  file: string
  line: number
  column: number
}

interface MessageRecord {
  id: string
  source: string
  kind: MessageKind
  placeholders: string[]
  ref: SourceRef
}

interface MergedEntry {
  source: string
  kind: MessageKind
  placeholders: string[]
  refs: SourceRef[]
}

interface CallReplacement {
  start: number
  end: number
}

interface BabelArgument {
  type: string
  value?: string
  properties?: BabelObjectProperty[]
}

interface BabelObjectProperty {
  type: string
  computed?: boolean
  key?: { type: string; name?: string; value?: string }
}

interface BabelCallNode {
  callee: { type: string; name?: string; start?: number | null; end?: number | null }
  arguments: BabelArgument[]
  loc?: { start: { line: number; column: number } | null } | null
}

interface BabelCallPath {
  node: BabelCallNode
  scope: { getBinding(name: string): unknown }
}

enum MessageKind {
  Text = "text",
  Template = "template",
}

enum BabelNodeType {
  Identifier = "Identifier",
  StringLiteral = "StringLiteral",
  ObjectExpression = "ObjectExpression",
  ObjectProperty = "ObjectProperty",
}

function hashMessageID(message: string): string {
  return createHash("sha256").update(message).digest("hex").slice(0, 16)
}

function stripQuery(id: string): string {
  return id.split("?")[0]
}

function isSourceFile(id: string): boolean {
  return /\.(?:js|jsx|ts|tsx)$/.test(stripQuery(id))
}

function isExcludedFile(sourceRoot: string, id: string): boolean {
  const cleanID = normalizePath(stripQuery(id))
  if (cleanID.includes("/node_modules/")) return true
  const relativePath = normalizePath(path.relative(sourceRoot, cleanID))
  if (relativePath.startsWith("..")) return true
  return relativePath.startsWith("i18n/")
}

function readJSONFile(filePath: string): Record<string, string> {
  if (!fs.existsSync(filePath)) return {}
  const raw = fs.readFileSync(filePath, "utf8").trim()
  return raw ? JSON.parse(raw) : {}
}

function writeJSONFile(filePath: string, payload: unknown): void {
  fs.mkdirSync(path.dirname(filePath), { recursive: true })
  fs.writeFileSync(filePath, `${JSON.stringify(payload, null, 2)}\n`)
}

function buildRef(
  filePath: string,
  sourceRoot: string,
  loc: { line: number; column: number } | null
): SourceRef {
  return {
    file: normalizePath(path.relative(sourceRoot, filePath)),
    line: loc?.line ?? 1,
    column: loc?.column ?? 1
  }
}

function sourceLocation(
  filePath: string,
  sourceRoot: string,
  loc: { line: number; column: number } | null
): string {
  const ref = buildRef(filePath, sourceRoot, loc)
  return `${ref.file}:${ref.line}:${ref.column}`
}

function placeholderNames(message: string): string[] {
  return Array.from(message.matchAll(PLACEHOLDER_PATTERN), (match) => match[1])
}

function assertUniquePlaceholders(
  placeholders: string[],
  location: string,
  message: string
): void {
  const duplicate = placeholders.find(
    (name, index) => placeholders.indexOf(name) !== index
  )
  if (!duplicate) return
  throw new Error(
    `[static-i18n] ${location} 重复使用占位符 {${duplicate}}：${JSON.stringify(message)}`
  )
}

function objectParameterNames(argument: BabelArgument, location: string): string[] {
  if (argument.type !== BabelNodeType.ObjectExpression) {
    throw new Error(`[static-i18n] ${location} 的 t params 必须是静态对象字面量`)
  }

  const names: string[] = []
  for (const property of argument.properties ?? []) {
    if (property.type !== BabelNodeType.ObjectProperty || property.computed) {
      throw new Error(
        `[static-i18n] ${location} 的 t params 不允许展开、计算属性或方法`
      )
    }
    const key = property.key
    const name =
      key?.type === BabelNodeType.Identifier
        ? key.name
        : key?.type === BabelNodeType.StringLiteral
          ? key.value
          : undefined
    if (!name) {
      throw new Error(`[static-i18n] ${location} 的 t params key 必须是静态名称`)
    }
    if (names.includes(name)) {
      throw new Error(`[static-i18n] ${location} 的 t params 重复提供 ${name}`)
    }
    names.push(name)
  }
  return names
}

function validateSourceCall(
  filePath: string,
  sourceRoot: string,
  source: string,
  args: BabelArgument[],
  loc: { line: number; column: number } | null
): string[] {
  const location = sourceLocation(filePath, sourceRoot, loc)
  if (!CHINESE_SOURCE_PATTERN.test(source)) {
    throw new Error(
      `[static-i18n] ${location} 的 t source 必须包含中文：${JSON.stringify(source)}`
    )
  }

  const placeholders = placeholderNames(source)
  assertUniquePlaceholders(placeholders, location, source)

  if (!placeholders.length) {
    if (args.length !== 1) {
      throw new Error(`[static-i18n] ${location} 的无参数消息不能传入 params`)
    }
    return placeholders
  }

  if (args.length !== 2) {
    throw new Error(
      `[static-i18n] ${location} 必须为占位符传入一个具名 params 对象`
    )
  }

  const parameters = objectParameterNames(args[1], location)
  const missing = placeholders.filter((name) => !parameters.includes(name))
  const extra = parameters.filter((name) => !placeholders.includes(name))
  if (missing.length || extra.length) {
    throw new Error(
      `[static-i18n] ${location} 的 params 与占位符不一致` +
      `${missing.length ? `，缺少：${missing.join(", ")}` : ""}` +
      `${extra.length ? `，多余：${extra.join(", ")}` : ""}`
    )
  }
  return placeholders
}

function buildMessageRecord(
  filePath: string,
  sourceRoot: string,
  source: string,
  placeholders: string[],
  loc: { line: number; column: number } | null
): MessageRecord {
  return {
    id: hashMessageID(source),
    source,
    kind: placeholders.length ? MessageKind.Template : MessageKind.Text,
    placeholders,
    ref: buildRef(filePath, sourceRoot, loc)
  }
}

function mergeMessageRecords(records: MessageRecord[]) {
  const entries = new Map<string, MergedEntry>()
  for (const record of records) {
    const current = entries.get(record.id)
    if (!current) {
      entries.set(record.id, {
        source: record.source,
        kind: record.kind,
        placeholders: record.placeholders,
        refs: [record.ref]
      })
      continue
    }
    if (current.source !== record.source) {
      throw new Error(
        `[static-i18n] Message id collision for ${record.id}: ${current.source} <> ${record.source}`
      )
    }
    current.refs.push(record.ref)
  }

  const sortedEntries: Record<string, MergedEntry> = {}
  for (const id of Array.from(entries.keys()).sort()) {
    const entry = entries.get(id)!
    sortedEntries[id] = {
      ...entry,
      refs: entry.refs.sort(
        (a, b) =>
          a.file.localeCompare(b.file) || a.line - b.line || a.column - b.column
      )
    }
  }
  return { entries: sortedEntries }
}

function mergeLocaleMessages(
  existingMessages: Record<string, string>,
  catalogEntries: Record<string, MergedEntry>,
  locale: string
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(catalogEntries).map(([id, entry]) => [
      id,
      locale === SOURCE_LOCALE ? entry.source : existingMessages[id] ?? ""
    ])
  )
}

function validateTranslations(
  sourceRoot: string,
  entries: Record<string, MergedEntry>,
  requireComplete: boolean
): void {
  for (const locale of SUPPORTED_LOCALES) {
    if (locale === SOURCE_LOCALE) continue
    const localePath = path.join(sourceRoot, "i18n/locales", `${locale}.json`)
    const messages = readJSONFile(localePath)
    for (const [id, entry] of Object.entries(entries)) {
      const translation = messages[id]
      if (!translation) {
        if (requireComplete) {
          throw new Error(
            `[static-i18n] ${localePath}:${id} 缺少译文：${entry.source}`
          )
        }
        continue
      }
      const actual = placeholderNames(translation)
      assertUniquePlaceholders(actual, `${localePath}:${id}`, translation)
      const missing = entry.placeholders.filter((name) => !actual.includes(name))
      const extra = actual.filter((name) => !entry.placeholders.includes(name))
      if (!missing.length && !extra.length) continue
      throw new Error(
        `[static-i18n] ${localePath}:${id} 的译文占位符与 source 不一致` +
        `${missing.length ? `，缺少：${missing.join(", ")}` : ""}` +
        `${extra.length ? `，多余：${extra.join(", ")}` : ""}`
      )
    }
  }
}

function walkSourceFiles(dirPath: string, visitor: (filePath: string) => void): void {
  for (const entry of fs.readdirSync(dirPath, { withFileTypes: true })) {
    const nextPath = path.join(dirPath, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== "node_modules") walkSourceFiles(nextPath, visitor)
    } else {
      visitor(nextPath)
    }
  }
}

function parseProgram(code: string, filename: string) {
  try {
    return parseJavaScript(code, {
      sourceType: "module",
      sourceFilename: filename,
      plugins: [...BABEL_PLUGINS]
    })
  } catch (error) {
    throw new Error(
      `[static-i18n] Failed to parse ${filename}: ${(error as Error).message}`
    )
  }
}

function analyzeTCalls(
  code: string,
  filePath: string,
  sourceRoot: string
): { records: MessageRecord[]; replacements: CallReplacement[] } {
  const records: MessageRecord[] = []
  const replacements: CallReplacement[] = []
  const ast = parseProgram(code, filePath)

  traverse(ast, {
    CallExpression(callPath: BabelCallPath) {
      const node = callPath.node
      if (node.callee.type !== BabelNodeType.Identifier || node.callee.name !== "t") return

      let loc: { line: number; column: number } | null = null
      if (node.loc?.start) {
        loc = { line: node.loc.start.line, column: node.loc.start.column + 1 }
      }
      const location = sourceLocation(filePath, sourceRoot, loc)
      if (callPath.scope.getBinding("t")) {
        throw new Error(
          `[static-i18n] ${location} 的 t 是保留全局函数，请删除本地声明或 import`
        )
      }
      if (!node.arguments.length) {
        throw new Error(`[static-i18n] ${location} 的 t 调用缺少中文 source`)
      }

      const firstArg = node.arguments[0]
      if (firstArg.type !== BabelNodeType.StringLiteral || firstArg.value === undefined) {
        throw new Error(`[static-i18n] ${location} 的 t source 必须是静态字符串字面量`)
      }
      const placeholders = validateSourceCall(
        filePath,
        sourceRoot,
        firstArg.value,
        node.arguments,
        loc
      )
      records.push(
        buildMessageRecord(filePath, sourceRoot, firstArg.value, placeholders, loc)
      )

      const { start, end } = node.callee
      if (start == null || end == null) {
        throw new Error(`[static-i18n] ${location} 无法定位 t 调用`)
      }
      replacements.push({ start, end })
    }
  })
  return { records, replacements }
}

function collectCatalog(sourceRoot: string) {
  const records: MessageRecord[] = []
  walkSourceFiles(sourceRoot, (filePath) => {
    const cleanPath = normalizePath(filePath)
    if (!isSourceFile(cleanPath) || isExcludedFile(sourceRoot, cleanPath)) return
    records.push(
      ...analyzeTCalls(fs.readFileSync(cleanPath, "utf8"), cleanPath, sourceRoot).records
    )
  })
  return mergeMessageRecords(records)
}

function syncCatalogFiles(
  sourceRoot: string,
  catalog: ReturnType<typeof collectCatalog>
): void {
  writeJSONFile(path.join(sourceRoot, "i18n/generated/catalog.json"), catalog)
  for (const locale of SUPPORTED_LOCALES) {
    const localePath = path.join(sourceRoot, "i18n/locales", `${locale}.json`)
    writeJSONFile(
      localePath,
      mergeLocaleMessages(readJSONFile(localePath), catalog.entries, locale)
    )
  }
}

function injectRuntimeImport(code: string, replacements: CallReplacement[]): string {
  let transformed = code
  for (const replacement of replacements.sort((a, b) => b.start - a.start)) {
    transformed =
      transformed.slice(0, replacement.start) +
      AUTO_IMPORT_NAME +
      transformed.slice(replacement.end)
  }
  return AUTO_IMPORT + transformed
}

export function staticI18nPlugin(): Plugin {
  let sourceRoot = path.join(process.cwd(), "src")
  const shouldScan =
    process.argv.includes("--scan") || process.env.STATIC_I18N_SCAN === "true"

  return {
    name: "cursor-byok-static-i18n",
    enforce: "pre",
    configResolved(config) {
      sourceRoot = path.join(config.root, "src")
    },
    buildStart() {
      const catalog = collectCatalog(sourceRoot)
      if (shouldScan) syncCatalogFiles(sourceRoot, catalog)
      validateTranslations(sourceRoot, catalog.entries, !shouldScan)
    },
    transform(code, id) {
      if (!isSourceFile(id) || isExcludedFile(sourceRoot, id)) return null
      const analysis = analyzeTCalls(code, normalizePath(stripQuery(id)), sourceRoot)
      if (!analysis.replacements.length) return null
      return { code: injectRuntimeImport(code, analysis.replacements), map: null }
    }
  }
}
