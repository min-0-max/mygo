//go:build darwin

package mainthread

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The test of this file reads the call sites of mygo and holds each native
// call that the Go code makes to the decision of this package (calls.go):
// a call through purego.SyscallN, through a function that
// purego.RegisterFunc or purego.RegisterLibFunc binds, or a message send
// (a call of objc_msgSend, objc_msgSend_stret or objc_msgSendSuper, whose
// selector is its second argument, and the message sends of purego's objc
// package) is outside the forwarding function only when it is a function
// or a message that runs on the calling thread, named by a constant, and
// commit, release, autorelease, CFRetain, CFRelease, _Block_copy and
// _Block_release go through the function that decides them by their
// receiver.
//
// The reader follows constant names through the code that carries them:
// a symbol through purego.Dlsym and the functions that return it, a
// selector through objc.RegisterName and the functions that register the
// name they are given, values through the assignments of variables and
// fields, the elements of composite literals and the ranges over them,
// and the parameters of functions, which it instantiates at each call: a
// message that send(obj, "close") sends is judged at that call, by its
// selector "close". What it cannot follow fails: a native call whose
// function or selector is not a constant, a binding whose target it cannot
// find, a function of purego's objc package that it does not know, and a
// function whose native calls depend on its parameters but whose calls it
// cannot see.

const modulePath = "github.com/egoist/mygo"

// excludedPackages make purego calls that reach only the library that each
// package loads from a path that its caller gives.
var excludedPackages = []string{
	modulePath + "/plugins/sqlite",
	modulePath + "/plugins/terminal/internal/vt",
}

func TestCallSitesGoThroughTheWorker(t *testing.T) {
	root := moduleRoot(t)
	r := newReader(defaultRules())
	findings, stats, err := r.readModule(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d files in %d packages; %d native call sites judged, %d run on the calling thread or go through %s",
		stats.files, stats.packages, stats.judged, stats.accepted, ByReceiverFunc)
	if len(findings) > 0 {
		lines := make([]string, len(findings))
		for i, f := range findings {
			lines[i] = f.String(root)
		}
		t.Fatalf("%d call sites neither go through %s nor run on the calling thread as this package decides:\n%s",
			len(findings), ForwardFunc, strings.Join(lines, "\n"))
	}
}

// TestTheCallReaderJudgesEachKindOfCall reads a package made for the
// reader (testdata/callsites), whose lines that the reader must report end
// with a comment "// want: <message>", each other line with a native call
// being one the reader must accept.
func TestTheCallReaderJudgesEachKindOfCall(t *testing.T) {
	root := moduleRoot(t)
	dir, err := filepath.Abs(filepath.Join("testdata", "callsites"))
	if err != nil {
		t.Fatal(err)
	}
	rules := defaultRules()
	rules.workerPackage = "callsites"
	r := newReader(rules)
	findings, stats, err := r.readDir(root, dir, "callsites")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if _, msg, ok := strings.Cut(line, "// want: "); ok {
				want[fmt.Sprintf("%s:%d", filepath.Base(path), i+1)] = strings.TrimSpace(msg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range findings {
		key := fmt.Sprintf("%s:%d", filepath.Base(f.pos.Filename), f.pos.Line)
		msg := f.msg
		if i := strings.LastIndex(msg, " (the call at "); i >= 0 {
			msg = msg[:i]
		}
		if got[key] != "" {
			t.Errorf("%s: two findings: %q and %q", key, got[key], msg)
		}
		got[key] = msg
	}
	for key, msg := range want {
		if got[key] != msg {
			t.Errorf("%s: got %q, want %q", key, got[key], msg)
		}
	}
	for key, msg := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("%s: unexpected finding %q", key, msg)
		}
	}
	if stats.accepted == 0 {
		t.Errorf("the reader accepted no call of the package")
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatal(err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		t.Fatal("not in a module")
	}
	return filepath.Dir(mod)
}

// assemblyAddress ends the name of each function of the worker package
// that its assembly defines and that returns the address of the assembly
// function whose name precedes it: exchangeAddress returns that of
// exchange.
const assemblyAddress = "Address"

// rules is what the reader holds the call sites to: the data of calls.go,
// and the package of the worker's functions.
type rules struct {
	workerPackage  string
	forward        string
	byReceiver     string
	messageSends   map[string]bool
	receiverSels   map[string]bool
	receiverFuncs  map[string]bool
	collections    []string
	selectors      map[string]bool
	anySelectors   map[string]bool
	classMessages  map[string]bool
	instances      map[string]bool
	classGroups    map[string]*Receivers
	functions      []FunctionGroup
	excludePrefix  []string
	purego, objcPk string
}

func defaultRules() rules {
	r := rules{
		workerPackage: reflect.TypeFor[Receivers]().PkgPath(),
		forward:       ForwardFunc,
		byReceiver:    ByReceiverFunc,
		messageSends:  set(MessageSendFunctions),
		receiverSels:  set(ByReceiverSelectors),
		receiverFuncs: set(ByReceiverFunctions),
		collections:   AutoreleasedCollectionPrefixes,
		selectors:     map[string]bool{},
		anySelectors:  map[string]bool{},
		classMessages: map[string]bool{},
		instances:     map[string]bool{},
		classGroups:   map[string]*Receivers{},
		functions:     CallingThreadFunctions,
		purego:        "github.com/ebitengine/purego",
		objcPk:        "github.com/ebitengine/purego/objc",
	}
	for i := range CallingThreadReceivers {
		g := &CallingThreadReceivers[i]
		for _, c := range g.Classes {
			r.classGroups[c] = g
		}
		for _, s := range g.Selectors {
			if g.AnyObject {
				r.anySelectors[s] = true
			} else {
				r.selectors[s] = true
			}
		}
		for _, c := range g.Classes {
			if g.ClassMessages {
				r.classMessages[c] = true
			}
			if g.AllMessages {
				r.instances[c] = true
			}
		}
	}
	return r
}

func set(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[s] = true
	}
	return m
}

// callingThreadFunction reports whether the function name runs on the
// calling thread when the package pkg calls it.
func (r *rules) callingThreadFunction(name, pkg string) bool {
	for _, g := range r.functions {
		if g.Package != "" && pkg != modulePath+"/"+g.Package && !strings.HasSuffix(pkg, "/"+g.Package) {
			continue
		}
		if slices.Contains(g.Names, name) {
			return true
		}
		for _, p := range g.Prefixes {
			if strings.HasPrefix(name, p) && !slices.ContainsFunc(g.Except, func(e string) bool { return strings.HasPrefix(name, e) }) {
				return true
			}
		}
	}
	return false
}

// knownReceiver reports whether the reader knows the class of each
// receiver that recv may be.
// returnsMembers reports whether the objects that the messages to the
// receiver r return belong to its group.
func (r *rules) returnsMembers(recv string) bool {
	_, class, _ := strings.Cut(recv, ":")
	g := r.classGroups[class]
	return g != nil && g.ReturnsMembers
}

func knownReceiver(recv val) bool {
	return !recv.unknown && !recv.open() && len(recv.names) > 0
}

// callingThreadReceiver reports whether the message s to each receiver
// that recv may be, a class or an instance of a class, runs on the
// calling thread.
func (r *rules) callingThreadReceiver(recv val, s string) bool {
	for n := range recv.names {
		kind, class, _ := strings.Cut(n, ":")
		g := r.classGroups[class]
		switch {
		case kind == "class" && r.classMessages[class]:
		case kind == "instance" && r.instances[class]:
		case kind == "instance" && g != nil && slices.Contains(g.Selectors, s):
		default:
			return false
		}
	}
	return true
}

func receiverText(recv val) string {
	if recv.unknown || len(recv.names) == 0 {
		return "a receiver"
	}
	list := recv.sortedNames()
	for i, n := range list {
		kind, class, _ := strings.Cut(n, ":")
		if kind == "class" {
			list[i] = "the class " + class
		} else {
			list[i] = "an instance of " + class
		}
	}
	return strings.Join(list, " or ")
}

// A finding is a call site that the reader rejects.
type finding struct {
	pos token.Position
	msg string
}

func (f finding) String(root string) string {
	name := f.pos.Filename
	if rel, err := filepath.Rel(root, name); err == nil {
		name = rel
	}
	return fmt.Sprintf("%s:%d: %s", name, f.pos.Line, f.msg)
}

type readStats struct {
	files, packages  int
	judged, accepted int
}

type reader struct {
	rules    rules
	fset     *token.FileSet
	findings map[string]finding
	stats    readStats
	files    map[string]bool
	packages map[string]bool
}

func newReader(r rules) *reader {
	return &reader{rules: r, findings: map[string]finding{}, files: map[string]bool{}, packages: map[string]bool{}}
}

// listed is a package as go list describes it.
type listed struct {
	ImportPath, Dir, Export string
	GoFiles                 []string
	Standard, DepOnly       bool
	Module                  *struct{ Path string }
	Error                   *struct{ Err string }
}

type buildConfig struct {
	arch string
	tags []string
}

func (c buildConfig) String() string { return "darwin/" + c.arch + " " + strings.Join(c.tags, ",") }

