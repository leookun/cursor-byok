package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/desc/protoparse"
)

// isGooglePkg checks if a package is a Google standard package that should not be generated
func isGooglePkg(pkg string) bool {
	return pkg == "google.protobuf" || pkg == "google.rpc"
}

// Scalar type mapping
var scalarTypes = map[int]string{
	1:  "double",
	2:  "float",
	3:  "int64",
	4:  "uint64",
	5:  "int32",
	6:  "fixed64",
	7:  "fixed32",
	8:  "bool",
	9:  "string",
	12: "bytes",
	13: "uint32",
	15: "sfixed32",
	16: "sfixed64",
	17: "sint32",
	18: "sint64",
}

var strictExtractionValidation = true

type extractionDiagnostics struct {
	totalFieldObjects   int
	parsedFieldObjects  int
	skippedFieldObjects int
	skippedFieldSamples []string
	unresolvedTypeRefs  map[string]int
	emptyMessages       []string
	placeholderHits     []string
}

func newExtractionDiagnostics() *extractionDiagnostics {
	return &extractionDiagnostics{
		unresolvedTypeRefs: make(map[string]int),
	}
}

func (d *extractionDiagnostics) addSkippedField(fieldObject string, reason error) {
	if d == nil {
		return
	}
	d.totalFieldObjects++
	d.skippedFieldObjects++
	if len(d.skippedFieldSamples) < 20 {
		trimmed := strings.TrimSpace(fieldObject)
		if len(trimmed) > 140 {
			trimmed = trimmed[:140] + "..."
		}
		if reason != nil {
			d.skippedFieldSamples = append(d.skippedFieldSamples, fmt.Sprintf("%s | %s", reason.Error(), trimmed))
		} else {
			d.skippedFieldSamples = append(d.skippedFieldSamples, trimmed)
		}
	}
}

func (d *extractionDiagnostics) addParsedField() {
	if d == nil {
		return
	}
	d.totalFieldObjects++
	d.parsedFieldObjects++
}

func (d *extractionDiagnostics) addUnresolvedType(ref string) {
	if d == nil {
		return
	}
	key := strings.TrimSpace(ref)
	if key == "" {
		key = "<empty>"
	}
	d.unresolvedTypeRefs[key]++
}

func SetStrictMode(enabled bool) {
	strictExtractionValidation = enabled
}

var activeDiagnostics *extractionDiagnostics

var (
	noRe          = regexp.MustCompile(`(?:^|[,{]\s*)no:\s*(\d+)`)
	nameRe        = regexp.MustCompile(`(?:^|[,{]\s*)name:\s*["']([^"']+)["']`)
	kindRe        = regexp.MustCompile(`(?:^|[,{]\s*)kind:\s*["']([^"']+)["']`)
	enumTypeRe    = regexp.MustCompile(`[,\s]T:\s*[\w$.]+\.getEnumType\s*\(\s*([\w$.]+)\s*\)`)
	tRe           = regexp.MustCompile(`[,\s]T:\s*([\w$.]+)`)
	oneofRe       = regexp.MustCompile(`oneof:\s*["']([^"']+)["']`)
	repeatedRe    = regexp.MustCompile(`repeated:\s*(!0|true)`)
	optRe         = regexp.MustCompile(`opt:\s*(!0|true)`)
	keyRe         = regexp.MustCompile(`[,\s]K:\s*(\d+)`)
	mapValueRe    = regexp.MustCompile(`V:\s*\{([^}]*)\}`)
	mapValueKRe   = regexp.MustCompile(`(?:^|[,{]\s*)kind:\s*["'](\w+)["']`)
	mapValueTRe   = regexp.MustCompile(`[,\s]T:\s*([\w$.]+)`)
	oneofNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	fieldNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	placeholderRe = regexp.MustCompile(`^\s*(optional\s+|repeated\s+)?[A-Za-z_][A-Za-z0-9_.<>]*\s+(field_\d+|unknown(?:_[A-Za-z0-9_]+)?)\s*=\s*\d+\s*;`)
	varAliasRe    = regexp.MustCompile(`\b(?:let|const|var)\s+([\w$]+)\s*=\s*([\w$]+)\s*(?:[,;])`)
	streamCloseRe = regexp.MustCompile(`(?s)message\s+ExecClientControlMessage\s*\{.*?ExecClientStreamClose\s+stream_close\s*=\s*1\s*;`)
	shellStdoutRe = regexp.MustCompile(`(?s)message\s+ShellStream\s*\{.*?ShellStreamStdout\s+stdout\s*=\s*1\s*;`)
)

