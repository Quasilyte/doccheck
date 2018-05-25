package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"regexp"
	"strings"
	"unicode"
)

func main() {
	l := &linter{
		fset: token.NewFileSet(),
	}

	flag.StringVar(&l.path, "path", "", `path to package to be checked`)
	flag.Parse()
	if l.path == "" {
		log.Fatalf("path can't be empty")
	}

	packages, err := parser.ParseDir(l.fset, l.path, nil, parser.ParseComments)
	if err != nil {
		log.Fatalf("parse path: %v", err)
	}

	initRegexps(l)

	for _, pkg := range packages {
		l.CheckPackage(pkg)
		for _, f := range pkg.Files {
			l.CheckFile(f)
		}
	}

	os.Exit(l.ExitCode())
}

type linter struct {
	path string

	fset *token.FileSet

	current struct {
		fn *ast.FuncDecl
	}

	regexp struct {
		predAntipattern *regexp.Regexp
		predPrefix      *regexp.Regexp
		directive       *regexp.Regexp
	}

	issues int
}

func initRegexps(l *linter) {
	{
		prefixes := []string{
			"Has",
			"Is",
			"Contains",
			"Can",
		}
		for _, p := range prefixes {
			prefixes = append(prefixes, strings.ToLower(p))
		}
		pat := `(?:` + strings.Join(prefixes, "|") + `)[A-Z0-9]\w*`
		l.regexp.predPrefix = regexp.MustCompile(pat)
	}

	{
		patterns := []string{
			"returns true if",
			"returns false if",
			"returns true iff",
			"returns false iff",
			"returns true for",
			"returns false for",
			"returns true when",
			"returns false when",
			"tells whether",
			"tests whether",
			"determines whether",
			"indicates whether",
		}
		for i, p := range patterns {
			patterns[i] = " " + p + " "
		}
		pat := strings.Join(patterns, "|")
		l.regexp.predAntipattern = regexp.MustCompile(pat)
	}

	l.regexp.directive = regexp.MustCompile(`//\w+: .*`)
}

func (l *linter) warnPkg(fileName, format string, args ...interface{}) {
	l.issues++
	var anchor string
	if fileName == "" {
		anchor = l.path + ": "
	} else {
		anchor = fileName + ": "
	}
	fmt.Fprintf(os.Stderr, anchor+format+"\n", args...)
}

func (l *linter) warnFunc(format string, args ...interface{}) {
	l.issues++
	anchor := l.fset.Position(l.current.fn.Pos()).String() + ": "
	fmt.Fprintf(os.Stderr, anchor+format+"\n", args...)
}

func (l *linter) ExitCode() int {
	if l.issues == 0 {
		return 0
	}
	return 1
}

func (l *linter) CheckPackage(pkg *ast.Package) {
	var docFilename string
	var doc *ast.CommentGroup
	count := 0
	for filename, f := range pkg.Files {
		if f.Doc != nil {
			count++
			doc = f.Doc
			docFilename = filename
		}
	}

	switch count {
	case 1:
		// Good. Safe to run other checks.
	case 0:
		l.warnPkg("", "no doc-comment found")
		return
	default:
		l.warnPkg("", "found %d doc-comments, expected 1", count)
		return
	}

	if pkg.Name != "main" {
		lines := 0
		for _, c := range doc.List {
			lines += strings.Count(c.Text, "\n") + 1
		}
		if lines > 100 && docFilename != "doc.go" {
			l.warnPkg(docFilename, "long doc-comments should go into doc.go file")
		}
	}
}

func (l *linter) CheckFile(f *ast.File) {
	for _, decl := range f.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Doc != nil {
				l.current.fn = decl
				doc := decl.Doc
				l.checkBoolFuncStyle(doc)
				l.checkNoMultiline(doc)
				l.checkEndsWithPunct(doc)
				l.checkSpacing(doc)
			}
		}
	}
}

func (l *linter) checkSpacing(doc *ast.CommentGroup) {
	for _, c := range doc.List {
		if strings.HasPrefix(c.Text, "/*") {
			continue
		}
		if l.regexp.directive.MatchString(c.Text) {
			continue
		}
		if !strings.HasPrefix(c.Text, "// ") && !strings.HasPrefix(c.Text, "//\t") {
			l.warnFunc("found comment without leading space and it's not a pragma")
		}
	}
}

func (l *linter) checkEndsWithPunct(doc *ast.CommentGroup) {
	// Check only 1-line comments for now as it's easier to avoid
	// false-positives this way.
	if len(doc.List) != 1 || !strings.HasPrefix(doc.List[0].Text, "//") {
		return
	}
	line := doc.List[0].Text
	if !unicode.IsPunct(rune(line[len(line)-1])) {
		l.warnFunc("doc-comment should end with punctuation, usually with period")
	}
}

func (l *linter) checkNoMultiline(doc *ast.CommentGroup) {
	for _, c := range doc.List {
		if strings.HasPrefix(c.Text, "/*") {
			l.warnFunc("should not use /**/ comments in doc-comments")
			return
		}
	}
}

func (l *linter) checkBoolFuncStyle(doc *ast.CommentGroup) {
	if !isBooleanFunc(l.current.fn) {
		return
	}

	line := doc.List[0].Text
	name := l.current.fn.Name.Name

	// 1. Check if doc string has common pattern that is considered
	// less idiomatic than proposed alternative.
	loc := l.regexp.predAntipattern.FindStringIndex(line)
	if loc != nil {
		diff := loc[0] - len(name)
		if diff > 1 && diff <= 4 {
			l.warnFunc("bad predicate comment")
		}
	}

	// 2. Guess predicate function by it's name.
	// If it is a predicate, check doc-comment.
	if l.regexp.predPrefix.MatchString(name) {
		if !strings.Contains(line, name+" reports whether ") {
			l.warnFunc("bad predicate comment")
			return
		}
	}
}

func isBooleanFunc(decl *ast.FuncDecl) bool {
	if decl.Type.Results == nil || len(decl.Type.Results.List) != 1 {
		return false
	}
	res := decl.Type.Results.List[0]
	if len(res.Names) != 1 {
		return false
	}
	if typ, ok := res.Type.(*ast.Ident); ok {
		return typ.Name == "bool"
	}
	return false
}