func goList(dir string, c buildConfig, args ...string) ([]listed, error) {
	all := []string{"list", "-e", "-json=ImportPath,Dir,Export,GoFiles,Standard,DepOnly,Module,Error"}
	if len(c.tags) > 0 {
		all = append(all, "-tags="+strings.Join(c.tags, ","))
	}
	cmd := exec.Command("go", append(all, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH="+c.arch, "CGO_ENABLED=0", "GOFLAGS=")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list (%s): %v\n%s", c, err, stderr.String())
	}
	var pkgs []listed
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var p listed
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// buildConfigs returns the configurations in which mygo builds for
// darwin: each architecture that Go supports on darwin, with each set of
// the build tags that mygo's files name besides operating systems,
// architectures, release tags and ignore, which marks the generators that
// no build of their package includes.
func buildConfigs(root string) ([]buildConfig, error) {
	out, err := exec.Command("go", "tool", "dist", "list").Output()
	if err != nil {
		return nil, err
	}
	known := map[string]bool{"unix": true, "cgo": true, "gc": true, "gccgo": true, "ignore": true}
	var arches []string
	for _, line := range strings.Fields(string(out)) {
		goos, goarch, _ := strings.Cut(line, "/")
		known[goos], known[goarch] = true, true
		if goos == "darwin" {
			arches = append(arches, goarch)
		}
	}
	custom := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "package ") {
				break
			}
			if !constraint.IsGoBuild(line) {
				continue
			}
			expr, err := constraint.Parse(line)
			if err != nil {
				return fmt.Errorf("%s: %v", path, err)
			}
			expr.Eval(func(tag string) bool {
				if !known[tag] && !strings.HasPrefix(tag, "go1.") {
					custom[tag] = true
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(custom))
	for tag := range custom {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	var configs []buildConfig
	for _, arch := range arches {
		for mask := 0; mask < 1<<len(tags); mask++ {
			c := buildConfig{arch: arch}
			for i, tag := range tags {
				if mask&(1<<i) != 0 {
					c.tags = append(c.tags, tag)
				}
			}
			configs = append(configs, c)
		}
	}
	return configs, nil
}

func excluded(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// readModule reads every non-test Go file of the module that builds for
// darwin in some configuration, other than those of excludedPackages.
func (r *reader) readModule(root string) ([]finding, readStats, error) {
	configs, err := buildConfigs(root)
	if err != nil {
		return nil, readStats{}, err
	}
	// The files of each configuration; the configurations read are those
	// that together hold every file, the first first.
	fileSets := make([]map[string]bool, len(configs))
	all := map[string]bool{}
	for i, c := range configs {
		pkgs, err := goList(root, c, "./...")
		if err != nil {
			return nil, readStats{}, err
		}
		fileSets[i] = map[string]bool{}
		for _, p := range pkgs {
			if p.Module == nil || p.Module.Path != modulePath || excluded(p.ImportPath, excludedPackages) {
				continue
			}
			for _, f := range p.GoFiles {
				name := filepath.Join(p.Dir, f)
				fileSets[i][name] = true
				all[name] = true
			}
		}
	}
	covered := map[string]bool{}
	for len(covered) < len(all) {
		best, gain := -1, 0
		for i := range configs {
			n := 0
			for f := range fileSets[i] {
				if !covered[f] {
					n++
				}
			}
			if n > gain {
				best, gain = i, n
			}
		}
		if best < 0 {
			break
		}
		for f := range fileSets[best] {
			covered[f] = true
		}
		if err := r.readConfig(root, configs[best], nil); err != nil {
			return nil, readStats{}, err
		}
	}
	r.stats.files = len(all)
	return r.sorted(), r.stats, nil
}

// readDir reads the package in dir, whose import path is path, outside
// the module's own packages: the test of the reader.
func (r *reader) readDir(root, dir, path string) ([]finding, readStats, error) {
	if err := r.readConfig(root, buildConfig{arch: "arm64"}, &extraPackage{dir: dir, path: path}); err != nil {
		return nil, readStats{}, err
	}
	return r.sorted(), r.stats, nil
}

type extraPackage struct{ dir, path string }

func (r *reader) sorted() []finding {
	list := make([]finding, 0, len(r.findings))
	for _, f := range r.findings {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i].pos, list[j].pos
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return list[i].msg < list[j].msg
	})
	return list
}

// A universe type-checks the module's packages of one configuration from
// their source, so that each object of the module is one object, and
// imports every other package from its export data.
type universe struct {
	fset    *token.FileSet
	module  map[string]listed
	gc      types.Importer
	checked map[string]*srcPackage
	extra   *extraPackage
}

type srcPackage struct {
	path  string
	files []*ast.File
	info  *types.Info
	pkg   *types.Package
	scan  bool
}

func (u *universe) Import(path string) (*types.Package, error) {
	if p, ok := u.checked[path]; ok {
		if p == nil {
			return nil, fmt.Errorf("import cycle at %s", path)
		}
		return p.pkg, nil
	}
	if l, ok := u.module[path]; ok {
		p, err := u.check(path, l.Dir, l.GoFiles)
		if err != nil {
			return nil, err
		}
		return p.pkg, nil
	}
	return u.gc.Import(path)
}

func (u *universe) check(path, dir string, names []string) (*srcPackage, error) {
	u.checked[path] = nil
	var files []*ast.File
	for _, name := range names {
		f, err := parser.ParseFile(u.fset, filepath.Join(dir, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Instances:  map[*ast.Ident]types.Instance{},
	}
	conf := types.Config{Importer: u, Sizes: types.SizesFor("gc", "arm64")}
	pkg, err := conf.Check(path, u.fset, files, info)
	if err != nil {
		return nil, fmt.Errorf("type-checking %s: %w", path, err)
	}
	p := &srcPackage{path: path, files: files, info: info, pkg: pkg}
	u.checked[path] = p
	return p, nil
}

func (r *reader) readConfig(root string, c buildConfig, extra *extraPackage) error {
	args := []string{"-export", "-deps", "./..."}
	if extra != nil {
		// The test package imports only purego.
		args = []string{"-export", "-deps", r.rules.purego, r.rules.objcPk, "unsafe", "runtime"}
	}
	pkgs, err := goList(root, c, args...)
	if err != nil {
		return err
	}
	if r.fset == nil {
		r.fset = token.NewFileSet()
	}
	u := &universe{fset: r.fset, module: map[string]listed{}, checked: map[string]*srcPackage{}, extra: extra}
	exports := map[string]string{}
	for _, p := range pkgs {
		if p.Error != nil && p.ImportPath != modulePath+"/internal/e2e" {
			return fmt.Errorf("%s: %s", p.ImportPath, p.Error.Err)
		}
		exports[p.ImportPath] = p.Export
		if p.Module != nil && p.Module.Path == modulePath && extra == nil && len(p.GoFiles) > 0 {
			u.module[p.ImportPath] = p
		}
	}
	u.gc = importer.ForCompiler(r.fset, "gc", func(path string) (io.ReadCloser, error) {
		if exports[path] == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(exports[path])
	})
	var scanned []*srcPackage
	if extra != nil {
		entries, err := os.ReadDir(extra.dir)
		if err != nil {
			return err
		}
		var names []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".go") {
				names = append(names, e.Name())
			}
		}
		p, err := u.check(extra.path, extra.dir, names)
		if err != nil {
			return err
		}
		p.scan = true
		scanned = append(scanned, p)
	} else {
		paths := make([]string, 0, len(u.module))
		for path := range u.module {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if _, err := u.Import(path); err != nil {
				return err
			}
			p := u.checked[path]
			if !excluded(path, excludedPackages) {
				p.scan = true
			}
		}
		for _, path := range paths {
			scanned = append(scanned, u.checked[path])
		}
	}
	a := newAnalysis(r, scanned)
	a.run()
	for _, p := range scanned {
		if p.scan {
			r.packages[p.path] = true
		}
	}
	r.stats.packages = len(r.packages)
	return nil
}

// --- analysis ---

// A pref is a parameter that a value depends on: the parameter itself
// (elem -1), or element elem of a variadic parameter.
type pref struct {
	v    *types.Var
	elem int
	// wrap marks the value as a class ("class:") or an instance of a class
	// ("instance:") whose name the parameter holds.
	wrap string
}

// A val is what the reader knows of a value: constant names (of symbols,
// selectors or strings), parameters it depends on, or that it is unknown.
type val struct {
	names   map[string]bool
	params  map[pref]bool
	unknown bool
	why     string
}

func names(s ...string) val {
	v := val{names: map[string]bool{}}
	for _, n := range s {
		v.names[n] = true
	}
	return v
}

func unknown(why string) val { return val{unknown: true, why: why} }

func param(p pref) val { return val{params: map[pref]bool{p: true}} }

func (v val) open() bool { return len(v.params) > 0 }

// concat returns the strings that x + y makes, each name of x followed by
// each name of y.
func concat(x, y val) val {
	switch {
	case x.unknown:
		return x
	case y.unknown:
		return y
	case x.open() || y.open():
		return unknown("a concatenation of a parameter")
	}
	out := names()
	for a := range x.names {
		for b := range y.names {
			out.names[a+b] = true
		}
	}
	return out
}

func union(vs ...val) val {
	out := val{}
	for _, v := range vs {
		for n := range v.names {
			if out.names == nil {
				out.names = map[string]bool{}
			}
			out.names[n] = true
		}
		for p := range v.params {
			if out.params == nil {
				out.params = map[pref]bool{}
			}
			out.params[p] = true
		}
		if v.unknown && !out.unknown {
			out.unknown, out.why = true, v.why
		}
	}
	return out
}

func (v val) sortedNames() []string {
	list := make([]string, 0, len(v.names))
	for n := range v.names {
		list = append(list, n)
	}
	sort.Strings(list)
	return list
}

func (v val) key() string {
	var b strings.Builder
	for _, n := range v.sortedNames() {
		b.WriteString(n)
		b.WriteByte(',')
	}
	ps := make([]string, 0, len(v.params))
	for p := range v.params {
		ps = append(ps, fmt.Sprintf("%p/%d", p.v, p.elem))
	}
	sort.Strings(ps)
	b.WriteString(strings.Join(ps, ","))
	if v.unknown {
		b.WriteString("?")
	}
	return b.String()
}

// fn is a function of the code read: a declaration or a literal.
type fn struct {
	pkg    *srcPackage
	decl   *ast.FuncDecl
	lit    *ast.FuncLit
	obj    *types.Func
	sig    *types.Signature
	body   *ast.BlockStmt
	parent *fn
	depth  int
	params []*types.Var // the receiver first, for methods
	recv   bool
	// returns are the first results of its return statements.
	returns []ast.Expr
	// sites are the calls of it that the reader follows, escapes the uses
	// of it that it does not.
	sites   []site
	escapes []token.Pos
	summary map[string]tmpl
	// instantiated and closed are the templates of summary that the
	// reader instantiated at the sites, and judged without them.
	instantiated, closed map[string]bool
	name                 string
}

type site struct {
	call *ast.CallExpr
	in   *fn
	recv ast.Expr // the receiver of a method call
}

// A tmpl is a native call: the function it calls, the second argument
// (the selector of a message send), whether it goes through the function
// that decides it by its receiver, and where it is.
type tmpl struct {
	fn, recv, arg1 val
	decided        bool
	origin         token.Pos
	pkg            *srcPackage
	sites          []token.Pos
}

func (t tmpl) key() string {
	return fmt.Sprintf("%d|%s|%s|%s|%v", t.origin, t.fn.key(), t.recv.key(), t.arg1.key(), t.decided)
}

type paramInfo struct {
	fn    *fn
	index int
}

type assign struct {
	pkg    *srcPackage
	rhs    ast.Expr
	result int // the result of a call of several results, or -1
	over   ast.Expr
	elem   bool // over's elements: the value of a range
}

type elemAssign struct {
	pkg   *srcPackage
	index int // -1: not a constant index
	rhs   ast.Expr
}

type binding struct {
	target *types.Var
	value  val
}

type analysis struct {
	r        *reader
	pkgs     []*srcPackage
	fns      []*fn
	byObj    map[*types.Func]*fn
	byLit    map[*ast.FuncLit]*fn
	params   map[*types.Var]paramInfo
	assigns  map[*types.Var][]assign
	elems    map[*types.Var][]elemAssign
	litVars  map[*types.Var][]*fn // variables holding function literals
	bound    map[*types.Var][]val
	inFlight map[any]bool
	varMemo  map[*types.Var]val
	// fixed are the selector parameters of the methods that mygo
	// registers, which hold the selector of the method.
	fixed    map[*types.Var]val
	worklist []*fn
	queued   map[*fn]bool
	candMemo map[*types.Var][]*fn
}

func newAnalysis(r *reader, pkgs []*srcPackage) *analysis {
	return &analysis{
		r: r, pkgs: pkgs,
		byObj: map[*types.Func]*fn{}, byLit: map[*ast.FuncLit]*fn{},
		params: map[*types.Var]paramInfo{}, assigns: map[*types.Var][]assign{},
		elems: map[*types.Var][]elemAssign{}, litVars: map[*types.Var][]*fn{},
		bound: map[*types.Var][]val{}, inFlight: map[any]bool{}, queued: map[*fn]bool{},
		candMemo: map[*types.Var][]*fn{}, varMemo: map[*types.Var]val{}, fixed: map[*types.Var]val{},
	}
}

func (a *analysis) pos(p token.Pos) token.Position { return a.r.fset.Position(p) }

func (a *analysis) report(at token.Pos, msg string) {
	p := a.pos(at)
	key := fmt.Sprintf("%s:%d:%s", p.Filename, p.Line, msg)
	a.r.findings[key] = finding{pos: p, msg: msg}
}

func (a *analysis) run() {
	for _, p := range a.pkgs {
		a.index(p)
	}
	for _, p := range a.pkgs {
		a.indexSites(p)
	}
	a.bindings()
	a.methodSelectors()
	for _, f := range a.fns {
		a.primitives(f)
	}
	for {
		for len(a.worklist) > 0 {
			f := a.worklist[0]
			a.worklist = a.worklist[1:]
			a.queued[f] = false
			a.instantiate(f)
		}
		if !a.unfollowed() {
			return
		}
	}
}

// unfollowed judges the native calls that depend on parameters of a
// function whose calls the reader does not see, and reports whether it
// placed any call to be instantiated further.
func (a *analysis) unfollowed() bool {
	placed := false
	for _, f := range a.fns {
		if len(f.summary) == 0 {
			continue
		}
		var why string
		switch {
		case len(f.escapes) > 0:
			e := a.pos(f.escapes[0])
			why = fmt.Sprintf("%s is used at %s:%d, where the reader cannot follow its arguments", f.name, filepath.Base(e.Filename), e.Line)
		case len(f.sites) == 0:
			why = fmt.Sprintf("no call of %s that the reader follows gives them", f.name)
		default:
			continue
		}
		for k, t := range f.summary {
			if !f.pkg.scan || f.closed[k] {
				continue
			}
			f.closed[k] = true
			at := t.origin
			if len(t.sites) > 0 {
				at = t.sites[len(t.sites)-1]
			}
			// A receiver that the reader cannot follow is judged as unknown:
			// the call is then judged by its function and selector alone.
			if !dependsOn(t.fn, f) && !dependsOn(t.arg1, f) {
				t.recv = unknown("a parameter of " + f.name)
				a.place(t, at)
				placed = true
				continue
			}
			a.report(at, "a native call depends on parameters of "+f.name+" and "+why)
		}
	}
	return placed
}

func dependsOn(v val, f *fn) bool {
	for p := range v.params {
		if slices.Contains(f.params, p.v) {
			return true
		}
	}
	return false
}

// index records the functions, their parameters and returns, and the
// assignments of each package.
func (a *analysis) index(p *srcPackage) {
	for _, file := range p.files {
		var stack []*fn
		cur := func() *fn {
			if len(stack) == 0 {
				return nil
			}
			return stack[len(stack)-1]
		}
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				if n.Body == nil {
					return false
				}
				obj, _ := p.info.Defs[n.Name].(*types.Func)
				f := a.newFn(p, nil, obj, n.Body, obj.Type().(*types.Signature))
				f.decl = n
				f.name = obj.Name()
				if sig := obj.Type().(*types.Signature); sig.Recv() != nil {
					f.name = recvName(sig.Recv().Type()) + "." + obj.Name()
				}
				a.byObj[obj] = f
				stack = append(stack, f)
				ast.Inspect(n.Body, visit)
				stack = stack[:len(stack)-1]
				return false
			case *ast.FuncLit:
				parent := cur()
				f := a.newFn(p, parent, nil, n.Body, p.info.Types[n].Type.(*types.Signature))
				f.lit = n
				if parent != nil {
					f.name = "a function literal in " + parent.name
				} else {
					f.name = "a function literal"
				}
				a.byLit[n] = f
				stack = append(stack, f)
				ast.Inspect(n.Body, visit)
				stack = stack[:len(stack)-1]
				return false
			case *ast.ReturnStmt:
				if f := cur(); f != nil && len(n.Results) > 0 {
					f.returns = append(f.returns, n.Results[0])
				}
			case *ast.AssignStmt:
				a.indexAssign(p, n.Lhs, n.Rhs, n.Tok)
			case *ast.ValueSpec:
				lhs := make([]ast.Expr, len(n.Names))
				for i, name := range n.Names {
					lhs[i] = name
				}
				if len(n.Values) > 0 {
					a.indexAssign(p, lhs, n.Values, token.DEFINE)
				}
			case *ast.RangeStmt:
				if id, ok := n.Value.(*ast.Ident); ok {
					if v, ok := a.object(p, id).(*types.Var); ok {
						a.assigns[v] = append(a.assigns[v], assign{pkg: p, over: n.X, elem: true, result: -1})
					}
				}
			case *ast.CompositeLit:
				a.indexComposite(p, n)
			}
			return true
		}
		for _, d := range file.Decls {
			ast.Inspect(d, visit)
		}
	}
}

