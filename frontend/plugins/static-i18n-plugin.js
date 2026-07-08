import { createHash } from "crypto";
import fs from "fs";
import path from "path";
import MagicString from "magic-string";
import { parse as parseJavaScript } from "@babel/parser";
import traverseModule from "@babel/traverse";
import { parse as parseTemplate } from "@vue/compiler-dom";
import { parse as parseSFC } from "@vue/compiler-sfc";
import { normalizePath } from "vite";

const traverse = traverseModule.default ?? traverseModule;

const SOURCE_LANGUAGE = "zh-CN";
const SUPPORTED_LOCALES = ["zh-CN", "en-US", "ja-JP"];
const HAN_REGEX = /\p{Script=Han}/u;
const JS_HELPERS = {
  localized: "__i18nLocalized",
  localizedTemplate: "__i18nLocalizedTemplate",
};
const TEMPLATE_HELPERS = {
  localized: "$ls",
  localizedTemplate: "$lt",
};
const RUNTIME_IMPORT = "@/i18n/runtime";
const BABEL_PLUGINS = [
  "jsx",
  "typescript",
  "classProperties",
  "classPrivateProperties",
  "classPrivateMethods",
  "topLevelAwait",
  "importAttributes",
];

function containsHan(value) {
  return typeof value === "string" && HAN_REGEX.test(value);
}

function toJSONLiteral(value) {
  return JSON.stringify(value);
}

function toSingleQuotedLiteral(value) {
  return `'${String(value)
    .replace(/\\/g, "\\\\")
    .replace(/'/g, "\\'")
    .replace(/\r/g, "\\r")
    .replace(/\n/g, "\\n")
    .replace(/\u2028/g, "\\u2028")
    .replace(/\u2029/g, "\\u2029")}'`;
}

function hashMessageID(message) {
  return createHash("sha256").update(message).digest("hex").slice(0, 16);
}

function stripQuery(id) {
  return id.split("?")[0];
}

function isSourceFile(id) {
  const cleanID = stripQuery(id);
  return /\.(?:js|jsx|ts|tsx|vue)$/.test(cleanID);
}

function isExcludedFile(rootDir, id) {
  const cleanID = normalizePath(stripQuery(id));
  if (cleanID.includes("/node_modules/")) {
    return true;
  }

  const relativePath = normalizePath(path.relative(rootDir, cleanID));
  if (!relativePath.startsWith("src/")) {
    return true;
  }

  return relativePath.startsWith("src/i18n/");
}

function readJSONFile(filePath, fallback) {
  if (!fs.existsSync(filePath)) {
    return fallback;
  }

  const raw = fs.readFileSync(filePath, "utf8").trim();
  if (!raw) {
    return fallback;
  }

  return JSON.parse(raw);
}

function ensureDirectory(filePath) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
}

function writeJSONFile(filePath, payload) {
  ensureDirectory(filePath);
  fs.writeFileSync(filePath, `${JSON.stringify(payload, null, 2)}\n`);
}

function buildRef(filePath, rootDir, loc) {
  return {
    file: normalizePath(path.relative(rootDir, filePath)),
    line: loc?.line ?? 1,
    column: loc?.column ?? 1,
  };
}

function buildMessageRecord(filePath, rootDir, canonical, placeholders, loc) {
  const id = hashMessageID(canonical);
  return {
    id,
    source: canonical,
    kind: placeholders > 0 ? "template" : "text",
    placeholders,
    ref: buildRef(filePath, rootDir, loc),
  };
}

function mergeMessageRecords(records) {
  const entries = new Map();

  for (const record of records) {
    const current = entries.get(record.id);
    if (!current) {
      entries.set(record.id, {
        source: record.source,
        kind: record.kind,
        placeholders: record.placeholders,
        refs: [record.ref],
      });
      continue;
    }

    if (current.source !== record.source) {
      throw new Error(
        `[static-i18n] Message id collision for ${record.id}: ${current.source} <> ${record.source}`,
      );
    }

    current.refs.push(record.ref);
  }

  const sortedEntries = {};
  for (const id of Array.from(entries.keys()).sort()) {
    const entry = entries.get(id);
    sortedEntries[id] = {
      source: entry.source,
      kind: entry.kind,
      placeholders: entry.placeholders,
      refs: entry.refs.sort((left, right) =>
        left.file.localeCompare(right.file) ||
        left.line - right.line ||
        left.column - right.column),
    };
  }

  return { entries: sortedEntries };
}

function mergeLocaleMessages(existingMessages, catalogEntries, locale) {
  const nextMessages = {};

  for (const id of Object.keys(catalogEntries)) {
    if (locale === SOURCE_LANGUAGE) {
      nextMessages[id] = catalogEntries[id].source;
      continue;
    }

    const currentValue = existingMessages?.[id];
    nextMessages[id] = typeof currentValue === "string" ? currentValue : "";
  }

  return nextMessages;
}

