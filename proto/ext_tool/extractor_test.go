package main

import "testing"

func TestParseFieldObjectSupportsShorthandType(t *testing.T) {
	field, err := parseFieldObject(`{no:4,name:"file_not_found",kind:"message",T,oneof:"result"}`)
	if err != nil {
		t.Fatalf("parse shorthand T: %v", err)
	}
	if field.T != "T" {
		t.Fatalf("parsed shorthand T as %#v, want T", field.T)
	}
}

func TestWebpackExportAliasResolvesServiceMessageType(t *testing.T) {
	const bundle = `
1:(e,t,n)=>{
  n.d(t,{KS:()=>T,_B:()=>r});
  var r;
  class T {}
  T.typeName="agent.v1.AgentClientMessage";
  n.proto3.util.setEnumType(r,"agent.v1.DiagnosticSeverity",[]);
},
2:(e,t,n)=>{
  var r=n(1);
  const service={typeName:"agent.v1.AgentService",methods:{run:{name:"Run",I:r.KS,O:r.KS,kind:n.MethodKind.BiDiStreaming}}};
}`

	moduleStarts := buildModuleStarts(bundle)
	messages := []Message{{
		TypeName:     "agent.v1.AgentClientMessage",
		VarName:      "T",
		InternalName: "T",
		Package:      "agent.v1",
		Pos:          35,
		ModuleStart:  moduleStartForPos(moduleStarts, 35),
	}}
	enums := []Enum{{
		TypeName:    "agent.v1.DiagnosticSeverity",
		VarName:     "r",
		Package:     "agent.v1",
		Pos:         100,
		ModuleStart: moduleStartForPos(moduleStarts, 100),
	}}

	resolver := newTypeResolver(messages, enums, buildAliasIndex(bundle, moduleStarts), buildWebpackExportAliasIndex(bundle, moduleStarts))
	resolver.moduleImports = buildModuleImportIndex(bundle, moduleStarts)
	typeName, ok := resolver.ResolveTypeName("r.KS", len(bundle)-1, moduleStartForPos(moduleStarts, len(bundle)-1), "agent.v1", "message")
	if !ok {
		t.Fatal("expected webpack export alias to resolve")
	}
	if typeName != "agent.v1.AgentClientMessage" {
		t.Fatalf("resolved r.KS to %q, want agent.v1.AgentClientMessage", typeName)
	}
}

func TestResolverPrefersExpectedKindOverCurrentPackage(t *testing.T) {
	resolver := &TypeResolver{bySymbol: map[string][]symbolDef{
		"nt": {
			{TypeName: "git_forge.v1.GetTagResponse", Kind: "message", Pos: 10, ModuleStart: 1},
			{TypeName: "origin.v1.TeamGroupKind", Kind: "enum", Pos: 20, ModuleStart: 1},
		},
	}}

	typeName, ok := resolver.ResolveTypeName("nt", 30, 1, "origin.v1", "message")
	if !ok {
		t.Fatal("expected cross-package message type to resolve")
	}
	if typeName != "git_forge.v1.GetTagResponse" {
		t.Fatalf("resolved nt to %q, want git_forge.v1.GetTagResponse", typeName)
	}
}