func recvName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return t.String()
}

func (a *analysis) newFn(p *srcPackage, parent *fn, obj *types.Func, body *ast.BlockStmt, sig *types.Signature) *fn {
	f := &fn{pkg: p, obj: obj, sig: sig, body: body, parent: parent, summary: map[string]tmpl{}, instantiated: map[string]bool{}, closed: map[string]bool{}}
	if parent != nil {
		f.depth = parent.depth + 1
	}
	if sig.Recv() != nil {
		f.params = append(f.params, sig.Recv())
		f.recv = true
	}
	for i := range sig.Params().Len() {
		f.params = append(f.params, sig.Params().At(i))
	}
	for i, v := range f.params {
		a.params[v] = paramInfo{fn: f, index: i}
	}
	a.fns = append(a.fns, f)
	return f
}

func (a *analysis) object(p *srcPackage, id *ast.Ident) types.Object {
	if o := p.info.Defs[id]; o != nil {
		return o
	}
	return p.info.Uses[id]
}

// target returns the variable or field that an assignment or a binding
// writes, and for an element (x[i] = v) the array and the index.
func (a *analysis) target(p *srcPackage, e ast.Expr) (v *types.Var, elem *types.Var, index int) {
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		v, _ := a.object(p, e).(*types.Var)
		return v, nil, 0
	case *ast.SelectorExpr:
		if s := p.info.Selections[e]; s != nil {
			if v, ok := s.Obj().(*types.Var); ok {
				return v, nil, 0
			}
			return nil, nil, 0
		}
		v, _ := p.info.Uses[e.Sel].(*types.Var)
		return v, nil, 0
	case *ast.IndexExpr:
		if id, ok := ast.Unparen(e.X).(*ast.Ident); ok {
			if arr, ok := a.object(p, id).(*types.Var); ok {
				index := -1
				if tv := p.info.Types[e.Index]; tv.Value != nil {
					if i, ok := constant.Int64Val(constant.ToInt(tv.Value)); ok {
						index = int(i)
					}
				}
				return nil, arr, index
			}
		}
	}
	return nil, nil, 0
}

func (a *analysis) indexAssign(p *srcPackage, lhs, rhs []ast.Expr, tok token.Token) {
	if tok != token.ASSIGN && tok != token.DEFINE {
		for _, l := range lhs {
			if v, _, _ := a.target(p, l); v != nil {
				a.assigns[v] = append(a.assigns[v], assign{pkg: p, result: -1})
			}
		}
		return
	}
	for i, l := range lhs {
		var as assign
		switch {
		case len(lhs) == len(rhs):
			as = assign{pkg: p, rhs: rhs[i], result: -1}
		case len(rhs) == 1:
			as = assign{pkg: p, rhs: rhs[0], result: i}
		default:
			continue
		}
		v, arr, index := a.target(p, l)
		switch {
		case v != nil:
			a.assigns[v] = append(a.assigns[v], as)
			if lit, ok := ast.Unparen(as.rhs).(*ast.FuncLit); ok && as.result < 0 {
				a.litVars[v] = append(a.litVars[v], &fn{lit: lit})
			}
		case arr != nil:
			a.elems[arr] = append(a.elems[arr], elemAssign{pkg: p, index: index, rhs: as.rhs})
		}
	}
}

func (a *analysis) indexComposite(p *srcPackage, lit *ast.CompositeLit) {
	t := p.info.Types[lit].Type
	if t == nil {
		return
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return
	}
	for i, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok {
				if v, ok := p.info.Uses[id].(*types.Var); ok && v.IsField() {
					a.assigns[v] = append(a.assigns[v], assign{pkg: p, rhs: kv.Value, result: -1})
					if fl, ok := ast.Unparen(kv.Value).(*ast.FuncLit); ok {
						a.litVars[v] = append(a.litVars[v], &fn{lit: fl})
					}
				}
			}
			continue
		}
		if i < st.NumFields() {
			v := st.Field(i)
			a.assigns[v] = append(a.assigns[v], assign{pkg: p, rhs: e, result: -1})
			if fl, ok := ast.Unparen(e).(*ast.FuncLit); ok {
				a.litVars[v] = append(a.litVars[v], &fn{lit: fl})
			}
		}
	}
}

