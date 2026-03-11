package v8go_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	v8 "github.com/willyelm/v8go"
)

func TestCompileModuleWithoutImports(t *testing.T) {
	t.Parallel()

	iso := v8.NewIsolate()
	defer iso.Dispose()

	var lines []string
	global := testModuleGlobal(iso, &lines)
	ctx := v8.NewContext(iso, global)
	defer ctx.Close()

	mod, err := v8.CompileModule(iso, `print("42")`, "")
	fatalIf(t, err)

	err = mod.InstantiateModule(ctx, nil)
	fatalIf(t, err)

	val, err := mod.Evaluate(ctx)
	fatalIf(t, err)

	p, err := val.AsPromise()
	fatalIf(t, err)
	ctx.PerformMicrotaskCheckpoint()

	if got := p.State(); got != v8.Fulfilled {
		t.Fatalf("unexpected promise state: got %v want %v", got, v8.Fulfilled)
	}
	if !reflect.DeepEqual(lines, []string{"42"}) {
		t.Fatalf("unexpected output: %#v", lines)
	}
}

func TestCompileModuleSyntaxError(t *testing.T) {
	t.Parallel()

	iso := v8.NewIsolate()
	defer iso.Dispose()

	if _, err := v8.CompileModule(iso, `{ syntax error`, ""); err == nil {
		t.Fatal("expected syntax error")
	}
}

func TestCompileModuleMissingDependency(t *testing.T) {
	t.Parallel()

	iso := v8.NewIsolate()
	defer iso.Dispose()

	ctx := v8.NewContext(iso)
	defer ctx.Close()

	mod, err := v8.CompileModule(iso, `import foo from "missing";`, "")
	fatalIf(t, err)

	err = mod.InstantiateModule(ctx, &testModuleResolver{modules: map[string]string{}})
	if err == nil {
		t.Fatal("expected instantiation to fail")
	}
	if !strings.Contains(err.Error(), `cannot resolve module "missing"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileModuleImports(t *testing.T) {
	t.Parallel()

	iso := v8.NewIsolate()
	defer iso.Dispose()

	var lines []string
	global := testModuleGlobal(iso, &lines)
	ctx := v8.NewContext(iso, global)
	defer ctx.Close()

	mod, err := v8.CompileModule(iso, `
		import foo from "a";
		print(1 + foo.a + foo.b)
	`, "")
	fatalIf(t, err)

	resolver := &testModuleResolver{
		modules: map[string]string{
			"a": `import b from "b"; export default { a: 2, b };`,
			"b": `export default 3`,
		},
	}

	err = mod.InstantiateModule(ctx, resolver)
	fatalIf(t, err)

	val, err := mod.Evaluate(ctx)
	fatalIf(t, err)
	p, err := val.AsPromise()
	fatalIf(t, err)
	ctx.PerformMicrotaskCheckpoint()

	if got := p.State(); got != v8.Fulfilled {
		t.Fatalf("unexpected promise state: got %v want %v", got, v8.Fulfilled)
	}
	if !reflect.DeepEqual(lines, []string{"6"}) {
		t.Fatalf("unexpected output: %#v", lines)
	}
}

func TestDynamicImport(t *testing.T) {
	t.Parallel()

	iso := v8.NewIsolate()
	defer iso.Dispose()

	var lines []string
	global := testModuleGlobal(iso, &lines)
	ctx := v8.NewContext(iso, global)
	defer ctx.Close()

	mod, err := v8.CompileModule(iso, `
		const mod = await import("./dep.js");
		print(mod.default);
	`, "https://example.com/root.js")
	fatalIf(t, err)

	resolver := &testModuleResolver{
		modules: map[string]string{
			"https://example.com/dep.js": `export default "loaded";`,
		},
	}

	err = mod.InstantiateModule(ctx, resolver)
	fatalIf(t, err)

	val, err := mod.Evaluate(ctx)
	fatalIf(t, err)
	p, err := val.AsPromise()
	fatalIf(t, err)
	ctx.PerformMicrotaskCheckpoint()
	ctx.PerformMicrotaskCheckpoint()

	if got := p.State(); got != v8.Fulfilled {
		t.Fatalf("unexpected promise state: got %v want %v", got, v8.Fulfilled)
	}
	if !reflect.DeepEqual(lines, []string{"loaded"}) {
		t.Fatalf("unexpected output: %#v", lines)
	}
}

func TestModuleNamespace(t *testing.T) {
	t.Parallel()
	t.Skip("TODO: Module.GetModuleNamespace currently crashes in the fork bridge")
}

type testModuleResolver struct {
	modules map[string]string
	cache   map[string]*v8.Module
}

func (r *testModuleResolver) ResolveModule(ctx *v8.Context, spec string, _ v8.ImportAttributes, referrer *v8.Module) (*v8.Module, error) {
	if r.cache == nil {
		r.cache = map[string]*v8.Module{}
	}
	if mod, ok := r.cache[spec]; ok {
		return mod, nil
	}
	source, ok := r.modules[spec]
	if !ok {
		return nil, fmt.Errorf("module not found")
	}
	mod, err := v8.CompileModule(ctx.Isolate(), source, spec)
	if err != nil {
		return nil, err
	}
	r.cache[spec] = mod
	return mod, nil
}

func (r *testModuleResolver) ResolveDynamicImport(ctx *v8.Context, spec string, referrerOrigin string) (*v8.Promise, error) {
	resolved := spec
	if strings.HasPrefix(spec, "./") {
		prefix := referrerOrigin[:strings.LastIndex(referrerOrigin, "/")+1]
		resolved = prefix + strings.TrimPrefix(spec, "./")
	}

	mod, err := r.ResolveModule(ctx, resolved, v8.ImportAttributes{}, nil)
	if err != nil {
		return nil, err
	}
	if mod.GetStatus() == 0 {
		if err := mod.InstantiateModule(ctx, r); err != nil {
			return nil, err
		}
	}

	resolver, err := v8.NewPromiseResolver(ctx)
	if err != nil {
		return nil, err
	}
	promise := resolver.GetPromise()

	value, err := mod.Evaluate(ctx)
	if err != nil {
		return nil, err
	}
	if value != nil && value.IsPromise() {
		evalPromise, err := value.AsPromise()
		if err != nil {
			return nil, err
		}
		namespace := mod.GetModuleNamespace()
		evalPromise.Then(func(info *v8.FunctionCallbackInfo) *v8.Value {
			resolver.Resolve(namespace)
			return namespace
		}, func(info *v8.FunctionCallbackInfo) *v8.Value {
			if len(info.Args()) > 0 && info.Args()[0] != nil {
				resolver.Reject(info.Args()[0])
			}
			return nil
		})
		return promise, nil
	}

	resolver.Resolve(mod.GetModuleNamespace())
	return promise, nil
}

func testModuleGlobal(iso *v8.Isolate, lines *[]string) *v8.ObjectTemplate {
	global := v8.NewObjectTemplate(iso)
	printFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		if len(info.Args()) > 0 && info.Args()[0] != nil {
			*lines = append(*lines, info.Args()[0].String())
		}
		return nil
	})
	global.Set("print", printFn, v8.ReadOnly)
	return global
}