type Field struct {
	No           int    `json:"no"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	T            any    `json:"T"`     // int for scalar, string for message ref
	Oneof        string `json:"oneof"` // oneof group name
	Repeated     bool   `json:"repeated"`
	Opt          bool   `json:"opt"` // optional
	MapKey       int    `json:"K"`   // map key type (scalar type number)
	MapValueKind string // "scalar" or "message"
	MapValueT    any    // scalar type number or message var name
}

type Message struct {
	TypeName     string
	VarName      string // JS external variable name (e.g., tPe)
	InternalName string // JS internal class name (e.g., bd)
	Fields       []Field
	Package      string
	ShortName    string
	Pos          int
	ModuleStart  int
}

type Enum struct {
	TypeName    string
	VarName     string
	Values      []EnumValue
	Package     string
	ShortName   string
	Pos         int
	ModuleStart int
}

type EnumValue struct {
	No   int
	Name string
}

type Service struct {
	TypeName    string
	VarName     string
	Methods     []Method
	Package     string
	ShortName   string
	Pos         int
	ModuleStart int
}

type Method struct {
	Name       string
	InputType  string // variable name
	OutputType string // variable name
	Kind       string // Unary, ServerStreaming, ClientStreaming, BiDiStreaming
}

type symbolDef struct {
	TypeName    string
	Pos         int
	Kind        string
	ModuleStart int
}

type TypeResolver struct {
	bySymbol map[string][]symbolDef
	byShort  map[string][]symbolDef
}

func newTypeResolver(messages []Message, enums []Enum, aliases map[string][]string) *TypeResolver {
	resolver := &TypeResolver{
		bySymbol: make(map[string][]symbolDef),
		byShort:  make(map[string][]symbolDef),
	}

	add := func(symbol, typeName string, pos int, moduleStart int, kind string) {
		symbol = strings.TrimSpace(symbol)
		typeName = strings.TrimSpace(typeName)
		if symbol == "" || typeName == "" {
			return
		}
		def := symbolDef{TypeName: typeName, Pos: pos, ModuleStart: moduleStart, Kind: kind}
		resolver.bySymbol[symbol] = append(resolver.bySymbol[symbol], def)
		_, shortName := parseTypeName(typeName)
		if shortName != "" {
			resolver.byShort[shortName] = append(resolver.byShort[shortName], def)
			underscoreAlias := strings.ReplaceAll(shortName, ".", "_")
			if underscoreAlias != shortName {
				resolver.byShort[underscoreAlias] = append(resolver.byShort[underscoreAlias], def)
			}
			if idx := strings.LastIndex(shortName, "."); idx > 0 && idx+1 < len(shortName) {
				resolver.byShort[shortName[idx+1:]] = append(resolver.byShort[shortName[idx+1:]], def)
			}
			if idx := strings.LastIndex(underscoreAlias, "_"); idx > 0 && idx+1 < len(underscoreAlias) {
				resolver.byShort[underscoreAlias[idx+1:]] = append(resolver.byShort[underscoreAlias[idx+1:]], def)
			}
		}
	}

	for _, msg := range messages {
		add(msg.VarName, msg.TypeName, msg.Pos, msg.ModuleStart, "message")
		if msg.InternalName != "" && msg.InternalName != msg.VarName {
			add(msg.InternalName, msg.TypeName, msg.Pos, msg.ModuleStart, "message")
		}
		for _, alias := range aliasesForSymbols(aliases, msg.VarName, msg.InternalName) {
			add(alias, msg.TypeName, msg.Pos, msg.ModuleStart, "message")
		}
	}
	for _, enum := range enums {
		add(enum.VarName, enum.TypeName, enum.Pos, enum.ModuleStart, "enum")
		for _, alias := range aliasesForSymbols(aliases, enum.VarName) {
			add(alias, enum.TypeName, enum.Pos, enum.ModuleStart, "enum")
		}
	}

	return resolver
}

func buildAliasIndex(text string) map[string][]string {
	matches := varAliasRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	direct := make(map[string]string, len(matches))
	for _, match := range matches {
		alias := strings.TrimSpace(match[1])
		target := strings.TrimSpace(match[2])
		if alias == "" || target == "" || alias == target {
			continue
		}
		direct[alias] = target
	}

	resolveRoot := func(symbol string) string {
		seen := make(map[string]bool)
		current := symbol
		for {
			if seen[current] {
				return symbol
			}
			seen[current] = true
			next := direct[current]
			if next == "" {
				return current
			}
			current = next
		}
	}

	aliases := make(map[string][]string)
	for alias := range direct {
		root := resolveRoot(alias)
		if root == alias {
			continue
		}
		aliases[root] = append(aliases[root], alias)
	}
	for root := range aliases {
		sort.Strings(aliases[root])
	}
	return aliases
}

func aliasesForSymbols(aliases map[string][]string, symbols ...string) []string {
	if len(aliases) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, symbol := range symbols {
		for _, alias := range aliases[strings.TrimSpace(symbol)] {
			if alias == "" || seen[alias] {
				continue
			}
			seen[alias] = true
			result = append(result, alias)
		}
	}
	sort.Strings(result)
	return result
}

func looksLikeFullTypeName(ref string) bool {
	trimmed := strings.TrimSpace(ref)
	if strings.HasPrefix(trimmed, "google.protobuf.") || strings.HasPrefix(trimmed, "google.rpc.") {
		return true
	}
	matched, _ := regexp.MatchString(`^[\w.]+\.v\d+\.[\w.]+$`, trimmed)
	return matched
}

func pickBestDefinition(candidates []symbolDef, contextPos int, contextModuleStart int, preferredPkg string, expectedKind string) (symbolDef, bool) {
	if len(candidates) == 0 {
		return symbolDef{}, false
	}

	filtered := candidates
	if strings.TrimSpace(preferredPkg) != "" {
		tmp := make([]symbolDef, 0, len(candidates))
		for _, item := range candidates {
			pkg, _ := parseTypeName(item.TypeName)
			if pkg == preferredPkg {
				tmp = append(tmp, item)
			}
		}
		if len(tmp) > 0 {
			filtered = tmp
		}
	}

	if strings.TrimSpace(expectedKind) != "" {
		tmp := make([]symbolDef, 0, len(filtered))
		for _, item := range filtered {
			if item.Kind == expectedKind {
				tmp = append(tmp, item)
			}
		}
		if len(tmp) > 0 {
			filtered = tmp
		}
	}

	if contextModuleStart > 0 {
		tmp := make([]symbolDef, 0, len(filtered))
		for _, item := range filtered {
			if item.ModuleStart == contextModuleStart {
				tmp = append(tmp, item)
			}
		}
		if len(tmp) > 0 {
			filtered = tmp
		}
	}

	// Pick absolute nearest definition, prefer previous if distance ties.
	bestIndex := -1
	bestDistance := 0
	bestIsFuture := false
	for index, item := range filtered {
		distance := absInt(item.Pos - contextPos)
		isFuture := item.Pos > contextPos
		if bestIndex == -1 {
			bestIndex = index
			bestDistance = distance
			bestIsFuture = isFuture
			continue
		}
		if distance < bestDistance {
			bestIndex = index
			bestDistance = distance
			bestIsFuture = isFuture
			continue
		}
		if distance == bestDistance {
			// Same distance: prefer previous definition over future.
			if bestIsFuture && !isFuture {
				bestIndex = index
				bestIsFuture = isFuture
			}
		}
	}
	if bestIndex < 0 {
		return symbolDef{}, false
	}
	return filtered[bestIndex], true
}

func (resolver *TypeResolver) ResolveTypeName(ref string, contextPos int, contextModuleStart int, preferredPkg string, expectedKind string) (string, bool) {
	if resolver == nil {
		return "", false
	}

	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", false
	}
	if looksLikeFullTypeName(trimmed) {
		return trimmed, true
	}

	resolveBySymbol := func(symbol string, preferSameModule bool) (string, bool) {
		candidates := resolver.bySymbol[symbol]
		if len(candidates) == 0 {
			return "", false
		}
		moduleStart := 0
		if preferSameModule {
			moduleStart = contextModuleStart
		}
		best, ok := pickBestDefinition(candidates, contextPos, moduleStart, preferredPkg, expectedKind)
		if !ok {
			return "", false
		}
		return best.TypeName, true
	}
	resolveByShort := func(symbol string, preferSameModule bool) (string, bool) {
		candidates := resolver.byShort[symbol]
		if len(candidates) == 0 {
			return "", false
		}
		moduleStart := 0
		if preferSameModule {
			moduleStart = contextModuleStart
		}
		best, ok := pickBestDefinition(candidates, contextPos, moduleStart, preferredPkg, expectedKind)
		if !ok {
			return "", false
		}
		return best.TypeName, true
	}

	if typeName, ok := resolveBySymbol(trimmed, !strings.Contains(trimmed, ".")); ok {
		return typeName, true
	}
	if typeName, ok := resolveByShort(trimmed, !strings.Contains(trimmed, ".")); ok {
		return typeName, true
	}

	if strings.Contains(trimmed, ".") {
		parts := strings.Split(trimmed, ".")
		last := parts[len(parts)-1]
		if typeName, ok := resolveByShort(last, false); ok {
			return typeName, true
		}
		first := parts[0]
		if typeName, ok := resolveBySymbol(first, false); ok {
			return typeName, true
		}
	}

	return "", false
}

func fallbackTypeToken(ref string) string {
	token := strings.TrimSpace(ref)
	if token == "" {
		return token
	}
	if strings.Contains(token, ".") {
		parts := strings.Split(token, ".")
		return parts[len(parts)-1]
	}
	return token
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

var moduleStartRe = regexp.MustCompile(`(?:^|,)(\d+):(?:function\([\w$,]*\)|\([\w$,]*\)=>)\{`)

func buildModuleStarts(text string) []int {
	matches := moduleStartRe.FindAllStringSubmatchIndex(text, -1)
	starts := make([]int, 0, len(matches))
	for _, match := range matches {
		starts = append(starts, match[0])
	}
	return starts
}

func moduleStartForPos(moduleStarts []int, pos int) int {
	if len(moduleStarts) == 0 {
		return 0
	}
	index := sort.Search(len(moduleStarts), func(i int) bool {
		return moduleStarts[i] > pos
	}) - 1
	if index < 0 {
		return 0
	}
	return moduleStarts[index]
}

// ExtractProtos extracts proto definitions from formatted JS file
func ExtractProtos(inputFile, outputDir string) {
	activeDiagnostics = newExtractionDiagnostics()
	defer func() {
		activeDiagnostics = nil
	}()

	content, err := os.ReadFile(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		os.Exit(1)
	}

	text := string(content)
	moduleStarts := buildModuleStarts(text)
	aliases := buildAliasIndex(text)

	// Extract messages, enums, and services
	messages := extractMessages(text, moduleStarts)
	enums := extractEnums(text, moduleStarts)
	services := extractServices(text, moduleStarts)
	for _, msg := range messages {
		if len(msg.Fields) == 0 {
			activeDiagnostics.emptyMessages = append(activeDiagnostics.emptyMessages, msg.TypeName)
		}
	}

	resolver := newTypeResolver(messages, enums, aliases)

	// Generate proto files
	generateProtos(messages, enums, services, resolver, outputDir)

	validateErr := validateGeneratedProtos(outputDir, activeDiagnostics)

	printDiagnosticsSummary(activeDiagnostics)

	if strictExtractionValidation && hasValidationFailure(activeDiagnostics, validateErr) {
		if validateErr != nil {
			fmt.Fprintf(os.Stderr, "Validation failed: %v\n", validateErr)
		}
		os.Exit(1)
	}

	if validateErr != nil {
		fmt.Fprintf(os.Stderr, "Validation warning: %v\n", validateErr)
	}

	fmt.Printf("提取完成: %d 个消息, %d 个枚举, %d 个服务\n", len(messages), len(enums), len(services))
}

func hasValidationFailure(diag *extractionDiagnostics, validateErr error) bool {
	if validateErr != nil {
		return true
	}
	if diag == nil {
		return false
	}
	if diag.skippedFieldObjects > 0 {
		return true
	}
	if len(diag.unresolvedTypeRefs) > 0 {
		return true
	}
	if len(diag.placeholderHits) > 0 {
		return true
	}
	return false
}

func printDiagnosticsSummary(diag *extractionDiagnostics) {
	if diag == nil {
		return
	}

	fmt.Printf(
		"诊断汇总: fields %d/%d 解析成功, skipped=%d, unresolved=%d, placeholders=%d, empty_messages=%d\n",
		diag.parsedFieldObjects,
		diag.totalFieldObjects,
		diag.skippedFieldObjects,
		len(diag.unresolvedTypeRefs),
		len(diag.placeholderHits),
		len(diag.emptyMessages),
	)

	if diag.skippedFieldObjects > 0 && len(diag.skippedFieldSamples) > 0 {
		fmt.Println("字段解析失败样例:")
		for _, sample := range diag.skippedFieldSamples {
			fmt.Printf("  - %s\n", sample)
		}
	}

	if len(diag.unresolvedTypeRefs) > 0 {
		keys := make([]string, 0, len(diag.unresolvedTypeRefs))
		for key := range diag.unresolvedTypeRefs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Println("未解析类型引用:")
		for _, key := range keys {
			fmt.Printf("  - %s (%d)\n", key, diag.unresolvedTypeRefs[key])
		}
	}

	if len(diag.placeholderHits) > 0 {
		fmt.Println("占位字段命中:")
		for i, hit := range diag.placeholderHits {
			if i >= 20 {
				fmt.Printf("  - ... and %d more\n", len(diag.placeholderHits)-20)
				break
			}
			fmt.Printf("  - %s\n", hit)
		}
	}
}

func validateGeneratedProtos(outputDir string, diag *extractionDiagnostics) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("read output dir failed: %w", err)
	}

	protoFiles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".proto") {
			protoFiles = append(protoFiles, name)
		}
	}
	if len(protoFiles) == 0 {
		return errors.New("no generated proto files found")
	}
	sort.Strings(protoFiles)

	for _, file := range protoFiles {
		body, readErr := os.ReadFile(filepath.Join(outputDir, file))
		if readErr != nil {
			return fmt.Errorf("read generated proto failed: %s: %w", file, readErr)
		}
		lines := strings.Split(string(body), "\n")
		for idx, line := range lines {
			if placeholderRe.MatchString(line) && diag != nil {
				hit := fmt.Sprintf("%s:%d: %s", file, idx+1, strings.TrimSpace(line))
				diag.placeholderHits = append(diag.placeholderHits, hit)
			}
		}
		if err := validateRequiredAgentShapes(file, string(body)); err != nil {
			return err
		}
	}

	parser := protoparse.Parser{
		ImportPaths:  []string{outputDir},
		LookupImport: desc.LoadFileDescriptor,
	}
	if _, parseErr := parser.ParseFiles(protoFiles...); parseErr != nil {
		return fmt.Errorf("parse generated proto failed: %w", parseErr)
	}

	return nil
}

func validateRequiredAgentShapes(file string, body string) error {
	if strings.Contains(body, "message ExecClientControlMessage") && !streamCloseRe.MatchString(body) {
		return fmt.Errorf("%s: ExecClientControlMessage.stream_close must be ExecClientStreamClose", file)
	}
	if strings.Contains(body, "message ShellStream") && !shellStdoutRe.MatchString(body) {
		return fmt.Errorf("%s: ShellStream.stdout must be ShellStreamStdout", file)
	}
	return nil
}

func extractMessages(text string, moduleStarts []int) []Message {
	var messages []Message

	// Pattern 1: VarName = class InternalName extends l { ... this.typeName = "..." ... this.fields = ... }
	// 先找所有 "变量名 = class 内部类名" 定义
	// JS 变量名可以包含 $ 符号，如 B$e, qg 等
	// 需要同时捕获外部变量名和内部类名，因为字段引用可能用任一个
	classDefRe := regexp.MustCompile(`([\w$]+)\s*=\s*class\s+([\w$]+)\s+extends\s+[\w$.]+\s*\{`)
	classMatches := classDefRe.FindAllStringSubmatchIndex(text, -1)

	// Pattern: this.typeName = "xxx.v1.YYY" (any package)
	typeNameRe := regexp.MustCompile(`this\.typeName\s*=\s*"([\w.]+)"`)

	// Pattern: this.fields = n.util.newFieldList(() => [...])
	fieldsRe := regexp.MustCompile(`this\.fields\s*=\s*\w+(?:\.proto3)?\.util\.newFieldList\s*\(\s*\(\s*\)\s*=>\s*\[`)

	for _, classMatch := range classMatches {
		varName := text[classMatch[2]:classMatch[3]]
		internalName := text[classMatch[4]:classMatch[5]]
		classStart := classMatch[0]

		// 找到类的结束位置（匹配大括号）
		classEnd := findClassEnd(text, classMatch[1]-1)
		if classEnd == -1 {
			continue
		}

		classBody := text[classStart:classEnd]

		// 在类体内查找 typeName
		typeMatch := typeNameRe.FindStringSubmatch(classBody)
		if typeMatch == nil {
			continue
		}
		typeName := typeMatch[1]

		// 在类体内查找 fields
		fieldsMatch := fieldsRe.FindStringIndex(classBody)
		if fieldsMatch == nil {
			continue
		}

		// 找到 fields 数组的开始位置
		bracketPos := classStart + fieldsMatch[1] - 1
		fields := extractFieldArray(text, bracketPos)

		pkg, shortName := parseTypeName(typeName)
		msg := Message{
			TypeName:     typeName,
			VarName:      varName,
			InternalName: internalName,
			Fields:       fields,
			Package:      pkg,
			ShortName:    shortName,
			Pos:          classStart,
			ModuleStart:  moduleStartForPos(moduleStarts, classStart),
		}
		messages = append(messages, msg)
	}

	// Pattern 2: transpiled/minified bundle style
	// Example:
	// i.runtime=n.proto3,i.typeName="agent.v1.McpArgs",i.fields=n.proto3.util.newFieldList(()=>[{...}]);
	assignmentRe := regexp.MustCompile(`([\w$]+)\.typeName\s*=\s*"([\w.]+)"\s*,\s*[\w$]+\.fields\s*=\s*\w+(?:\.\w+)*\.util\.newFieldList\s*\(\s*\(\s*\)\s*=>\s*\[`)
	assignmentMatches := assignmentRe.FindAllStringSubmatchIndex(text, -1)
	for _, m := range assignmentMatches {
		varName := text[m[2]:m[3]]
		typeName := text[m[4]:m[5]]

		// Skip duplicates already captured by class-body style
		alreadyExists := false
		for _, existing := range messages {
			if existing.TypeName == typeName && existing.VarName == varName {
				alreadyExists = true
				break
			}
		}
		if alreadyExists {
			continue
		}

		// Locate array start from the regex end (which stops right before '[')
		start := m[1] - 1
		if start < 0 || start >= len(text) || text[start] != '[' {
			continue
		}
		fields := extractFieldArray(text, start)

		pkg, shortName := parseTypeName(typeName)
		messages = append(messages, Message{
			TypeName:     typeName,
			VarName:      varName,
			InternalName: "",
			Fields:       fields,
			Package:      pkg,
			ShortName:    shortName,
			Pos:          m[0],
			ModuleStart:  moduleStartForPos(moduleStarts, m[0]),
		})
	}

	return messages
}

// findClassEnd finds the matching closing brace for a class definition
func findClassEnd(text string, openBrace int) int {
	depth := 0
	for i := openBrace; i < len(text); i++ {
		if text[i] == '{' {
			depth++
		} else if text[i] == '}' {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func extractFieldArray(text string, start int) []Field {
	// Find matching bracket
	depth := 0
	end := start
	for i := start; i < len(text); i++ {
		if text[i] == '[' {
			depth++
		} else if text[i] == ']' {
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
	}

	arrayText := text[start:end]

	// Parse individual field objects by extracting each {...} block
	var fields []Field

	// Find each field object
	fieldObjects := extractFieldObjects(arrayText)

	for _, fieldObj := range fieldObjects {
		field, parseErr := parseFieldObject(fieldObj)
		if parseErr != nil {
			activeDiagnostics.addSkippedField(fieldObj, parseErr)
			continue
		}
		activeDiagnostics.addParsedField()
		fields = append(fields, *field)
	}

	return fields
}

// extractFieldObjects extracts individual {...} objects from array text
func extractFieldObjects(arrayText string) []string {
	var objects []string
	depth := 0
	start := -1

	for i := 0; i < len(arrayText); i++ {
		if arrayText[i] == '{' {
			if depth == 0 {
				start = i
			}
			depth++
		} else if arrayText[i] == '}' {
			depth--
			if depth == 0 && start >= 0 {
				objects = append(objects, arrayText[start:i+1])
				start = -1
			}
		}
	}

	return objects
}

// parseFieldObject parses a single field object like { no: 1, name: "foo", kind: "scalar", T: 9, opt: !0 }
func parseFieldObject(obj string) (*Field, error) {
	// Extract no
	noMatch := noRe.FindStringSubmatch(obj)
	if noMatch == nil {
		return nil, errors.New("missing field no")
	}
	no, _ := strconv.Atoi(noMatch[1])

	// Extract name
	nameMatch := nameRe.FindStringSubmatch(obj)
	if nameMatch == nil {
		return nil, errors.New("missing field name")
	}
	name := strings.TrimSpace(nameMatch[1])
	if !fieldNameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid field name: %s", name)
	}

	// Extract kind
	kindMatch := kindRe.FindStringSubmatch(obj)
	if kindMatch == nil {
		return nil, errors.New("missing field kind")
	}
	kind := strings.TrimSpace(kindMatch[1])

	field := &Field{
		No:   no,
		Name: name,
		Kind: kind,
	}

	// Extract T (type) - can be:
	// 1. number (scalar): T: 9
	// 2. variable name: T: SPe
	// 3. getEnumType call: T: n.getEnumType(SPe) or T: n.proto3.getEnumType(SPe)

	// Try getEnumType pattern first (for enums)
	if enumMatch := enumTypeRe.FindStringSubmatch(obj); enumMatch != nil {
		field.T = enumMatch[1]
	} else {
		// Try simple T: value pattern
		if tMatch := tRe.FindStringSubmatch(obj); tMatch != nil {
			if t, err := strconv.Atoi(tMatch[1]); err == nil {
				field.T = t
			} else {
				field.T = tMatch[1]
			}
		}
	}

	// Check for oneof (within THIS object only)
	if oneofMatch := oneofRe.FindStringSubmatch(obj); oneofMatch != nil {
		candidate := strings.TrimSpace(oneofMatch[1])
		if oneofNameRe.MatchString(candidate) {
			field.Oneof = candidate
		}
	}

	// Check for repeated (within THIS object only)
	// !0 means true in minified JS
	if repeatedRe.MatchString(obj) {
		field.Repeated = true
	}

	// Check for optional (within THIS object only)
	if optRe.MatchString(obj) {
		field.Opt = true
	}

	// Check for map type: K: keyType, V: { kind: "scalar"|"message", T: valueType }
	if field.Kind == "map" {
		// Extract K (key type)
		if keyMatch := keyRe.FindStringSubmatch(obj); keyMatch != nil {
			field.MapKey, _ = strconv.Atoi(keyMatch[1])
		}

		// Extract V (value type) - property order can vary.
		if valueMatch := mapValueRe.FindStringSubmatch(obj); valueMatch != nil {
			valueObj := valueMatch[1]
			if kindMatch := mapValueKRe.FindStringSubmatch(valueObj); kindMatch != nil {
				field.MapValueKind = kindMatch[1]
			}
			if tMatch := mapValueTRe.FindStringSubmatch(valueObj); tMatch != nil {
				if t, err := strconv.Atoi(tMatch[1]); err == nil {
					field.MapValueT = t
				} else {
					field.MapValueT = tMatch[1]
				}
			}
		}
	}

	return field, nil
}

func extractEnums(text string, moduleStarts []int) []Enum {
	var enums []Enum

	// Pattern for enum: setEnumType(XXX, "xxx.v1.EnumName", [...]) (any package)
	// JS 变量名可以包含 $ 符号
	enumRe := regexp.MustCompile(`setEnumType\s*\(\s*([\w$]+)\s*,\s*"([\w.]+)"\s*,\s*\[`)

	matches := enumRe.FindAllStringSubmatchIndex(text, -1)
	for _, match := range matches {
		varName := text[match[2]:match[3]]
		typeName := text[match[4]:match[5]]

		// Extract enum values array
		bracketStart := match[1] - 1
		values := extractEnumValues(text, bracketStart)

		pkg, shortName := parseTypeName(typeName)
		enum := Enum{
			TypeName:    typeName,
			VarName:     varName,
			Values:      values,
			Package:     pkg,
			ShortName:   shortName,
			Pos:         match[0],
			ModuleStart: moduleStartForPos(moduleStarts, match[0]),
		}
		enums = append(enums, enum)
	}

	return enums
}

func extractServices(text string, moduleStarts []int) []Service {
	var services []Service

	// Pattern: VarName = { typeName: "xxx.v1.ServiceName", methods: { ... } }
	// Service definitions are object literals, not classes
	serviceRe := regexp.MustCompile(`([\w$]+)\s*=\s*\{\s*typeName:\s*"([\w.]+)"\s*,\s*methods:\s*\{`)

	matches := serviceRe.FindAllStringSubmatchIndex(text, -1)
	for _, match := range matches {
		varName := text[match[2]:match[3]]
		typeName := text[match[4]:match[5]]

		// Find the end of the methods object
		methodsStart := match[1] - 1 // position of '{'
		methodsEnd := findMatchingBrace(text, methodsStart)
		if methodsEnd == -1 {
			continue
		}

		methodsText := text[methodsStart:methodsEnd]
		methods := extractMethods(methodsText)

		pkg, shortName := parseTypeName(typeName)
		service := Service{
			TypeName:    typeName,
			VarName:     varName,
			Methods:     methods,
			Package:     pkg,
			ShortName:   shortName,
			Pos:         match[0],
			ModuleStart: moduleStartForPos(moduleStarts, match[0]),
		}
		services = append(services, service)
	}

	return services
}

func extractMethods(methodsText string) []Method {
	var methods []Method

	// Pattern: methodName: { name: "MethodName", I: n.Input, O: n.Output, kind: s.MethodKind.Unary }
	methodRe := regexp.MustCompile(`\w+:\s*\{\s*name:\s*"([^"]+)"\s*,\s*I:\s*([\w$.]+)\s*,\s*O:\s*([\w$.]+)\s*,\s*kind:\s*[\w$.]+\.(Unary|ServerStreaming|ClientStreaming|BiDiStreaming)`)

	matches := methodRe.FindAllStringSubmatch(methodsText, -1)
	for _, m := range matches {
		method := Method{
			Name:       m[1],
			InputType:  m[2],
			OutputType: m[3],
			Kind:       m[4],
		}
		methods = append(methods, method)
	}

	return methods
}

func findMatchingBrace(text string, start int) int {
	depth := 0
	for i := start; i < len(text); i++ {
		if text[i] == '{' {
			depth++
		} else if text[i] == '}' {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func extractEnumValues(text string, start int) []EnumValue {
	// Find matching bracket
	depth := 0
	end := start
	for i := start; i < len(text); i++ {
		if text[i] == '[' {
			depth++
		} else if text[i] == ']' {
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
	}

	arrayText := text[start:end]

	var values []EnumValue
	valueRe := regexp.MustCompile(`\{\s*no:\s*(\d+)\s*,\s*name:\s*"([^"]+)"`)

	matches := valueRe.FindAllStringSubmatch(arrayText, -1)
	for _, m := range matches {
		no, _ := strconv.Atoi(m[1])
		values = append(values, EnumValue{No: no, Name: m[2]})
	}

	return values
}

func generateProtos(messages []Message, enums []Enum, services []Service, resolver *TypeResolver, outputDir string) {
	os.MkdirAll(outputDir, 0755)

	// Group by package
	packages := make(map[string]struct {
		messages []Message
		enums    []Enum
		services []Service
	})

	for _, msg := range messages {
		pkg := packages[msg.Package]
		pkg.messages = append(pkg.messages, msg)
		packages[msg.Package] = pkg
	}

	for _, enum := range enums {
		pkg := packages[enum.Package]
		pkg.enums = append(pkg.enums, enum)
		packages[enum.Package] = pkg
	}

	for _, svc := range services {
		pkg := packages[svc.Package]
		pkg.services = append(pkg.services, svc)
		packages[svc.Package] = pkg
	}

	// Build global type maps for copying
	allMessages := make(map[string]*Message)
	allEnums := make(map[string]*Enum)

	for pkgName, pkg := range packages {
		if isGooglePkg(pkgName) {
			continue
		}
		for i := range pkg.messages {
			msg := &pkg.messages[i]
			allMessages[msg.TypeName] = msg
		}
		for i := range pkg.enums {
			enum := &pkg.enums[i]
			allEnums[enum.TypeName] = enum
		}
	}

	// Reset copiedTypes tracking
	copiedTypes = make(map[string]map[string]string)

	for pkgName, pkg := range packages {
		// Skip Google standard packages - use official proto files instead
		if isGooglePkg(pkgName) {
			fmt.Printf("跳过: %s (使用官方 proto 文件)\n", pkgName)
			continue
		}

		// Copy all external types referenced by this package
		augmentedPkg := copyAllExternalTypes(pkgName, pkg, resolver, allMessages, allEnums)
		generateProtoFile(pkgName, augmentedPkg.messages, augmentedPkg.enums, pkg.services, resolver, outputDir)
	}
}

// copyAllExternalTypes copies all externally referenced types into the current package
func copyAllExternalTypes(pkgName string, pkg struct {
	messages []Message
	enums    []Enum
	services []Service
}, resolver *TypeResolver, allMessages map[string]*Message, allEnums map[string]*Enum) struct {
	messages []Message
	enums    []Enum
	services []Service
} {
	if copiedTypes[pkgName] == nil {
		copiedTypes[pkgName] = make(map[string]string)
	}

	// Build set of types already in this package
	// Also record them in copiedTypes so resolveFieldTypeWithPkg can use local names
	localTypes := make(map[string]bool)
	for _, msg := range pkg.messages {
		localTypes[msg.ShortName] = true
		// Mark as "local" - empty string means original type in this package
		if copiedTypes[pkgName][msg.ShortName] == "" {
			copiedTypes[pkgName][msg.ShortName] = "local:" + msg.TypeName
		}
	}
	for _, enum := range pkg.enums {
		localTypes[enum.ShortName] = true
		if copiedTypes[pkgName][enum.ShortName] == "" {
			copiedTypes[pkgName][enum.ShortName] = "local:" + enum.TypeName
		}
	}

	// Result starts with original types
	result := struct {
		messages []Message
		enums    []Enum
		services []Service
	}{
		messages: append([]Message{}, pkg.messages...),
		enums:    append([]Enum{}, pkg.enums...),
		services: pkg.services,
	}

	totalCopied := 0

	// Iterate until no new types need to be copied
	for round := 1; ; round++ {
		// Collect all external type references from current messages
		neededTypes := make(map[string]bool)

		for _, msg := range result.messages {
			for _, f := range msg.Fields {
				collectFieldRefsSimple(f, pkgName, msg.Pos, msg.ModuleStart, resolver, neededTypes, localTypes)
			}
		}
		for _, svc := range result.services {
			for _, m := range svc.Methods {
				collectMethodRefsSimple(m.InputType, pkgName, svc.Pos, svc.ModuleStart, resolver, neededTypes, localTypes)
				collectMethodRefsSimple(m.OutputType, pkgName, svc.Pos, svc.ModuleStart, resolver, neededTypes, localTypes)
			}
		}

		// Copy needed types
		copiedThisRound := 0
		for typeName := range neededTypes {
			refPkg, shortName := parseTypeName(typeName)
			if refPkg == pkgName || isGooglePkg(refPkg) {
				continue
			}

			// Check if already local
			if localTypes[shortName] {
				continue
			}

			// Copy message
			if msg, ok := allMessages[typeName]; ok {
				msgCopy := *msg
				msgCopy.Package = pkgName
				// Keep original TypeName for source reference in comments
				// msgCopy.TypeName will be used for reference, store original separately
				result.messages = append(result.messages, msgCopy)
				copiedTypes[pkgName][shortName] = typeName // original full type name
				localTypes[shortName] = true
				copiedThisRound++
				fmt.Printf("  [%s] 轮%d 复制: %s\n", pkgName, round, typeName)
			} else if enum, ok := allEnums[typeName]; ok {
				// Copy enum
				enumCopy := *enum
				enumCopy.Package = pkgName
				result.enums = append(result.enums, enumCopy)
				copiedTypes[pkgName][shortName] = typeName
				localTypes[shortName] = true
				copiedThisRound++
				fmt.Printf("  [%s] 轮%d 复制枚举: %s\n", pkgName, round, typeName)
			} else {
				// Type not found - add to copiedTypes anyway to use local reference
				// This handles cases where the type exists locally but wasn't in our extraction
				copiedTypes[pkgName][shortName] = typeName
				localTypes[shortName] = true
				fmt.Printf("  [%s] 轮%d 警告: 类型未找到 %s，标记为本地引用\n", pkgName, round, typeName)
			}
		}

		totalCopied += copiedThisRound

		if copiedThisRound == 0 {
			break // No more types to copy
		}

		if round > 20 {
			fmt.Printf("  [%s] 警告: 复制轮次超过20，可能存在问题\n", pkgName)
			break
		}
	}

	if totalCopied > 0 {
		fmt.Printf("  [%s] 共复制 %d 个外部类型\n", pkgName, totalCopied)
	}

	return result
}

// collectFieldRefsSimple collects external type references from a field (non-recursive, just this field)
func collectFieldRefsSimple(f Field, currentPkg string, contextPos int, contextModuleStart int, resolver *TypeResolver,
	neededTypes map[string]bool, localTypes map[string]bool) {

	type refWithKind struct {
		ref  string
		kind string
	}

	var refs []refWithKind
	if f.Kind == "message" || f.Kind == "enum" {
		if v, ok := f.T.(string); ok {
			refs = append(refs, refWithKind{ref: v, kind: f.Kind})
		}
	}
	if f.Kind == "map" && (f.MapValueKind == "message" || f.MapValueKind == "enum") {
		if v, ok := f.MapValueT.(string); ok {
			refs = append(refs, refWithKind{ref: v, kind: f.MapValueKind})
		}
	}

	for _, item := range refs {
		typeName, ok := resolver.ResolveTypeName(item.ref, contextPos, contextModuleStart, currentPkg, item.kind)
		if !ok {
			continue
		}

		refPkg, shortName := parseTypeName(typeName)
		if refPkg == "" || refPkg == currentPkg || isGooglePkg(refPkg) {
			continue
		}

		// Skip if already local
		if localTypes[shortName] {
			continue
		}

		neededTypes[typeName] = true
	}
}

// collectMethodRefsSimple collects external type references from a method type
func collectMethodRefsSimple(ref string, currentPkg string, contextPos int, contextModuleStart int, resolver *TypeResolver,
	neededTypes map[string]bool, localTypes map[string]bool) {

	typeName, ok := resolver.ResolveTypeName(ref, contextPos, contextModuleStart, currentPkg, "message")
	if !ok {
		return
	}

	refPkg, shortName := parseTypeName(typeName)
	if refPkg == "" || refPkg == currentPkg || isGooglePkg(refPkg) {
		return
	}

	if localTypes[shortName] {
		return
	}

	neededTypes[typeName] = true
}

// Global map to track copied types: targetPkg -> shortName -> original typeName
var copiedTypes = make(map[string]map[string]string)

// TypeNode represents a node in the nested type tree
type TypeNode struct {
	Name     string
	Message  *Message
	Enum     *Enum
	Children map[string]*TypeNode
}

// collectImports collects only Google standard imports (all other types are copied locally)
func collectImports(currentPkg string, messages []Message, services []Service, resolver *TypeResolver) map[string]bool {
	imports := make(map[string]bool)

	addImport := func(ref string, contextPos int, contextModuleStart int, expectedKind string) {
		typeName, ok := resolver.ResolveTypeName(ref, contextPos, contextModuleStart, currentPkg, expectedKind)
		if !ok {
			return
		}

		refPkg, shortName := parseTypeName(typeName)
		// Only import Google standard types - all others are copied locally
		if refPkg == "google.protobuf" {
			var importFile string
			switch shortName {
			case "Struct", "Value", "ListValue", "NullValue":
				importFile = "google/protobuf/struct.proto"
			case "Timestamp":
				importFile = "google/protobuf/timestamp.proto"
			case "Duration":
				importFile = "google/protobuf/duration.proto"
			case "Any":
				importFile = "google/protobuf/any.proto"
			case "Empty":
				importFile = "google/protobuf/empty.proto"
			case "FieldMask":
				importFile = "google/protobuf/field_mask.proto"
			case "BoolValue", "BytesValue", "DoubleValue", "FloatValue",
				"Int32Value", "Int64Value", "StringValue", "UInt32Value", "UInt64Value":
				importFile = "google/protobuf/wrappers.proto"
			default:
				importFile = "google/protobuf/descriptor.proto"
			}
			imports[importFile] = true
		} else if refPkg == "google.rpc" {
			var importFile string
			switch shortName {
			case "Status":
				importFile = "google/rpc/status.proto"
			case "Code":
				importFile = "google/rpc/code.proto"
			default:
				importFile = "google/rpc/status.proto"
			}
			imports[importFile] = true
		}
	}

	for _, msg := range messages {
		for _, f := range msg.Fields {
			if f.Kind == "message" || f.Kind == "enum" {
				if ref, ok := f.T.(string); ok {
					addImport(ref, msg.Pos, msg.ModuleStart, f.Kind)
				}
			}
			// Also check map value types
			if f.Kind == "map" && (f.MapValueKind == "message" || f.MapValueKind == "enum") {
				if ref, ok := f.MapValueT.(string); ok {
					addImport(ref, msg.Pos, msg.ModuleStart, f.MapValueKind)
				}
			}
		}
	}

	for _, svc := range services {
		for _, m := range svc.Methods {
			addImport(m.InputType, svc.Pos, svc.ModuleStart, "message")
			addImport(m.OutputType, svc.Pos, svc.ModuleStart, "message")
		}
	}

	return imports
}

func generateProtoFile(pkgName string, messages []Message, enums []Enum, services []Service, resolver *TypeResolver, outputDir string) {
	// First, collect all cross-package imports
	imports := collectImports(pkgName, messages, services, resolver)

	var sb strings.Builder

	sb.WriteString(`syntax = "proto3";` + "\n\n")
	sb.WriteString(fmt.Sprintf("package %s;\n\n", pkgName))

	// Write imports
	if len(imports) > 0 {
		sortedImports := make([]string, 0, len(imports))
		for imp := range imports {
			sortedImports = append(sortedImports, imp)
		}
		sort.Strings(sortedImports)
		for _, imp := range sortedImports {
			sb.WriteString(fmt.Sprintf("import \"%s\";\n", imp))
		}
		sb.WriteString("\n")
	}

	goPackagePath := strings.ReplaceAll(pkgName, ".", "/")
	goPackageName := strings.ReplaceAll(pkgName, ".", "")
	sb.WriteString(fmt.Sprintf(`option go_package = "react-admin/cursor-server/gen/%s;%s";`+"\n\n", goPackagePath, goPackageName))

	// Build type tree
	root := &TypeNode{Children: make(map[string]*TypeNode)}

	for i := range messages {
		msg := &messages[i]
		path := getNestedPath(msg.ShortName)
		insertMessage(root, path, msg)
	}

	for i := range enums {
		enum := &enums[i]
		path := getNestedPath(enum.ShortName)
		insertEnum(root, path, enum)
	}

	// Write all top-level types
	writeTypeTree(root, &sb, resolver, 0, pkgName)

	// Write services
	sort.Slice(services, func(i, j int) bool {
		return services[i].ShortName < services[j].ShortName
	})

	for _, svc := range services {
		// Write source comment for service
		sb.WriteString(fmt.Sprintf("// Source: %s (var: %s)\n", svc.TypeName, svc.VarName))
		sb.WriteString(fmt.Sprintf("service %s {\n", svc.ShortName))
		for _, m := range svc.Methods {
			inputType := resolveMethodType(m.InputType, resolver, pkgName, svc.Pos, svc.ModuleStart)
			outputType := resolveMethodType(m.OutputType, resolver, pkgName, svc.Pos, svc.ModuleStart)

			switch m.Kind {
			case "ServerStreaming":
				sb.WriteString(fmt.Sprintf("  rpc %s(%s) returns (stream %s) {}\n", m.Name, inputType, outputType))
			case "ClientStreaming":
				sb.WriteString(fmt.Sprintf("  rpc %s(stream %s) returns (%s) {}\n", m.Name, inputType, outputType))
			case "BiDiStreaming":
				sb.WriteString(fmt.Sprintf("  rpc %s(stream %s) returns (stream %s) {}\n", m.Name, inputType, outputType))
			default: // Unary
				sb.WriteString(fmt.Sprintf("  rpc %s(%s) returns (%s) {}\n", m.Name, inputType, outputType))
			}
		}
		sb.WriteString("}\n\n")
	}

	// Write to file - single flat directory
	fileName := strings.ReplaceAll(pkgName, ".", "_") + ".proto"
	filePath := filepath.Join(outputDir, fileName)

	os.WriteFile(filePath, []byte(sb.String()), 0644)
	fmt.Printf("Generated: %s (%d messages, %d enums, %d services)\n", filePath, len(messages), len(enums), len(services))
}

func resolveMethodType(ref string, resolver *TypeResolver, currentPkg string, contextPos int, contextModuleStart int) string {
	typeName, ok := resolver.ResolveTypeName(ref, contextPos, contextModuleStart, currentPkg, "message")
	if !ok {
		activeDiagnostics.addUnresolvedType("method:" + ref)
		return fallbackTypeToken(ref)
	}

	refPkg, shortName := parseTypeName(typeName)
	if refPkg == currentPkg || refPkg == "" {
		return shortName
	}
	// Check if this type was copied to current package
	if copied := copiedTypes[currentPkg]; copied != nil {
		if _, isCopied := copied[shortName]; isCopied {
			return shortName
		}
	}
	return refPkg + "." + shortName
}

func insertMessage(node *TypeNode, path []string, msg *Message) {
	if len(path) == 0 {
		return
	}

	name := path[0]
	if node.Children == nil {
		node.Children = make(map[string]*TypeNode)
	}

	child, exists := node.Children[name]
	if !exists {
		child = &TypeNode{Name: name, Children: make(map[string]*TypeNode)}
		node.Children[name] = child
	}

	if len(path) == 1 {
		child.Message = msg
	} else {
		insertMessage(child, path[1:], msg)
	}
}

func insertEnum(node *TypeNode, path []string, enum *Enum) {
	if len(path) == 0 {
		return
	}

	name := path[0]
	if node.Children == nil {
		node.Children = make(map[string]*TypeNode)
	}

	child, exists := node.Children[name]
	if !exists {
		child = &TypeNode{Name: name, Children: make(map[string]*TypeNode)}
		node.Children[name] = child
	}

	if len(path) == 1 {
		child.Enum = enum
	} else {
		insertEnum(child, path[1:], enum)
	}
}

func writeTypeTree(node *TypeNode, sb *strings.Builder, resolver *TypeResolver, indent int, currentPkg string) {
	// Get sorted child names
	var names []string
	for name := range node.Children {
		names = append(names, name)
	}
	sort.Strings(names)

	indentStr := strings.Repeat("  ", indent)

	for _, name := range names {
		child := node.Children[name]

		if child.Enum != nil {
			// Check if this is a copied type
			originalType := ""
			if copied := copiedTypes[currentPkg]; copied != nil {
				if orig, ok := copied[child.Enum.ShortName]; ok {
					originalType = orig
				}
			}

			// Write source comment for enum
			if originalType != "" {
				sb.WriteString(fmt.Sprintf("%s// Copied from: %s (var: %s)\n", indentStr, originalType, child.Enum.VarName))
			} else {
				sb.WriteString(fmt.Sprintf("%s// Source: %s (var: %s)\n", indentStr, child.Enum.TypeName, child.Enum.VarName))
			}
			// Write enum
			sb.WriteString(fmt.Sprintf("%senum %s {\n", indentStr, name))
			for _, v := range child.Enum.Values {
				sb.WriteString(fmt.Sprintf("%s  %s = %d;\n", indentStr, v.Name, v.No))
			}
			sb.WriteString(fmt.Sprintf("%s}\n\n", indentStr))
		} else if child.Message != nil || len(child.Children) > 0 {
			// Write source comment for message
			if child.Message != nil {
				varInfo := child.Message.VarName
				if child.Message.InternalName != "" && child.Message.InternalName != child.Message.VarName {
					varInfo = fmt.Sprintf("%s, class: %s", child.Message.VarName, child.Message.InternalName)
				}

				// Check if this is a copied type
				originalType := ""
				if copied := copiedTypes[currentPkg]; copied != nil {
					if orig, ok := copied[child.Message.ShortName]; ok {
						originalType = orig
					}
				}

				if originalType != "" {
					sb.WriteString(fmt.Sprintf("%s// Copied from: %s (var: %s)\n", indentStr, originalType, varInfo))
				} else {
					sb.WriteString(fmt.Sprintf("%s// Source: %s (var: %s)\n", indentStr, child.Message.TypeName, varInfo))
				}
			}
			// Write message (even if just a container for nested types)
			sb.WriteString(fmt.Sprintf("%smessage %s {\n", indentStr, name))

			// Write nested types first
			writeTypeTree(child, sb, resolver, indent+1, currentPkg)

			// Write fields if this node has a message
			if child.Message != nil {
				writeMessageFields(child.Message, sb, resolver, indent+1)
			}

			sb.WriteString(fmt.Sprintf("%s}\n\n", indentStr))
		}
	}
}

func writeMessageFields(msg *Message, sb *strings.Builder, resolver *TypeResolver, indent int) {
	indentStr := strings.Repeat("  ", indent)

	// Get the current message's path prefix for relative type resolution
	msgPath := msg.ShortName
	currentPkg := msg.Package

	// Group fields by oneof
	oneofGroups := make(map[string][]Field)
	var regularFields []Field

	for _, f := range msg.Fields {
		if f.Oneof != "" {
			oneofGroups[f.Oneof] = append(oneofGroups[f.Oneof], f)
		} else {
			regularFields = append(regularFields, f)
		}
	}

	// Write regular fields
	for _, f := range regularFields {
		fieldType := resolveFieldTypeWithPkg(f, resolver, msgPath, currentPkg, msg.Pos, msg.ModuleStart)
		prefix := ""
		if f.Repeated {
			prefix = "repeated "
		} else if f.Opt {
			prefix = "optional "
		}
		sb.WriteString(fmt.Sprintf("%s%s%s %s = %d;\n", indentStr, prefix, fieldType, f.Name, f.No))
	}

	// Write oneof groups
	var oneofNames []string
	for name := range oneofGroups {
		oneofNames = append(oneofNames, name)
	}
	sort.Strings(oneofNames)

	for _, oneofName := range oneofNames {
		fields := oneofGroups[oneofName]
		sb.WriteString(fmt.Sprintf("%soneof %s {\n", indentStr, oneofName))
		for _, f := range fields {
			fieldType := resolveFieldTypeWithPkg(f, resolver, msgPath, currentPkg, msg.Pos, msg.ModuleStart)
			sb.WriteString(fmt.Sprintf("%s  %s %s = %d;\n", indentStr, fieldType, f.Name, f.No))
		}
		sb.WriteString(fmt.Sprintf("%s}\n", indentStr))
	}
}

// parseTypeName extracts package and full nested path from type name
// "agent.v1.Foo" -> ("agent.v1", "Foo")
// "agent.v1.Foo.Bar" -> ("agent.v1", "Foo.Bar")
// "anyrun.v1.PodStatus" -> ("anyrun.v1", "PodStatus")
// "google.protobuf.Timestamp" -> ("google.protobuf", "Timestamp")
func parseTypeName(typeName string) (pkg, shortName string) {
	// Find pattern: xxx.v1.Rest or xxx.vN.Rest
	versionRe := regexp.MustCompile(`^([\w.]+\.v\d+)\.(.+)$`)
	if match := versionRe.FindStringSubmatch(typeName); match != nil {
		return match[1], match[2]
	}

	// Handle google.protobuf.XXX pattern
	if strings.HasPrefix(typeName, "google.protobuf.") {
		rest := strings.TrimPrefix(typeName, "google.protobuf.")
		return "google.protobuf", rest
	}

	// Handle google.rpc.XXX pattern
	if strings.HasPrefix(typeName, "google.rpc.") {
		rest := strings.TrimPrefix(typeName, "google.rpc.")
		return "google.rpc", rest
	}

	// Fallback: split at last dot
	parts := strings.Split(typeName, ".")
	if len(parts) > 1 {
		return strings.Join(parts[:len(parts)-1], "."), parts[len(parts)-1]
	}
	return "", typeName
}

// getNestedPath returns the path components for a nested type
// "Foo" -> ["Foo"]
// "Foo.Bar" -> ["Foo", "Bar"]
// "Foo.Bar.Baz" -> ["Foo", "Bar", "Baz"]
func getNestedPath(shortName string) []string {
	return strings.Split(shortName, ".")
}

func resolveFieldType(f Field, resolver *TypeResolver, contextPos int, contextModuleStart int) string {
	return resolveFieldTypeWithPkg(f, resolver, "", "", contextPos, contextModuleStart)
}

// resolveFieldTypeWithPkg resolves field type with package awareness
// parentPath is like "ConversationMessage" or "ConversationMessage.ToolResult"
// currentPkg is the package of the current message being written (e.g., "agent.v1")
func resolveFieldTypeWithPkg(f Field, resolver *TypeResolver, parentPath string, currentPkg string, contextPos int, contextModuleStart int) string {
	resolveNamedType := func(ref string, expectedKind string) string {
		typeName, ok := resolver.ResolveTypeName(ref, contextPos, contextModuleStart, currentPkg, expectedKind)
		if !ok {
			activeDiagnostics.addUnresolvedType(expectedKind + ":" + ref)
			return fallbackTypeToken(ref)
		}

		refPkg, shortName := parseTypeName(typeName)

		// If the type is nested under the same parent, use relative path
		if parentPath != "" && strings.HasPrefix(shortName, parentPath+".") {
			// ConversationMessage.CodeChunk -> CodeChunk (when inside ConversationMessage)
			return strings.TrimPrefix(shortName, parentPath+".")
		}

		// If same package, use short name only
		if refPkg == currentPkg || refPkg == "" {
			return shortName
		}

		// Check if this type was copied to current package (circular import resolution)
		if copied := copiedTypes[currentPkg]; copied != nil {
			if _, isCopied := copied[shortName]; isCopied {
				// This type exists locally as a copy, use short name
				return shortName
			}
		}

		// For cross-package references, use full type name
		return refPkg + "." + shortName
	}

	if f.Kind == "scalar" {
		if t, ok := f.T.(int); ok {
			return scalarTypes[t]
		}
		if t, ok := f.T.(float64); ok {
			return scalarTypes[int(t)]
		}
	}

	if f.Kind == "message" || f.Kind == "enum" {
		if ref, ok := f.T.(string); ok {
			return resolveNamedType(ref, f.Kind)
		}
	}

	if f.Kind == "map" {
		// Handle map types: map<KeyType, ValueType>
		keyType := scalarTypes[f.MapKey]
		if keyType == "" {
			keyType = "string" // default
		}

		var valueType string
		if f.MapValueKind == "scalar" {
			if t, ok := f.MapValueT.(int); ok {
				valueType = scalarTypes[t]
			} else if t, ok := f.MapValueT.(float64); ok {
				valueType = scalarTypes[int(t)]
			}
		} else if f.MapValueKind == "message" || f.MapValueKind == "enum" {
			if ref, ok := f.MapValueT.(string); ok {
				valueType = resolveNamedType(ref, f.MapValueKind)
			}
		}
		if valueType == "" {
			valueType = "bytes"
		}

		return fmt.Sprintf("map<%s, %s>", keyType, valueType)
	}

	return "bytes" // fallback
}