// --- calls ---

type callKind int

const (
	callNone       callKind = iota
	callGo                  // a function of the code read
	callSyscall             // purego.SyscallN
	callBound               // a function that RegisterFunc or RegisterLibFunc binds
	callBind                // RegisterFunc or RegisterLibFunc
	callAPI                 // a function of purego's objc package
	callForward             // the forwarding function
	callByReceiver          // the function that decides a call by its receiver
	callUnknownAPI          // a function of purego's objc package that the reader does not know
)

type callInfo struct {
	kind  callKind
	fns   []*fn
	recv  ast.Expr
	bound *types.Var
	api   apiCall
	name  string
}

// apiCall describes a function of purego's objc package: the native
// functions it calls, and for a message send the argument of the call
// that is the selector (selArg, -1 for the receiver of a method).
type apiCall struct {
	natives []string
	selArg  int
	recvArg int
	send    bool
}

func (a *analysis) objcAPI(f *types.Func) (apiCall, bool) {
	name := f.Name()
	recv := ""
	if sig, ok := f.Type().(*types.Signature); ok && sig.Recv() != nil {
		recv = recvName(sig.Recv().Type())
	}
	switch recv + "." + name {
	case "ID.Send":
		return apiCall{natives: []string{"objc_msgSend"}, recvArg: -1, selArg: 0, send: true}, true
	case ".Send":
		return apiCall{natives: []string{"objc_msgSend"}, recvArg: 0, selArg: 1, send: true}, true
	case "ID.SendSuper":
		return apiCall{natives: []string{"objc_msgSendSuper2"}, recvArg: -1, selArg: 0, send: true}, true
	case ".SendSuper":
		return apiCall{natives: []string{"objc_msgSendSuper2"}, recvArg: 0, selArg: 1, send: true}, true
	case "Block.Copy":
		return apiCall{natives: []string{"_Block_copy"}}, true
	case "Block.Release":
		return apiCall{natives: []string{"_Block_release"}}, true
	case ".InvokeBlock":
		return apiCall{natives: []string{"_Block_copy", "_Block_release"}}, true
	case "ID.Class":
		return apiCall{natives: []string{"object_getClass"}}, true
	case "ID.GetIvar":
		return apiCall{natives: []string{"object_getIvar"}}, true
	case "ID.SetIvar":
		return apiCall{natives: []string{"object_setIvar"}}, true
	case ".RegisterName":
		return apiCall{natives: []string{"sel_registerName"}}, true
	case ".GetClass":
		return apiCall{natives: []string{"objc_getClass"}}, true
	case ".GetProtocol":
		return apiCall{natives: []string{"objc_getProtocol"}}, true
	case ".AllocateProtocol":
		return apiCall{natives: []string{"objc_allocateProtocol"}}, true
	case ".RegisterClass":
		return apiCall{natives: []string{"objc_allocateClassPair", "class_addIvar", "class_addMethod", "class_addProtocol", "objc_registerClassPair", "class_getSuperclass"}}, true
	case "Class.SuperClass":
		return apiCall{natives: []string{"class_getSuperclass"}}, true
	case "Class.AddMethod":
		return apiCall{natives: []string{"class_addMethod"}}, true
	case "Class.AddProtocol":
		return apiCall{natives: []string{"class_addProtocol"}}, true
	case "Class.InstanceSize":
		return apiCall{natives: []string{"class_getInstanceSize"}}, true
	case "Class.InstanceVariable":
		return apiCall{natives: []string{"class_getInstanceVariable"}}, true
	case "Ivar.Offset":
		return apiCall{natives: []string{"ivar_getOffset"}}, true
	case "Ivar.Name":
		return apiCall{natives: []string{"ivar_getName"}}, true
	case "Protocol.Register":
		return apiCall{natives: []string{"objc_registerProtocol"}}, true
	case "Protocol.Name":
		return apiCall{natives: []string{"protocol_getName"}}, true
	case "Protocol.Equals":
		return apiCall{natives: []string{"protocol_isEqual"}}, true
	case "Protocol.AddMethodDescription":
		return apiCall{natives: []string{"protocol_addMethodDescription"}}, true
	case "Protocol.AddProtocol":
		return apiCall{natives: []string{"protocol_addProtocol"}}, true
	case "Protocol.AddProperty":
		return apiCall{natives: []string{"protocol_addProperty"}}, true
	case "Protocol.CopyMethodDescriptionList":
		return apiCall{natives: []string{"protocol_copyMethodDescriptionList", "free"}}, true
	case "Protocol.CopyProtocolList":
		return apiCall{natives: []string{"protocol_copyProtocolList", "free"}}, true
	case "Protocol.CopyPropertyList":
		return apiCall{natives: []string{"protocol_copyPropertyList2", "free"}}, true
	case "Property.Name":
		return apiCall{natives: []string{"property_getName"}}, true
	case "Property.Attributes":
		return apiCall{natives: []string{"property_getAttributes"}}, true
	case "MethodDescription.Name", "MethodDescription.Types":
		return apiCall{}, true
	// NewBlock makes a block: its _Block_copy copies the layout that
	// purego builds, which no receiver of mygo is; NewIMP makes a callback,
	// and Invoke calls the Go function of a block, both without a native
	// call.
	case ".NewBlock", ".NewIMP", "Block.Invoke":
		return apiCall{}, true
	}
	return apiCall{}, false
}

func (a *analysis) classify(p *srcPackage, call *ast.CallExpr) callInfo {
	fun := ast.Unparen(call.Fun)
	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun = f.X
	case *ast.IndexListExpr:
		fun = f.X
	}
	var obj types.Object
	var recv ast.Expr
	switch f := ast.Unparen(fun).(type) {
	case *ast.FuncLit:
		return callInfo{kind: callGo, fns: []*fn{a.byLit[f]}}
	case *ast.Ident:
		obj = p.info.Uses[f]
	case *ast.SelectorExpr:
		if s := p.info.Selections[f]; s != nil {
			obj = s.Obj()
			if s.Kind() == types.MethodVal {
				recv = f.X
			}
		} else {
			obj = p.info.Uses[f.Sel]
		}
	default:
		return callInfo{}
	}
	switch o := obj.(type) {
	case *types.Func:
		return a.classifyFunc(o, recv)
	case *types.Var:
		if len(a.bound[o]) > 0 {
			return callInfo{kind: callBound, bound: o}
		}
		if fns := a.funcValues(o); len(fns) > 0 {
			return callInfo{kind: callGo, fns: fns}
		}
		if f := a.funcObj(o); f != nil {
			return a.classifyFunc(f, nil)
		}
	}
	return callInfo{}
}

func (a *analysis) classifyFunc(o *types.Func, recv ast.Expr) callInfo {
	{
		o = o.Origin()
		if o.Pkg() == nil {
			return callInfo{}
		}
		switch path := o.Pkg().Path(); {
		case path == a.r.rules.purego:
			switch o.Name() {
			case "SyscallN":
				return callInfo{kind: callSyscall}
			case "RegisterFunc", "RegisterLibFunc":
				return callInfo{kind: callBind, name: o.Name()}
			}
			return callInfo{}
		case path == a.r.rules.objcPk:
			if api, ok := a.objcAPI(o); ok {
				return callInfo{kind: callAPI, api: api, recv: recv}
			}
			return callInfo{kind: callUnknownAPI, name: o.FullName()}
		case path == a.r.rules.workerPackage && o.Name() == a.r.rules.forward:
			return callInfo{kind: callForward}
		case path == a.r.rules.workerPackage && o.Name() == a.r.rules.byReceiver:
			return callInfo{kind: callByReceiver}
		}
		if f := a.byObj[o]; f != nil {
			return callInfo{kind: callGo, fns: []*fn{f}, recv: recv}
		}
	}
	return callInfo{}
}

// funcObj returns the declared function that each assignment of v
// assigns to it, as sel := objc.RegisterName does, or nil.
func (a *analysis) funcObj(v *types.Var) *types.Func {
	var out *types.Func
	for _, as := range a.assigns[v] {
		if as.rhs == nil || as.result >= 0 {
			return nil
		}
		var o types.Object
		switch e := ast.Unparen(as.rhs).(type) {
		case *ast.Ident:
			o = as.pkg.info.Uses[e]
		case *ast.SelectorExpr:
			if as.pkg.info.Selections[e] != nil {
				return nil
			}
			o = as.pkg.info.Uses[e.Sel]
		}
		f, ok := o.(*types.Func)
		if !ok || (out != nil && out != f) {
			return nil
		}
		out = f
	}
	return out
}

// funcValues returns the functions that a variable holds: the literals
// assigned to it, and for a parameter of function type the functions that
// the calls of its function pass.
func (a *analysis) funcValues(v *types.Var) []*fn {
	if fns, ok := a.candMemo[v]; ok {
		return fns
	}
	a.candMemo[v] = nil
	var fns []*fn
	for _, l := range a.litVars[v] {
		if f := a.byLit[l.lit]; f != nil {
			fns = append(fns, f)
		}
	}
	if pi, ok := a.params[v]; ok {
		for _, s := range pi.fn.sites {
			if arg := a.argExpr(pi.fn, s, pi.index); arg != nil {
				fns = append(fns, a.funcExprValues(s.in.pkg, arg)...)
			}
		}
	}
	a.candMemo[v] = fns
	return fns
}

func (a *analysis) funcExprValues(p *srcPackage, e ast.Expr) []*fn {
	switch e := ast.Unparen(e).(type) {
	case *ast.CallExpr:
		// The functions that the function called returns.
		var out []*fn
		for _, g := range a.classify(p, e).fns {
			for _, r := range g.returns {
				out = append(out, a.funcExprValues(g.pkg, r)...)
			}
		}
		return out
	case *ast.FuncLit:
		return []*fn{a.byLit[e]}
	case *ast.Ident:
		switch o := p.info.Uses[e].(type) {
		case *types.Func:
			if f := a.byObj[o]; f != nil {
				return []*fn{f}
			}
		case *types.Var:
			return a.funcValues(o)
		}
	case *ast.SelectorExpr:
		if o, ok := p.info.Uses[e.Sel].(*types.Func); ok {
			if f := a.byObj[o]; f != nil {
				return []*fn{f}
			}
		}
	}
	return nil
}