function walkSourceFiles(dirPath, visitor) {
  const entries = fs.readdirSync(dirPath, { withFileTypes: true });
  for (const entry of entries) {
    const nextPath = path.join(dirPath, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "node_modules") {
        continue;
      }
      walkSourceFiles(nextPath, visitor);
      continue;
    }

    visitor(nextPath);
  }
}

function parseProgram(code, filename) {
  try {
    return parseJavaScript(code, {
      sourceType: "module",
      sourceFilename: filename,
      plugins: BABEL_PLUGINS,
    });
  } catch (error) {
    throw new Error(`[static-i18n] Failed to parse ${filename}: ${error.message}`);
  }
}

function createTemplateCanonical(node) {
  const parts = [];
  for (let index = 0; index < node.quasis.length; index += 1) {
    const quasi = node.quasis[index];
    parts.push(quasi.value.cooked ?? quasi.value.raw ?? "");
    if (index < node.expressions.length) {
      parts.push(`{${index}}`);
    }
  }
  return parts.join("");
}

function shouldIgnoreStringLiteral(path) {
  const parent = path.parentPath;
  if (!parent) {
    return false;
  }

  if (
    parent.isImportDeclaration() ||
    parent.isExportAllDeclaration() ||
    parent.isExportNamedDeclaration()
  ) {
    return true;
  }

  if (parent.isDirective()) {
    return true;
  }

  if (
    (parent.isObjectProperty() || parent.isObjectMethod()) &&
    path.key === "key" &&
    parent.node.computed !== true
  ) {
    return true;
  }

  if (
    (parent.isMemberExpression() || parent.isOptionalMemberExpression?.()) &&
    path.key === "property" &&
    parent.node.computed !== true
  ) {
    return true;
  }

  if (
    (parent.isClassMethod?.() || parent.isClassProperty?.() || parent.isClassPrivateProperty?.()) &&
    path.key === "key"
  ) {
    return true;
  }

  return false;
}

function shouldIgnoreTemplateLiteral(path) {
  return path.parentPath?.isTaggedTemplateExpression?.() === true;
}

function createStringLiteralReplacement(path, helperNames, record, quoteLiteral) {
  if (path.parentPath?.isJSXAttribute?.() && path.key === "value") {
    return `{${helperNames.localized}(${quoteLiteral(record.id)}, ${quoteLiteral(record.source)})}`;
  }

  return `${helperNames.localized}(${quoteLiteral(record.id)}, ${quoteLiteral(record.source)})`;
}

function createTemplateLiteralReplacement(path, code, helperNames, record, quoteLiteral) {
  if (path.node.expressions.length === 0) {
    return `${helperNames.localized}(${quoteLiteral(record.id)}, ${quoteLiteral(record.source)})`;
  }

  const args = path.node.expressions.map((expression) => code.slice(expression.start, expression.end));
  const payload = `[${args.join(", ")}]`;
  return `${helperNames.localizedTemplate}(${quoteLiteral(record.id)}, ${quoteLiteral(record.source)}, ${payload})`;
}

function collectJSReplacements(code, ast, filePath, rootDir, helperNames, options = {}) {
  const replacements = [];
  const records = [];
  const helperUsage = {
    localized: false,
    localizedTemplate: false,
  };
  const refLoc = options.refLoc ?? null;
  const quoteLiteral = options.quoteLiteral ?? toJSONLiteral;

  function resolveLoc(node) {
    if (refLoc) {
      return refLoc;
    }

    return node?.loc?.start
      ? {
        line: node.loc.start.line,
        column: node.loc.start.column + 1,
      }
      : { line: 1, column: 1 };
  }

  traverse(ast, {
    noScope: true,
    StringLiteral(path) {
      if (shouldIgnoreStringLiteral(path) || !containsHan(path.node.value)) {
        return;
      }

      const record = buildMessageRecord(filePath, rootDir, path.node.value, 0, resolveLoc(path.node));
      records.push(record);
      helperUsage.localized = true;
      replacements.push({
        start: path.node.start,
        end: path.node.end,
        text: createStringLiteralReplacement(path, helperNames, record, quoteLiteral),
      });
    },
    TemplateLiteral(path) {
      if (shouldIgnoreTemplateLiteral(path)) {
        return;
      }

      const canonical = createTemplateCanonical(path.node);
      if (!containsHan(canonical)) {
        return;
      }

      const record = buildMessageRecord(
        filePath,
        rootDir,
        canonical,
        path.node.expressions.length,
        resolveLoc(path.node),
      );
      records.push(record);
      if (path.node.expressions.length === 0) {
        helperUsage.localized = true;
      } else {
        helperUsage.localizedTemplate = true;
      }
      replacements.push({
        start: path.node.start,
        end: path.node.end,
        text: createTemplateLiteralReplacement(path, code, helperNames, record, quoteLiteral),
      });
    },
  });

  return {
    replacements,
    records,
    helperUsage,
  };
}

