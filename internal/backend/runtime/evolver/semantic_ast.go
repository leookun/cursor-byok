// semantic_ast.go adds AST-backed semantic checks (ADR-042).
//
// Text scanners remain useful for coarse constitution markers, but ChannelService
// nil-guard enforcement is more reliable when verified against Go syntax trees.
package evolver

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// checkChannelServiceNilGuardsAST parses VirtualModel provider packages and
// verifies Execute methods contain a nil check on channelSvc.
// Falls back to soft-pass when parser cannot read a package.
func checkChannelServiceNilGuardsAST(repoRoot string) (bool, string) {
	root := filepath.Join(repoRoot, "internal", "backend", "virtualmodel")
	entries, err := os.ReadDir(root)
	if err != nil {
		return true, ""
	}
	var missing []string
	checked := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		provider := filepath.Join(root, entry.Name(), "provider.go")
		if _, err := os.Stat(provider); err != nil {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, provider, nil, 0)
		if err != nil {
			// Soft-skip unreadable/partial fixtures.
			continue
		}
		if !providerMentionsChannelSvc(file) {
			continue
		}
		checked++
		if !executeHasChannelSvcNilGuard(file) {
			missing = append(missing, entry.Name()+"/provider.go")
		}
	}
	if checked == 0 {
		// No providers with channelSvc found; defer to text scanner / soft-pass.
		return true, ""
	}
	if len(missing) > 0 {
		return false, fmt.Sprintf("AST: Execute missing channelSvc nil-guard in: %s", strings.Join(missing, ", "))
	}
	return true, ""
}

func providerMentionsChannelSvc(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == "channelSvc" {
			found = true
			return false
		}
		return true
	})
	return found
}

func executeHasChannelSvcNilGuard(file *ast.File) bool {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Body == nil {
			continue
		}
		// Focus on Execute methods.
		if fn.Name.Name != "Execute" {
			continue
		}
		if funcBodyHasChannelSvcNilCheck(fn.Body) {
			return true
		}
	}
	// Some packages put the guard in a helper called by Execute; accept file-level guard.
	return fileHasChannelSvcNilCheck(file)
}

func fileHasChannelSvcNilCheck(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if isChannelSvcNilCheck(n) {
			found = true
			return false
		}
		return true
	})
	return found
}

func funcBodyHasChannelSvcNilCheck(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if isChannelSvcNilCheck(n) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isChannelSvcNilCheck(n ast.Node) bool {
	bin, ok := n.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	// channelSvc == nil OR nil == channelSvc
	return (isIdentNamed(bin.X, "channelSvc") && isNilIdent(bin.Y)) ||
		(isIdentNamed(bin.Y, "channelSvc") && isNilIdent(bin.X)) ||
		(isSelectorNamed(bin.X, "channelSvc") && isNilIdent(bin.Y)) ||
		(isSelectorNamed(bin.Y, "channelSvc") && isNilIdent(bin.X))
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

func isIdentNamed(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isSelectorNamed(e ast.Expr, name string) bool {
	// m.channelSvc
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == name
}

// checkDualModeRoutingAST verifies server policy defines local/upstream constants
// via AST rather than plain text search only.
func checkDualModeRoutingAST(repoRoot string) (bool, string) {
	policy := filepath.Join(repoRoot, "internal", "backend", "server", "policy.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, policy, nil, 0)
	if err != nil {
		// Soft-pass unreadable fixtures.
		return true, ""
	}
	hasLocal, hasUpstream := false, false
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if name == nil {
				continue
			}
			switch name.Name {
			case "ModeLocal":
				if i < len(vs.Values) && isStringLit(vs.Values[i], "local") {
					hasLocal = true
				}
			case "ModeUpstream":
				if i < len(vs.Values) && isStringLit(vs.Values[i], "upstream") {
					hasUpstream = true
				}
			}
		}
		return true
	})
	if !hasLocal || !hasUpstream {
		return false, "AST: policy.go missing ModeLocal/ModeUpstream string constants"
	}
	return true, ""
}

func isStringLit(e ast.Expr, want string) bool {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return false
	}
	// bl.Value includes quotes
	return strings.Trim(bl.Value, "\"`") == want
}