// argExpr returns the argument of the call s that is parameter i of f,
// nil for an element of a variadic parameter.
func (a *analysis) argExpr(f *fn, s site, i int) ast.Expr {
	if f.recv {
		if i == 0 {
			return s.recv
		}
		i--
	}
	if f.sig.Variadic() && i == f.sig.Params().Len()-1 {
		return nil
	}
	if i < len(s.call.Args) {
		return s.call.Args[i]
	}
	return nil
}

// indexSites records each call of a function of the code read, and the
// uses of a declared function that are not calls.
func (a *analysis) indexSites(p *srcPackage) {
	// A function that a package-level variable holds escapes.
	for _, file := range p.files {
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			ast.Inspect(gd, func(n ast.Node) bool {
				if _, ok := n.(*ast.FuncLit); ok {
					return false
				}
				if id, ok := n.(*ast.Ident); ok {
					if o, ok := p.info.Uses[id].(*types.Func); ok {
						if g := a.byObj[o]; g != nil {
							g.escapes = append(g.escapes, id.Pos())
						}
					}
				}
				return true
			})
		}
	}
	for _, f := range a.fns {
		if f.pkg != p {
			continue
		}
		calls := map[ast.Expr]bool{}
		inspectOwn(f, func(n ast.Node) {
			if call, ok := n.(*ast.CallExpr); ok {
				fun := ast.Unparen(call.Fun)
				switch x := fun.(type) {
				case *ast.IndexExpr:
					fun = x.X
				case *ast.IndexListExpr:
					fun = x.X
				}
				calls[fun] = true
				if s, ok := fun.(*ast.SelectorExpr); ok {
					calls[s.Sel] = true
				}
			}
		})
		inspectOwn(f, func(n ast.Node) {
			switch n := n.(type) {
			case *ast.CallExpr:
				info := a.classify(p, n)
				if info.kind == callGo {
					for _, g := range info.fns {
						g.sites = append(g.sites, site{call: n, in: f, recv: info.recv})
					}
				}
			case *ast.Ident:
				if calls[n] {
					return
				}
				if o, ok := p.info.Uses[n].(*types.Func); ok {
					if g := a.byObj[o]; g != nil {
						g.escapes = append(g.escapes, n.Pos())
					}
				}
			}
		})
	}
	// The functions that a parameter of function type receives are
	// called at the calls through that parameter, which the first pass
	// could not resolve before the sites of its function were known.
	a.candMemo = map[*types.Var][]*fn{}
	for _, f := range a.fns {
		if f.pkg != p {
			continue
		}
		inspectOwn(f, func(n ast.Node) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			id, ok := ast.Unparen(call.Fun).(*ast.Ident)
			if !ok {
				return
			}
			v, ok := p.info.Uses[id].(*types.Var)
			if !ok {
				return
			}
			if _, isParam := a.params[v]; !isParam {
				return
			}
			for _, g := range a.funcValues(v) {
				if !slices.ContainsFunc(g.sites, func(s site) bool { return s.call == call }) {
					g.sites = append(g.sites, site{call: call, in: f})
				}
			}
		})
	}
}

// inspectOwn visits the body of f without the bodies of the function
// literals in it, which are functions of their own.
func inspectOwn(f *fn, visit func(ast.Node)) {
	ast.Inspect(f.body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok && lit != f.lit {
			visit(n)
			return false
		}
		if n != nil {
			visit(n)
		}
		return true
	})
}

// methodSelectors fixes the selector parameter (the second) of each
// function that an objc.MethodDef registers as the implementation of a
// method to the selector of the method, which the runtime passes it.
func (a *analysis) methodSelectors() {
	for _, h := range a.fns {
		inspectOwn(h, func(n ast.Node) {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return
			}
			named, ok := h.pkg.info.Types[lit].Type.(*types.Named)
			if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != a.r.rules.objcPk || named.Obj().Name() != "MethodDef" {
				return
			}
			cmd, fnExpr := a.fieldOf(h.pkg, lit, "Cmd"), a.fieldOf(h.pkg, lit, "Fn")
			if cmd == nil || fnExpr == nil {
				return
			}
			sel := a.eval(h.pkg, cmd, &ectx{})
			fnParam := -1
			if id, ok := ast.Unparen(fnExpr).(*ast.Ident); ok {
				if v, ok := h.pkg.info.Uses[id].(*types.Var); ok {
					if pi, ok := a.params[v]; ok && pi.fn == h {
						fnParam = pi.index
					}
				}
			}
			if !dependsOn(sel, h) && fnParam < 0 {
				a.fix(a.funcExprValues(h.pkg, fnExpr), sel)
				return
			}
			for _, s := range h.sites {
				var fns []*fn
				if fnParam >= 0 {
					if arg := a.argExpr(h, s, fnParam); arg != nil {
						fns = a.funcExprValues(s.in.pkg, arg)
					}
				} else {
					fns = a.funcExprValues(h.pkg, fnExpr)
				}
				a.fix(fns, a.subst(sel, h, s))
			}
		})
	}
	a.varMemo = map[*types.Var]val{}
}

func (a *analysis) fix(fns []*fn, sel val) {
	for _, f := range fns {
		if f == nil || len(f.params) < 2 {
			continue
		}
		v := f.params[1]
		if old, ok := a.fixed[v]; ok {
			sel = union(old, sel)
		}
		a.fixed[v] = sel
	}
}

// --- bindings ---

// bindings finds the functions that purego.RegisterFunc and
// purego.RegisterLibFunc bind, through the functions that bind the
// function or the symbol that they are given.
func (a *analysis) bindings() {
	type open struct {
		f      *fn
		target ast.Expr
		value  ast.Expr
		lib    bool
		at     token.Pos
	}
	var opens []open
	for _, f := range a.fns {
		inspectOwn(f, func(n ast.Node) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			info := a.classify(f.pkg, call)
			if info.kind != callBind {
				return
			}
			lib := info.name == "RegisterLibFunc"
			value := call.Args[1]
			if lib {
				value = call.Args[2]
			}
			opens = append(opens, open{f: f, target: call.Args[0], value: value, lib: lib, at: call.Pos()})
		})
	}
	for _, o := range opens {
		ctx := &ectx{}
		for _, env := range a.rangeEnvs(o.f, o.target, o.value) {
			ctx.env = env
			targets, ok := a.targets(o.f.pkg, o.target, ctx)
			v := a.eval(o.f.pkg, o.value, ctx)
			if !ok {
				if o.f.pkg.scan {
					a.report(o.at, "binds a function to a target that the reader cannot follow")
				}
				continue
			}
			for _, tg := range targets {
				if tg.v != nil {
					a.bound[tg.v] = append(a.bound[tg.v], v)
					continue
				}
				// The target is a parameter: bind at each call of its
				// function.
				pi := a.params[tg.p.v]
				if len(pi.fn.sites) == 0 && o.f.pkg.scan {
					a.report(o.at, "binds a function to a parameter of "+pi.fn.name+", which nothing calls that the reader follows")
				}
				for _, s := range pi.fn.sites {
					arg := a.argExpr(pi.fn, s, pi.index)
					if arg == nil {
						continue
					}
					callerCtx := &ectx{}
					tts, ok := a.targets(s.in.pkg, arg, callerCtx)
					if !ok {
						if s.in.pkg.scan {
							a.report(s.call.Pos(), "binds a function to a target that the reader cannot follow")
						}
						continue
					}
					bv := a.subst(v, pi.fn, s)
					for _, tt := range tts {
						if tt.v != nil {
							a.bound[tt.v] = append(a.bound[tt.v], bv)
						}
					}
				}
			}
		}
	}
	// Calls through a bound function are native calls, not calls of the
	// literals the variable may hold too.
	for v := range a.bound {
		delete(a.litVars, v)
	}
	a.candMemo = map[*types.Var][]*fn{}
}

type targetRef struct {
	v *types.Var
	p pref
}

// targets returns what a binding binds: the variables or fields that
// &x names, or a parameter that holds such a pointer.
func (a *analysis) targets(p *srcPackage, e ast.Expr, ctx *ectx) ([]targetRef, bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.UnaryExpr:
		if e.Op != token.AND {
			return nil, false
		}
		v, _, _ := a.target(p, e.X)
		if v == nil {
			return nil, false
		}
		return []targetRef{{v: v}}, true
	case *ast.Ident:
		v, ok := a.object(p, e).(*types.Var)
		if !ok {
			return nil, false
		}
		if el, ok := ctx.env[v]; ok {
			return a.targets(p, el, ctx)
		}
		if _, ok := a.params[v]; ok {
			return []targetRef{{p: pref{v: v, elem: -1}}}, true
		}
		var out []targetRef
		for _, as := range a.assigns[v] {
			if as.rhs == nil || as.result >= 0 {
				return nil, false
			}
			ts, ok := a.targets(as.pkg, as.rhs, ctx)
			if !ok {
				return nil, false
			}
			out = append(out, ts...)
		}
		return out, len(out) > 0
	case *ast.SelectorExpr:
		if el := a.rangeField(p, e, ctx); el != nil {
			return a.targets(p, el, ctx)
		}
	}
	return nil, false
}

// rangeEnvs returns the bindings of the range variables that the
// expressions use to each element of the composite literal they range
// over, one environment per element, or one empty environment.
func (a *analysis) rangeEnvs(f *fn, exprs ...ast.Expr) []map[*types.Var]ast.Expr {
	// The range variable is one that the expressions use, directly or
	// through the variables assigned from it, as p of
	// p, err := purego.Dlsym(f.lib, f.name).
	seen := map[*types.Var]bool{}
	var find func(e ast.Expr) *types.Var
	find = func(e ast.Expr) *types.Var {
		var found *types.Var
		ast.Inspect(e, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || found != nil {
				return found == nil
			}
			v, ok := f.pkg.info.Uses[id].(*types.Var)
			if !ok || seen[v] {
				return true
			}
			seen[v] = true
			if a.rangeEnvsOf(v) != nil {
				found = v
				return false
			}
			for _, as := range a.assigns[v] {
				if as.rhs != nil && as.pkg == f.pkg {
					if rv := find(as.rhs); rv != nil {
						found = rv
						return false
					}
				}
			}
			return true
		})
		return found
	}
	for _, e := range exprs {
		if rv := find(e); rv != nil {
			return a.rangeEnvsOf(rv)
		}
	}
	return []map[*types.Var]ast.Expr{nil}
}