function applyReplacements(code, replacements) {
  if (!replacements.length) {
    return null;
  }

  const magicString = new MagicString(code);
  const sortedReplacements = [...replacements].sort((left, right) => right.start - left.start);
  for (const replacement of sortedReplacements) {
    magicString.overwrite(replacement.start, replacement.end, replacement.text);
  }

  return magicString;
}

function ensureRuntimeImport(code, helperUsage) {
  if (!helperUsage.localized && !helperUsage.localizedTemplate) {
    return code;
  }

  const pieces = [];
  if (helperUsage.localized) {
    pieces.push(`localized as ${JS_HELPERS.localized}`);
  }
  if (helperUsage.localizedTemplate) {
    pieces.push(`localizedTemplate as ${JS_HELPERS.localizedTemplate}`);
  }

  return `import { ${pieces.join(", ")} } from "${RUNTIME_IMPORT}";\n${code}`;
}

function transformJavaScript(code, filePath, rootDir, options = {}) {
  const ast = parseProgram(code, filePath);
  const result = collectJSReplacements(
    code,
    ast,
    filePath,
    rootDir,
    options.helperNames ?? JS_HELPERS,
    {
      refLoc: options.refLoc,
      quoteLiteral: options.quoteLiteral,
    },
  );
  const magicString = applyReplacements(code, result.replacements);
  const transformedCode = options.injectImport === false
    ? magicString?.toString() ?? code
    : ensureRuntimeImport(magicString?.toString() ?? code, result.helperUsage);

  return {
    code: transformedCode,
    changed: transformedCode !== code,
    records: result.records,
    map: magicString
      ? magicString.generateMap({
        source: filePath,
        hires: true,
      })
      : null,
  };
}

function translateTemplateExpression(expression, filePath, rootDir, refLoc) {
  const wrappedCode = `(${expression})`;

  try {
    const transformed = transformJavaScript(wrappedCode, filePath, rootDir, {
      helperNames: TEMPLATE_HELPERS,
      injectImport: false,
      refLoc,
      quoteLiteral: toSingleQuotedLiteral,
    });
    const nextCode = transformed.code.slice(1, -1);
    return {
      code: nextCode,
      changed: nextCode !== expression,
      records: transformed.records,
    };
  } catch (_error) {
    const transformed = transformJavaScript(expression, filePath, rootDir, {
      helperNames: TEMPLATE_HELPERS,
      injectImport: false,
      refLoc,
      quoteLiteral: toSingleQuotedLiteral,
    });
    return {
      code: transformed.code,
      changed: transformed.code !== expression,
      records: transformed.records,
    };
  }
}

function createTextNodeReplacement(source, record) {
  const trimmed = source.trim();
  if (!trimmed) {
    return null;
  }

  const leadingLength = source.indexOf(trimmed);
  const leading = leadingLength > 0 ? source.slice(0, leadingLength) : "";
  const trailing = source.slice(leadingLength + trimmed.length);
  return `${leading}{{ $ls(${toSingleQuotedLiteral(record.id)}, ${toSingleQuotedLiteral(record.source)}) }}${trailing}`;
}

function walkTemplateNode(node, visitor) {
  visitor(node);

  if (Array.isArray(node.branches)) {
    node.branches.forEach((branch) => walkTemplateNode(branch, visitor));
  }

  if (Array.isArray(node.children)) {
    node.children.forEach((child) => walkTemplateNode(child, visitor));
  }

  if (node.type === 1 && Array.isArray(node.props)) {
    for (const prop of node.props) {
      visitor(prop, node);
      if (prop.exp) {
        visitor(prop.exp, prop);
      }
      if (prop.arg) {
        visitor(prop.arg, prop);
      }
    }
  }

  if (node.type === 5 && node.content) {
    visitor(node.content, node);
  }
}

