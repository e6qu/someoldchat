// Command rebaseaudit reports declarations that a branch changed but that did
// not survive into a target revision.
//
// Rebasing a stacked branch onto a moved base is a three-way problem, and the
// failure mode that matters is silent: a conflict resolved by keeping the base
// side discards the branch's change, and the result still compiles and still
// passes its tests, because the tests that would have caught it usually travel
// in the same discarded hunk. Reviewing the merged diff does not surface it
// either, since the diff against the new base looks self-consistent.
//
// The audit compares three revisions:
//
//	parent  the revision the branch was actually written against
//	branch  the branch tip as authored, before any rebase
//	target  the revision that is supposed to contain the branch's work
//
// A declaration the branch added must exist in target. A declaration the branch
// modified must not still hold the parent's body in target. Anything else is
// reported.
//
// Choosing parent correctly is the whole game. For a branch stacked on another
// branch, parent is that branch's tip, not the merge base with the trunk: a
// merge base predates the ancestor's own work, so every declaration the
// ancestor introduced looks new rather than modified and is skipped. Getting
// this wrong hides exactly the regressions the audit exists to find.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"sort"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "rebaseaudit: %v\n", err)
		os.Exit(1)
	}
}

type finding struct {
	File string
	Kind string
	Decl string
}

func run() error {
	parent := flag.String("parent", "", "revision the branch was written against (for a stacked branch, its parent branch tip)")
	branch := flag.String("branch", "", "branch tip as authored, before rebasing")
	target := flag.String("target", "HEAD", "revision that should contain the branch's work")
	flag.Parse()

	if strings.TrimSpace(*parent) == "" || strings.TrimSpace(*branch) == "" {
		return errors.New("-parent and -branch are required")
	}
	for _, revision := range []string{*parent, *branch, *target} {
		if err := verifyRevision(revision); err != nil {
			return err
		}
	}

	files, err := changedFiles(*parent, *branch)
	if err != nil {
		return err
	}
	findings := make([]finding, 0)
	for _, file := range files {
		fileFindings, err := auditFile(file, *parent, *branch, *target)
		if err != nil {
			return fmt.Errorf("audit %s: %w", file, err)
		}
		findings = append(findings, fileFindings...)
	}

	if len(findings) == 0 {
		fmt.Printf("rebaseaudit: %d file(s) examined, no lost declarations\n", len(files))
		return nil
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Decl < findings[j].Decl
	})
	for _, item := range findings {
		fmt.Printf("%s\t%s\t%s\n", item.Kind, item.File, item.Decl)
	}
	return fmt.Errorf("%d declaration(s) from %s are absent or stale in %s", len(findings), *branch, *target)
}

func verifyRevision(revision string) error {
	command := exec.Command("git", "rev-parse", "--verify", "--quiet", revision+"^{commit}")
	if err := command.Run(); err != nil {
		return fmt.Errorf("unknown revision %q", revision)
	}
	return nil
}

// changedFiles reports the Go files the branch touched. Generated protobuf
// output is excluded: it is reproduced from its .proto source, so auditing it
// reports churn rather than lost intent.
func changedFiles(parent, branch string) ([]string, error) {
	output, err := exec.Command("git", "diff", "--name-only", parent+".."+branch).Output()
	if err != nil {
		return nil, fmt.Errorf("diff %s..%s: %w", parent, branch, err)
	}
	files := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".pb.go") {
			continue
		}
		files = append(files, name)
	}
	return files, nil
}

// fileAt returns a file's contents at a revision. A file absent from that
// revision yields ok=false rather than an error, which is the normal case for a
// file the branch introduced.
func fileAt(revision, path string) (string, bool, error) {
	command := exec.Command("git", "show", revision+":"+path)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if strings.Contains(stderr.String(), "does not exist") || strings.Contains(stderr.String(), "exists on disk") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("show %s:%s: %s", revision, path, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), true, nil
}

func auditFile(path, parent, branch, target string) ([]finding, error) {
	branchSource, ok, err := fileAt(branch, path)
	if err != nil || !ok {
		return nil, err
	}
	branchDecls, err := declarations(path, branchSource)
	if err != nil {
		// A file that does not parse cannot be audited, but it also cannot be
		// the source of a silent loss: the build would have rejected it.
		return nil, nil
	}
	parentSource, _, err := fileAt(parent, path)
	if err != nil {
		return nil, err
	}
	parentDecls, err := declarations(path, parentSource)
	if err != nil {
		parentDecls = map[string]string{}
	}
	targetSource, present, err := fileAt(target, path)
	if err != nil {
		return nil, err
	}
	if !present {
		return []finding{{File: path, Kind: "file-missing", Decl: ""}}, nil
	}
	targetDecls, err := declarations(path, targetSource)
	if err != nil {
		return nil, fmt.Errorf("parse target: %w", err)
	}

	findings := make([]finding, 0)
	for key, branchBody := range branchDecls {
		parentBody, changedFromParent := parentDecls[key]
		if changedFromParent && parentBody == branchBody {
			continue // the branch left this declaration alone
		}
		targetBody, exists := targetDecls[key]
		switch {
		case !exists:
			findings = append(findings, finding{File: path, Kind: "missing", Decl: key})
		case targetBody != branchBody && changedFromParent && targetBody == parentBody:
			// target still holds the pre-branch body, so the rebase dropped it.
			findings = append(findings, finding{File: path, Kind: "stale", Decl: key})
		}
	}
	return findings, nil
}

// declarations indexes a file's top-level declarations by a stable key and
// returns each one's normalised source. Printing through go/printer means
// reindentation and comment reflow do not read as changes; only the
// declaration's own text does.
func declarations(path, source string) (map[string]string, error) {
	if strings.TrimSpace(source) == "" {
		return map[string]string{}, nil
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, source, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, declaration := range parsed.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			result[funcKey(typed)] = render(fileSet, typed)
		case *ast.GenDecl:
			for _, spec := range typed.Specs {
				switch value := spec.(type) {
				case *ast.TypeSpec:
					result["type "+value.Name.Name] = render(fileSet, value)
				case *ast.ValueSpec:
					for _, name := range value.Names {
						if name.Name == "_" {
							continue
						}
						result[declKind(typed.Tok)+" "+name.Name] = render(fileSet, value)
					}
				}
			}
		}
	}
	return result, nil
}

func declKind(tok token.Token) string {
	if tok == token.CONST {
		return "const"
	}
	return "var"
}

// funcKey distinguishes methods sharing a name across receivers, so that a
// Read on one type is never compared against a Read on another.
func funcKey(declaration *ast.FuncDecl) string {
	if declaration.Recv == nil || len(declaration.Recv.List) == 0 {
		return "func " + declaration.Name.Name
	}
	return "method (" + receiverType(declaration.Recv.List[0].Type) + ")." + declaration.Name.Name
}

func receiverType(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.StarExpr:
		return receiverType(typed.X)
	case *ast.IndexExpr:
		return receiverType(typed.X)
	case *ast.IndexListExpr:
		return receiverType(typed.X)
	case *ast.Ident:
		return typed.Name
	default:
		return "?"
	}
}

// render prints a declaration and collapses whitespace runs. The result is only
// ever compared, never emitted, so folding layout away means a reindented or
// re-spaced declaration is not mistaken for a modified one.
func render(fileSet *token.FileSet, node ast.Node) string {
	var buffer bytes.Buffer
	if err := printer.Fprint(&buffer, fileSet, node); err != nil {
		return fmt.Sprintf("<unprintable: %v>", err)
	}
	return strings.Join(strings.Fields(buffer.String()), " ")
}