// compositeOf returns the composite literal that e is, or that the
// variable e names is assigned once.
func (a *analysis) compositeOf(p *srcPackage, e ast.Expr) *ast.CompositeLit {
	switch e := ast.Unparen(e).(type) {
	case *ast.CompositeLit:
		return e
	case *ast.Ident:
		v, ok := a.object(p, e).(*types.Var)
		if !ok || len(a.assigns[v]) != 1 || a.assigns[v][0].rhs == nil {
			return nil
		}
		as := a.assigns[v][0]
		if lit, ok := ast.Unparen(as.rhs).(*ast.CompositeLit); ok {
			return lit
		}
	case *ast.SelectorExpr:
		if v, ok := p.info.Uses[e.Sel].(*types.Var); ok && p.info.Selections[e] == nil && len(a.assigns[v]) == 1 {
			if lit, ok := ast.Unparen(a.assigns[v][0].rhs).(*ast.CompositeLit); ok {
				return lit
			}
		}
	}
	return nil
}

// rangeField returns the expression that a field of a range variable has
// in the element that ctx binds it to.
func (a *analysis) rangeField(p *srcPackage, e *ast.SelectorExpr, ctx *ectx) ast.Expr {
	id, ok := ast.Unparen(e.X).(*ast.Ident)
	if !ok {
		return nil
	}
	v, ok := a.object(p, id).(*types.Var)
	if !ok {
		return nil
	}
	el, ok := ctx.env[v]
	if !ok {
		return nil
	}
	return a.fieldOf(p, el, e.Sel.Name)
}

func (a *analysis) fieldOf(p *srcPackage, el ast.Expr, name string) ast.Expr {
	lit, ok := ast.Unparen(el).(*ast.CompositeLit)
	if !ok {
		if u, ok := ast.Unparen(el).(*ast.UnaryExpr); ok && u.Op == token.AND {
			lit, ok = ast.Unparen(u.X).(*ast.CompositeLit)
		}
		if !ok || lit == nil {
			return nil
		}
	}
	t := p.info.Types[lit].Type
	if t == nil {
		return nil
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	for i, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
				return kv.Value
			}
			continue
		}
		if i < st.NumFields() && st.Field(i).Name() == name {
			return e
		}
	}
	return nil
}

// --- evaluation ---

type frameVar struct {
	v   *types.Var
	ctx *ectx
}

// frameExpr is an expression in the frame that ctx binds.
type frameExpr struct {
	e   ast.Expr
	ctx *ectx
}

type callKey struct {
	g    *fn
	call *ast.CallExpr
}

// ectx is what an evaluation binds: the parameters of the function whose
// returns it evaluates for a call, and range variables to an element.
type ectx struct {
	bind map[*types.Var]paramVal
	env  map[*types.Var]ast.Expr
}

type paramVal struct {
	whole *thunk
	elems []*thunk
	// spread is the variadic parameter of the caller that the call passes
	// on with ..., from which elements come.
	spread *types.Var
	at     int
}

func (pv paramVal) elem(k int) val {
	if pv.spread != nil {
		return param(pref{v: pv.spread, elem: pv.at + k})
	}
	if k < len(pv.elems) {
		return pv.elems[k].get()
	}
	return unknown("an argument the call does not pass")
}

// A thunk is a value that the reader evaluates when it is first read: the
// arguments of a call whose returns it follows, of which it reads few.
type thunk struct {
	f    func() val
	done bool
	v    val
}

func lazy(f func() val) *thunk { return &thunk{f: f} }

func ready(v val) *thunk { return &thunk{done: true, v: v} }

func (t *thunk) get() val {
	if !t.done {
		t.v, t.done = t.f(), true
		t.f = nil
	}
	return t.v
}

func (a *analysis) eval(p *srcPackage, e ast.Expr, ctx *ectx) val {
	if e == nil {
		return unknown("a value assigned by an operation")
	}
	if tv, ok := p.info.Types[e]; ok && tv.Value != nil {
		if tv.Value.Kind() == constant.String {
			return names(constant.StringVal(tv.Value))
		}
		return unknown("a constant that is not a string")
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return a.eval(p, e.X, ctx)
	case *ast.CallExpr:
		return a.evalCall(p, e, ctx)
	case *ast.Ident:
		v, ok := a.object(p, e).(*types.Var)
		if !ok {
			return unknown(e.Name + " is not a variable")
		}
		return a.evalVar(p, v, ctx)
	case *ast.SelectorExpr:
		if el := a.rangeField(p, e, ctx); el != nil {
			return a.eval(p, el, ctx)
		}
		if s := p.info.Selections[e]; s != nil {
			if v, ok := s.Obj().(*types.Var); ok {
				// A field of a range variable over a composite literal
				// that ctx does not bind: each element's.
				if id, ok := ast.Unparen(e.X).(*ast.Ident); ok {
					if rv, ok := a.object(p, id).(*types.Var); ok {
						if envs := a.rangeEnvsOf(rv); envs != nil {
							var out []val
							for _, env := range envs {
								out = append(out, a.eval(p, e, &ectx{bind: ctx.bind, env: env}))
							}
							return union(out...)
						}
					}
				}
				return a.evalVar(p, v, ctx)
			}
			return unknown("a method value")
		}
		if v, ok := p.info.Uses[e.Sel].(*types.Var); ok {
			return a.evalVar(p, v, ctx)
		}
		return unknown("not a variable")
	case *ast.IndexExpr:
		return a.evalIndex(p, e, ctx)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			break
		}
		// A concatenation that reaches itself, as s = s + t does, makes
		// strings without end.
		key := any(frameExpr{e, ctx})
		if a.inFlight[key] {
			return unknown("a concatenation that depends on itself")
		}
		a.inFlight[key] = true
		defer delete(a.inFlight, key)
		return concat(a.eval(p, e.X, ctx), a.eval(p, e.Y, ctx))
	}
	return unknown(fmt.Sprintf("an expression the reader does not follow (%T)", e))
}

func (a *analysis) rangeEnvsOf(v *types.Var) []map[*types.Var]ast.Expr {
	for _, as := range a.assigns[v] {
		if as.elem {
			if lit := a.compositeOf(as.pkg, as.over); lit != nil {
				envs := make([]map[*types.Var]ast.Expr, 0, len(lit.Elts))
				for _, el := range lit.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						el = kv.Value
					}
					envs = append(envs, map[*types.Var]ast.Expr{v: el})
				}
				return envs
			}
		}
	}
	return nil
}

func (a *analysis) evalVar(p *srcPackage, v *types.Var, ctx *ectx) (result val) {
	if pv, ok := ctx.bind[v]; ok {
		return pv.whole.get()
	}
	if el, ok := ctx.env[v]; ok {
		return a.eval(p, el, ctx)
	}
	if fv, ok := a.fixed[v]; ok {
		return fv
	}
	if _, ok := a.params[v]; ok {
		return param(pref{v: v, elem: -1})
	}
	// A package-level variable or a field has one value for the whole
	// program, which depends on no parameter of the function that reads
	// it: the reader evaluates it once, without the bindings of ctx.
	shared := v.IsField() || (v.Pkg() != nil && v.Parent() == v.Pkg().Scope())
	if shared {
		if m, ok := a.varMemo[v]; ok {
			return m
		}
		if len(ctx.bind) > 0 || len(ctx.env) > 0 {
			return a.evalVar(p, v, &ectx{})
		}
	}
	// A local variable is evaluated in the frame of its function that ctx
	// binds, which a nested call of the same function does not share.
	key := any(frameVar{v, ctx})
	if shared {
		key = v
	}
	if a.inFlight[key] {
		return unknown(v.Name() + " depends on itself")
	}
	a.inFlight[key] = true
	defer delete(a.inFlight, key)
	result = a.assignedValue(v, ctx)
	if shared {
		// A cycle that the evaluation cut leaves the value unknown, which
		// the memo keeps: the reader then rejects more, never less.
		if result.open() {
			result = val{names: result.names, unknown: true, why: v.Name() + " holds a parameter of a function"}
		}
		a.varMemo[v] = result
	}
	return result
}

func (a *analysis) assignedValue(v *types.Var, ctx *ectx) val {
	recs := a.assigns[v]
	if len(recs) == 0 {
		return unknown(v.Name() + " is assigned nowhere the reader sees")
	}
	var out []val
	for _, as := range recs {
		switch {
		case as.elem:
			lit := a.compositeOf(as.pkg, as.over)
			if lit == nil {
				out = append(out, a.elementsOf(as.pkg, as.over, ctx, v.Name()))
				continue
			}
			for _, el := range lit.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					el = kv.Value
				}
				out = append(out, a.eval(as.pkg, el, ctx))
			}
		case as.rhs == nil:
			out = append(out, unknown(v.Name()+" is changed by an operation"))
		case as.result > 0:
			out = append(out, unknown(v.Name()+" is not the first result of a call"))
		default:
			out = append(out, a.eval(as.pkg, as.rhs, ctx))
		}
	}
	return union(out...)
}

// elementsOf returns the values that the elements of the array or the map
// that over names are assigned.
func (a *analysis) elementsOf(p *srcPackage, over ast.Expr, ctx *ectx, name string) val {
	var arr *types.Var
	switch e := ast.Unparen(over).(type) {
	case *ast.Ident:
		arr, _ = a.object(p, e).(*types.Var)
	case *ast.SelectorExpr:
		if s := p.info.Selections[e]; s != nil {
			arr, _ = s.Obj().(*types.Var)
		} else {
			arr, _ = p.info.Uses[e.Sel].(*types.Var)
		}
	}
	if arr == nil || len(a.elems[arr]) == 0 {
		return unknown(name + " ranges over a value whose elements the reader does not follow")
	}
	var out []val
	for _, r := range a.elems[arr] {
		out = append(out, a.eval(r.pkg, r.rhs, ctx))
	}
	return union(out...)
}