function transformVueTemplate(templateCode, filePath, rootDir) {
  const ast = parseTemplate(templateCode, { comments: true });
  const replacements = [];
  const records = [];

  walkTemplateNode(ast, (node, parent) => {
    if (node.type === 2 && containsHan(node.content)) {
      const record = buildMessageRecord(
        filePath,
        rootDir,
        node.content.trim(),
        0,
        {
          line: node.loc.start.line,
          column: node.loc.start.column + 1,
        },
      );
      const replacement = createTextNodeReplacement(node.loc.source, record);
      if (!replacement) {
        return;
      }

      records.push(record);
      replacements.push({
        start: node.loc.start.offset,
        end: node.loc.end.offset,
        text: replacement,
      });
      return;
    }

    if (node.type === 6 && node.value && containsHan(node.value.content)) {
      const record = buildMessageRecord(
        filePath,
        rootDir,
        node.value.content,
        0,
        {
          line: node.loc.start.line,
          column: node.loc.start.column + 1,
        },
      );
      records.push(record);
      replacements.push({
        start: node.loc.start.offset,
        end: node.loc.end.offset,
        text: `:${node.name}="$ls(${toSingleQuotedLiteral(record.id)}, ${toSingleQuotedLiteral(record.source)})"`,
      });
      return;
    }

    if (
      node.type === 4 &&
      typeof node.content === "string" &&
      containsHan(node.content) &&
      parent &&
      ((parent.type === 5) || (parent.type === 7 && parent.exp === node))
    ) {
      const transformed = translateTemplateExpression(
        node.content,
        filePath,
        rootDir,
        {
          line: node.loc.start.line,
          column: node.loc.start.column + 1,
        },
      );
      if (!transformed.changed) {
        return;
      }

      records.push(...transformed.records);
      replacements.push({
        start: node.loc.start.offset,
        end: node.loc.end.offset,
        text: transformed.code,
      });
    }
  });

  const magicString = applyReplacements(templateCode, replacements);
  return {
    code: magicString?.toString() ?? templateCode,
    changed: Boolean(magicString),
    records,
  };
}

function transformVueSFC(code, filePath, rootDir) {
  const { descriptor } = parseSFC(code, { filename: filePath });
  const magicString = new MagicString(code);
  const records = [];
  let changed = false;

  if (descriptor.template) {
    const templateResult = transformVueTemplate(descriptor.template.content, filePath, rootDir);
    records.push(...templateResult.records);
    if (templateResult.changed) {
      changed = true;
      magicString.overwrite(
        descriptor.template.loc.start.offset,
        descriptor.template.loc.end.offset,
        templateResult.code,
      );
    }
  }

  for (const block of [descriptor.script, descriptor.scriptSetup].filter(Boolean)) {
    const scriptResult = transformJavaScript(block.content, filePath, rootDir, {
      helperNames: JS_HELPERS,
      injectImport: true,
    });
    records.push(...scriptResult.records);
    if (scriptResult.changed) {
      changed = true;
      magicString.overwrite(block.loc.start.offset, block.loc.end.offset, scriptResult.code);
    }
  }

  return {
    code: changed ? magicString.toString() : code,
    changed,
    records,
    map: changed
      ? magicString.generateMap({
        source: filePath,
        hires: true,
      })
      : null,
  };
}

function transformSourceCode(code, filePath, rootDir) {
  if (filePath.endsWith(".vue")) {
    return transformVueSFC(code, filePath, rootDir);
  }

  return transformJavaScript(code, filePath, rootDir, {
    helperNames: JS_HELPERS,
    injectImport: true,
  });
}

function collectCatalogRecords(rootDir) {
  const srcDir = path.join(rootDir, "src");
  const records = [];

  walkSourceFiles(srcDir, (filePath) => {
    const cleanPath = normalizePath(filePath);
    if (!isSourceFile(cleanPath) || isExcludedFile(rootDir, cleanPath)) {
      return;
    }

    const code = fs.readFileSync(cleanPath, "utf8");
    const result = transformSourceCode(code, cleanPath, rootDir);
    records.push(...result.records);
  });

  return records;
}

function syncCatalogFiles(rootDir) {
  const records = collectCatalogRecords(rootDir);
  const catalog = mergeMessageRecords(records);
  const generatedDir = path.join(rootDir, "src/i18n/generated");
  const localesDir = path.join(rootDir, "src/i18n/locales");

  writeJSONFile(path.join(generatedDir, "catalog.json"), catalog);

  for (const locale of SUPPORTED_LOCALES) {
    const localePath = path.join(localesDir, `${locale}.json`);
    const previousMessages = readJSONFile(localePath, {});
    const nextMessages = mergeLocaleMessages(previousMessages, catalog.entries, locale);
    writeJSONFile(localePath, nextMessages);
  }
}

export function staticI18nPlugin() {
  let rootDir = process.cwd();
  const shouldScan = process.argv.includes("--scan") || process.env.STATIC_I18N_SCAN === "true";

  return {
    name: "cursor-static-i18n",
    enforce: "pre",
    configResolved(config) {
      rootDir = config.root;
    },
    buildStart() {
      if (!shouldScan) {
        return;
      }

      syncCatalogFiles(rootDir);
    },
    transform(code, id) {
      if (!isSourceFile(id) || isExcludedFile(rootDir, id)) {
        return null;
      }

      const filePath = stripQuery(id);
      const result = transformSourceCode(code, filePath, rootDir);
      if (!result.changed) {
        return null;
      }

      return {
        code: result.code,
        map: result.map,
      };
    },
  };
}
