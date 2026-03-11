package v8go

// #include <stdlib.h>
// #include "v8go.h"
import "C"
import (
	"fmt"
	"unsafe"
)

type Module struct {
	iso *C.v8Isolate
	ptr C.ModulePtr
}

type ImportAttributes struct {
	fixedArray *C.v8goFixedArray
}

func (a ImportAttributes) All(ctx *Context) []ImportAttribute {
	if a.fixedArray == nil {
		return nil
	}
	l := int(C.FixedArrayLength(a.fixedArray, ctx.ptr)) / 3
	if l == 0 {
		return nil
	}
	res := make([]ImportAttribute, l)
	for i := 0; i < l; i++ {
		res[i] = a.get(ctx, i)
	}
	return res
}

func (a ImportAttributes) get(ctx *Context, i int) ImportAttribute {
	d1 := C.FixedArrayGet(a.fixedArray, ctx.ptr, C.int(i*3))
	d2 := C.FixedArrayGet(a.fixedArray, ctx.ptr, C.int(i*3+1))
	d3 := C.FixedArrayGet(a.fixedArray, ctx.ptr, C.int(i*3+2))
	defer C.DataRelease(d1)
	defer C.DataRelease(d2)
	defer C.DataRelease(d3)
	v1 := Value{ptr: C.DataAsValue(d1, ctx.ptr), ctx: ctx}
	v2 := Value{ptr: C.DataAsValue(d2, ctx.ptr), ctx: ctx}
	v3 := Value{ptr: C.DataAsValue(d3, ctx.ptr), ctx: ctx}
	return ImportAttribute{
		Key:      v1.String(),
		Value:    v2.String(),
		Location: int(v3.Int32()),
	}
}

type ImportAttribute struct {
	Key      string
	Value    string
	Location int
}

type ResolveModuler interface {
	ResolveModule(ctx *Context, spec string, attr ImportAttributes, referrer *Module) (*Module, error)
}

type DynamicImportModuler interface {
	ResolveDynamicImport(ctx *Context, spec string, referrerOrigin string) (*Promise, error)
}

func CompileModule(iso *Isolate, source, origin string) (*Module, error) {
	cSource := C.CString(source)
	cOrigin := C.CString(origin)
	defer C.free(unsafe.Pointer(cSource))
	defer C.free(unsafe.Pointer(cOrigin))

	rtn := C.CompileModule(iso.ptr, cSource, cOrigin)
	if rtn.ptr == nil {
		return nil, newJSError(rtn.error)
	}
	return &Module{iso: iso.ptr, ptr: rtn.ptr}, nil
}

func (m Module) Evaluate(ctx *Context) (*Value, error) {
	retVal := C.ModuleEvaluate(ctx.ptr, m.ptr)
	return valueResult(ctx, retVal)
}

//export resolveModuleCallback
func resolveModuleCallback(
	ctxref int,
	buf *C.char, bufLen C.int,
	importAttributes *C.v8goFixedArray,
	referrer C.ModulePtr,
) (C.ModulePtr, C.ValuePtr) {
	defer C.free(unsafe.Pointer(buf))
	spec := C.GoStringN(buf, bufLen)

	ctx := getContext(ctxref)
	ref := &Module{iso: ctx.Isolate().ptr, ptr: referrer}
	res, err := ctx.moduleResolver.ResolveModule(ctx, spec, ImportAttributes{fixedArray: importAttributes}, ref)
	if err == nil {
		return res.ptr, nil
	}
	err = fmt.Errorf("cannot resolve module %q: %w", spec, err)
	value, valueErr := NewValue(ctx.iso, err.Error())
	if valueErr != nil {
		return nil, nil
	}
	return nil, value.ptr
}

//export dynamicImportModuleCallback
func dynamicImportModuleCallback(
	ctxref int,
	resourceNameBuf *C.char, resourceNameLen C.int,
	specBuf *C.char, specLen C.int,
) C.ValuePtr {
	defer C.free(unsafe.Pointer(resourceNameBuf))
	defer C.free(unsafe.Pointer(specBuf))

	ctx := getContext(ctxref)
	if ctx == nil {
		return nil
	}

	spec := C.GoStringN(specBuf, specLen)
	referrerOrigin := C.GoStringN(resourceNameBuf, resourceNameLen)

	reject := func(err error) C.ValuePtr {
		resolver, resolverErr := NewPromiseResolver(ctx)
		if resolverErr != nil {
			return nil
		}
		value, valueErr := NewValue(ctx.iso, err.Error())
		if valueErr != nil {
			return nil
		}
		resolver.Reject(value)
		return resolver.GetPromise().ptr
	}

	resolver, ok := ctx.moduleResolver.(DynamicImportModuler)
	if !ok {
		return reject(fmt.Errorf("dynamic import is not supported"))
	}

	promise, err := resolver.ResolveDynamicImport(ctx, spec, referrerOrigin)
	if err != nil {
		return reject(fmt.Errorf("cannot resolve dynamic import %q: %w", spec, err))
	}
	if promise == nil {
		return reject(fmt.Errorf("dynamic import returned no promise"))
	}
	return promise.ptr
}

func (m Module) InstantiateModule(ctx *Context, resolver ResolveModuler) error {
	ctx.moduleResolver = resolver

	err := C.ModuleInstantiateModule(ctx.ptr, m.ptr)
	if err.msg == nil {
		return nil
	}
	return newJSError(err)
}

func (m Module) ScriptID() int {
	return int(C.ModuleScriptId(m.ptr))
}

func (m Module) GetStatus() int {
	return int(C.ModuleGetStatus(m.ptr))
}

func (m Module) IsSourceTextModule() bool {
	return C.ModuleIsSourceTextModule(m.ptr) != 0
}

func (m Module) GetModuleNamespace() *Value {
	return &Value{ptr: C.ModuleGetModuleNamespace(m.iso, m.ptr)}
}