func (a *analysis) evalIndex(p *srcPackage, e *ast.IndexExpr, ctx *ectx) val {
	id, ok := ast.Unparen(e.X).(*ast.Ident)
	if ok {
		if v, ok := a.object(p, id).(*types.Var); ok {
			if recs := a.elems[v]; len(recs) > 0 {
				index := -1
				if tv := p.info.Types[e.Index]; tv.Value != nil {
					if i, ok := constant.Int64Val(constant.ToInt(tv.Value)); ok {
						index = int(i)
					}
				}
				var out []val
				for _, r := range recs {
					if index < 0 || r.index < 0 || r.index == index {
						out = append(out, a.eval(r.pkg, r.rhs, ctx))
					}
				}
				if len(out) > 0 {
					return union(out...)
				}
			}
		}
	}
	if lit := a.compositeOf(p, e.X); lit != nil {
		var out []val
		for _, el := range lit.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			out = append(out, a.eval(p, el, ctx))
		}
		if len(out) > 0 {
			return union(out...)
		}
	}
	return unknown("an element the reader does not follow")
}

func (a *analysis) evalCall(p *srcPackage, call *ast.CallExpr, ctx *ectx) val {
	if tv, ok := p.info.Types[call.Fun]; ok && tv.IsType() && len(call.Args) == 1 {
		return a.eval(p, call.Args[0], ctx)
	}
	fun := ast.Unparen(call.Fun)
	var obj types.Object
	switch f := fun.(type) {
	case *ast.Ident:
		obj = p.info.Uses[f]
	case *ast.SelectorExpr:
		if s := p.info.Selections[f]; s != nil {
			obj = s.Obj()
		} else {
			obj = p.info.Uses[f.Sel]
		}
	}
	if v, ok := obj.(*types.Var); ok {
		if f := a.funcObj(v); f != nil {
			obj = f
		}
	}
	if f, ok := obj.(*types.Func); ok && f.Pkg() != nil {
		switch {
		case f.Pkg().Path() == a.r.rules.purego && f.Name() == "Dlsym" && len(call.Args) == 2:
			return a.eval(p, call.Args[1], ctx)
		case f.Pkg().Path() == a.r.rules.objcPk && f.Name() == "RegisterName" && len(call.Args) == 1:
			return a.eval(p, call.Args[0], ctx)
		case f.Pkg().Path() == a.r.rules.objcPk && f.Name() == "GetClass" && len(call.Args) == 1:
			return classNames(a.eval(p, call.Args[0], ctx))
		case f.Pkg().Path() == a.r.rules.workerPackage && a.byObj[f] == nil && strings.HasSuffix(f.Name(), assemblyAddress):
			// A function of the worker without a body, which its assembly
			// defines, returns the address of the assembly function that
			// its name names.
			return names(strings.TrimSuffix(f.Name(), assemblyAddress))
		}
	}
	info := a.classify(p, call)
	if v, ok := a.messageResult(p, call, info, ctx); ok {
		return v
	}
	if info.kind != callGo || len(info.fns) == 0 {
		return unknown("the result of a call the reader does not follow")
	}
	var out []val
	for _, g := range info.fns {
		if i := a.nameParam(g, "SEL", "RegisterName"); i >= 0 {
			s := site{call: call, recv: info.recv}
			if arg := a.argExpr(g, s, i); arg != nil {
				out = append(out, a.eval(p, arg, ctx))
				continue
			}
		}
		if i := a.nameParam(g, "", "GetClass"); i >= 0 {
			s := site{call: call, recv: info.recv}
			if arg := a.argExpr(g, s, i); arg != nil {
				out = append(out, classNames(a.eval(p, arg, ctx)))
				continue
			}
		}
		key := callKey{g, call}
		if a.inFlight[key] {
			out = append(out, unknown(g.name+" calls itself"))
			continue
		}
		if len(g.returns) == 0 {
			out = append(out, unknown(g.name+" returns nothing the reader sees"))
			continue
		}
		a.inFlight[key] = true
		inner := &ectx{bind: a.bindCall(p, g, site{call: call, recv: info.recv}, ctx), env: ctx.env}
		for _, r := range g.returns {
			out = append(out, a.eval(g.pkg, r, inner))
		}
		delete(a.inFlight, key)
	}
	return union(out...)
}

// classNames marks names as the names of classes, the receivers of the
// messages to a class.
func classNames(v val) val { return wrapNames(v, "class:") }

// wrapNames marks the names of v, and the names that its parameters will
// hold, as the names of classes or of their instances.
func wrapNames(v val, wrap string) val {
	out := val{unknown: v.unknown, why: v.why}
	for n := range v.names {
		if strings.Contains(n, ":") && !strings.HasSuffix(n, ":") {
			return unknown("a class whose name the reader does not follow")
		}
		if out.names == nil {
			out.names = map[string]bool{}
		}
		out.names[wrap+n] = true
	}
	for p := range v.params {
		if p.wrap != "" {
			return unknown("a class whose name the reader does not follow")
		}
		p.wrap = wrap
		if out.params == nil {
			out.params = map[pref]bool{}
		}
		out.params[p] = true
	}
	return out
}

// messageResult returns what the reader knows of the result of a message
// send: an instance of class X ("instance:X") for alloc, new or
// allocWithZone: sent to the class X, and for an init message sent to
// such an instance.
func (a *analysis) messageResult(p *srcPackage, call *ast.CallExpr, info callInfo, ctx *ectx) (val, bool) {
	var fnv, recv, sel val
	switch info.kind {
	case callSyscall:
		if len(call.Args) == 0 {
			return val{}, false
		}
		fnv, recv, sel = a.eval(p, call.Args[0], ctx), a.nativeArg(p, call, 1, 0, ctx), a.nativeArg(p, call, 1, 1, ctx)
	case callBound:
		fnv, recv, sel = union(a.bound[info.bound]...), a.nativeArg(p, call, 0, 0, ctx), a.nativeArg(p, call, 0, 1, ctx)
	case callAPI:
		if !info.api.send {
			return val{}, false
		}
		fnv = names(info.api.natives...)
		recv, sel = a.apiArgs(p, call, info, ctx)
	default:
		return val{}, false
	}
	if fnv.unknown || len(fnv.names) == 0 || sel.unknown || len(sel.names) == 0 {
		return unknown("the result of a message the reader does not follow"), true
	}
	for n := range fnv.names {
		if !a.r.rules.messageSends[n] {
			return unknown("the result of a native call"), true
		}
	}
	if recv.unknown || (len(recv.names) == 0 && len(recv.params) == 0) {
		return unknown("the result of a message to a receiver the reader does not follow"), true
	}
	out := val{names: map[string]bool{}}
	for s := range sel.names {
		for p := range recv.params {
			switch {
			case (s == "alloc" || s == "new" || s == "allocWithZone:") && p.wrap == "class:":
				p.wrap = "instance:"
			case strings.HasPrefix(s, "init") && p.wrap == "instance:":
			default:
				return unknown("the result of " + s), true
			}
			out = union(out, param(p))
		}
		for r := range recv.names {
			switch {
			case (s == "alloc" || s == "new" || s == "allocWithZone:") && strings.HasPrefix(r, "class:"):
				out.names["instance:"+strings.TrimPrefix(r, "class:")] = true
			case strings.HasPrefix(s, "init") && strings.HasPrefix(r, "instance:"):
				out.names[r] = true
			case a.r.rules.returnsMembers(r):
				_, class, _ := strings.Cut(r, ":")
				out.names["instance:"+class] = true
			default:
				return unknown("the result of " + s), true
			}
		}
	}
	return out, true
}

// nameParam returns the parameter that a function of one result passes to
// the function api of purego's objc package and returns, through a cache
// or not, as sel(name) and class(name) do, or -1. A function whose result
// is a bool tests the name instead. result names the type of the result
// in purego's objc package, empty for any type.
func (a *analysis) nameParam(f *fn, result, api string) int {
	res := f.sig.Results()
	if res.Len() != 1 {
		return -1
	}
	if b, ok := res.At(0).Type().Underlying().(*types.Basic); ok && b.Kind() == types.Bool {
		return -1
	}
	if result != "" {
		named, ok := res.At(0).Type().(*types.Named)
		if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != a.r.rules.objcPk || named.Obj().Name() != result {
			return -1
		}
	}
	found := -1
	inspectOwn(f, func(n ast.Node) {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return
		}
		sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
		if !ok {
			return
		}
		o, ok := f.pkg.info.Uses[sel.Sel].(*types.Func)
		if !ok || o.Pkg() == nil || o.Pkg().Path() != a.r.rules.objcPk || o.Name() != api {
			return
		}
		if id, ok := ast.Unparen(call.Args[0]).(*ast.Ident); ok {
			if v, ok := f.pkg.info.Uses[id].(*types.Var); ok {
				if pi, ok := a.params[v]; ok && pi.fn == f {
					found = pi.index
				}
			}
		}
	})
	return found
}

// bindCall binds the parameters of g to the arguments of the call s,
// evaluated in the caller's context.
func (a *analysis) bindCall(p *srcPackage, g *fn, s site, ctx *ectx) map[*types.Var]paramVal {
	bind := map[*types.Var]paramVal{}
	args := s.call.Args
	i := 0
	if g.recv {
		if s.recv != nil {
			recv := s.recv
			bind[g.params[0]] = paramVal{whole: lazy(func() val { return a.eval(p, recv, ctx) })}
		} else {
			bind[g.params[0]] = paramVal{whole: ready(unknown("a receiver"))}
		}
		i = 1
	}
	n := g.sig.Params().Len()
	for k := 0; k < n; k++ {
		v := g.params[i+k]
		if g.sig.Variadic() && k == n-1 {
			pv := paramVal{whole: ready(unknown("a variadic parameter as a whole"))}
			if s.call.Ellipsis.IsValid() && k < len(args) {
				pv = a.spreadVal(p, args[k], ctx)
			} else {
				for _, arg := range args[min(k, len(args)):] {
					pv.elems = append(pv.elems, lazy(func() val { return a.eval(p, arg, ctx) }))
				}
			}
			bind[v] = pv
			continue
		}
		if k < len(args) {
			arg := args[k]
			bind[v] = paramVal{whole: lazy(func() val { return a.eval(p, arg, ctx) })}
		} else {
			bind[v] = paramVal{whole: ready(unknown("a missing argument"))}
		}
	}
	return bind
}

// spreadVal is what a call passes with x...: the elements of a local
// array that x slices from its start, or of a variadic parameter.
func (a *analysis) spreadVal(p *srcPackage, x ast.Expr, ctx *ectx) paramVal {
	pv := paramVal{whole: ready(unknown("a variadic parameter as a whole"))}
	base := ast.Unparen(x)
	if s, ok := base.(*ast.SliceExpr); ok && s.Low == nil {
		base = ast.Unparen(s.X)
	}
	id, ok := base.(*ast.Ident)
	if !ok {
		return pv
	}
	v, ok := a.object(p, id).(*types.Var)
	if !ok {
		return pv
	}
	if b, ok := ctx.bind[v]; ok {
		return b
	}
	if pi, ok := a.params[v]; ok && pi.fn.sig.Variadic() && pi.index == len(pi.fn.params)-1 {
		pv.spread = v
		return pv
	}
	recs := a.elems[v]
	maxIndex := -1
	for _, r := range recs {
		maxIndex = max(maxIndex, r.index)
	}
	for k := 0; k <= maxIndex; k++ {
		pv.elems = append(pv.elems, lazy(func() val {
			var out []val
			for _, r := range recs {
				if r.index == k || r.index < 0 {
					out = append(out, a.eval(r.pkg, r.rhs, ctx))
				}
			}
			if len(out) == 0 {
				out = append(out, unknown("an element the reader does not follow"))
			}
			return union(out...)
		}))
	}
	return pv
}

// --- templates ---

// primitives makes the native calls of f's own body.
func (a *analysis) primitives(f *fn) {
	inspectOwn(f, func(n ast.Node) {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		info := a.classify(f.pkg, call)
		ctx := &ectx{}
		var ts []tmpl
		switch info.kind {
		case callSyscall:
			if len(call.Args) == 0 {
				return
			}
			fnv := a.eval(f.pkg, call.Args[0], ctx)
			ts = append(ts, tmpl{fn: fnv, recv: a.nativeArg(f.pkg, call, 1, 0, ctx), arg1: a.nativeArg(f.pkg, call, 1, 1, ctx)})
		case callBound:
			ts = append(ts, tmpl{fn: union(a.bound[info.bound]...), recv: a.nativeArg(f.pkg, call, 0, 0, ctx), arg1: a.nativeArg(f.pkg, call, 0, 1, ctx)})
		case callByReceiver:
			if len(call.Args) == 0 {
				return
			}
			ts = append(ts, tmpl{fn: a.eval(f.pkg, call.Args[0], ctx), recv: a.nativeArg(f.pkg, call, 1, 0, ctx), arg1: a.nativeArg(f.pkg, call, 1, 1, ctx), decided: true})
		case callAPI:
			for _, name := range info.api.natives {
				t := tmpl{fn: names(name), recv: unknown("not a message send"), arg1: unknown("not a message send")}
				if info.api.send {
					t.recv, t.arg1 = a.apiArgs(f.pkg, call, info, ctx)
				}
				ts = append(ts, t)
			}
		case callUnknownAPI:
			if f.pkg.scan {
				a.report(call.Pos(), "calls "+info.name+", a function of purego's objc package that the reader does not know")
			}
			return
		default:
			return
		}
		for _, t := range ts {
			t.origin, t.pkg = call.Pos(), f.pkg
			// Inside the function that decides a call by its receiver,
			// the call that it decides runs on the calling thread or is
			// forwarded by that decision.
			if f.obj != nil && f.obj.Pkg() != nil && f.obj.Pkg().Path() == a.r.rules.workerPackage {
				if f.obj.Name() == a.r.rules.forward {
					continue
				}
				if f.obj.Name() == a.r.rules.byReceiver && len(f.params) > 0 && t.fn.params[pref{v: f.params[0], elem: -1}] {
					continue
				}
			}
			a.place(t, call.Pos())
		}
	})
}

// apiArgs returns the receiver and the selector of a message send of
// purego's objc package.
func (a *analysis) apiArgs(p *srcPackage, call *ast.CallExpr, info callInfo, ctx *ectx) (recv, sel val) {
	recv = unknown("a receiver the reader does not follow")
	if info.api.recvArg < 0 && info.recv != nil {
		recv = a.eval(p, info.recv, ctx)
	} else if info.api.recvArg >= 0 && info.api.recvArg < len(call.Args) {
		recv = a.eval(p, call.Args[info.api.recvArg], ctx)
	}
	sel = unknown("a selector the reader does not follow")
	if info.api.selArg < len(call.Args) {
		sel = a.eval(p, call.Args[info.api.selArg], ctx)
	}
	return recv, sel
}

// nativeArg returns argument k of the native call that call makes, whose
// first native argument is call's argument first.
func (a *analysis) nativeArg(p *srcPackage, call *ast.CallExpr, first, k int, ctx *ectx) val {
	args := call.Args[min(first, len(call.Args)):]
	if call.Ellipsis.IsValid() && len(args) > 0 {
		last := len(args) - 1
		if k < last {
			return a.eval(p, args[k], ctx)
		}
		return a.spreadVal(p, args[last], ctx).elem(k - last)
	}
	if k < len(args) {
		return a.eval(p, args[k], ctx)
	}
	return unknown("no such argument")
}

// place judges a native call that depends on no parameter, and adds one
// that does to the summary of the innermost function whose parameter it
// depends on, to be judged at its calls.
func (a *analysis) place(t tmpl, at token.Pos) {
	// The receiver and the selector matter only to a message send.
	if !t.fn.open() && !t.fn.unknown && !slices.ContainsFunc(t.fn.sortedNames(), func(n string) bool { return a.r.rules.messageSends[n] }) {
		t.recv, t.arg1 = unknown("not a message send"), unknown("not a message send")
	}
	var owner *fn
	if t.fn.unknown && !t.fn.open() {
		a.judge(t, at)
		return
	}
	for _, v := range []val{t.fn, t.recv, t.arg1} {
		for p := range v.params {
			if pi, ok := a.params[p.v]; ok && (owner == nil || pi.fn.depth > owner.depth) {
				owner = pi.fn
			}
		}
	}
	if owner == nil {
		a.judge(t, at)
		return
	}
	k := t.key()
	if _, ok := owner.summary[k]; ok {
		return
	}
	owner.summary[k] = t
	if !a.queued[owner] {
		a.queued[owner] = true
		a.worklist = append(a.worklist, owner)
	}
}

// instantiate judges the summary of f at each of its calls.
func (a *analysis) instantiate(f *fn) {
	var fresh []tmpl
	for k, t := range f.summary {
		if !f.instantiated[k] {
			f.instantiated[k] = true
			fresh = append(fresh, t)
		}
	}
	for _, s := range f.sites {
		for _, t := range fresh {
			nt := t
			nt.fn = a.subst(t.fn, f, s)
			nt.recv = a.subst(t.recv, f, s)
			nt.arg1 = a.subst(t.arg1, f, s)
			nt.sites = append(slices.Clip(t.sites), s.call.Pos())
			a.place(nt, s.call.Pos())
		}
	}
}

// subst replaces the parameters of f in v with the arguments of the call s.
func (a *analysis) subst(v val, f *fn, s site) val {
	if !v.open() {
		return v
	}
	var bind map[*types.Var]paramVal
	out := val{names: v.names, unknown: v.unknown, why: v.why}
	for p := range v.params {
		pi, ok := a.params[p.v]
		if !ok || pi.fn != f {
			out = union(out, param(p))
			continue
		}
		if bind == nil {
			bind = a.bindCall(s.in.pkg, f, s, &ectx{})
		}
		pv := bind[p.v]
		var arg val
		if p.elem < 0 {
			arg = pv.whole.get()
		} else {
			arg = pv.elem(p.elem)
		}
		if p.wrap != "" {
			arg = wrapNames(arg, p.wrap)
		}
		out = union(out, arg)
	}
	return out
}

// judge holds a native call that depends on no parameter to the rules,
// at the call site at, where it became so.
func (a *analysis) judge(t tmpl, at token.Pos) {
	if !t.pkg.scan {
		return
	}
	rules := &a.r.rules
	a.r.stats.judged++
	where := ""
	if at != t.origin {
		o := a.pos(t.origin)
		where = fmt.Sprintf(" (the call at %s:%d)", filepath.Base(o.Filename), o.Line)
	}
	fail := func(msg string) { a.report(at, msg+where) }
	if t.fn.unknown {
		fail("calls a native function that no constant symbol binds: " + t.fn.why)
		return
	}
	ok := true
	for _, name := range t.fn.sortedNames() {
		if rules.messageSends[name] {
			if t.arg1.unknown {
				fail("sends a message whose selector is not a constant: " + t.arg1.why)
				ok = false
				continue
			}
			for _, s := range t.arg1.sortedNames() {
				switch {
				case slices.ContainsFunc(rules.collections, func(prefix string) bool { return strings.HasPrefix(s, prefix) }):
					fail("sends " + s + " outside " + rules.forward + ": a method that makes an autoreleased collection")
					ok = false
				case rules.receiverSels[s] && !t.decided:
					fail("sends " + s + " outside " + rules.byReceiver)
					ok = false
				case rules.receiverSels[s]:
				case t.decided:
					fail("sends " + s + " through " + rules.byReceiver + ", which decides only commit, release, autorelease and the functions that it names")
					ok = false
				case rules.anySelectors[s]:
				case knownReceiver(t.recv) && rules.callingThreadReceiver(t.recv, s):
				case !knownReceiver(t.recv) && rules.selectors[s]:
				default:
					fail("sends " + s + " to " + receiverText(t.recv) + " outside " + rules.forward)
					ok = false
				}
			}
			continue
		}
		switch {
		case rules.receiverFuncs[name] && !t.decided:
			fail("calls " + name + " outside " + rules.byReceiver)
			ok = false
		case rules.receiverFuncs[name]:
		case t.decided:
			fail("calls " + name + " through " + rules.byReceiver + ", which decides only commit, release, autorelease and the functions that it names")
			ok = false
		case rules.callingThreadFunction(name, t.pkg.path):
		default:
			fail("calls " + name + " outside " + rules.forward)
			ok = false
		}
	}
	if ok {
		a.r.stats.accepted++
	}
}
