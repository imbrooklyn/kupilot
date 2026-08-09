package kube_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/kube"
)

func TestExportedKubeBoundaryContainsNoClientGoTypes(t *testing.T) {
	t.Parallel()

	types := []reflect.Type{
		reflect.TypeOf(kube.NewConfigLoader),
		reflect.TypeOf(kube.NewClientFactory),
		reflect.TypeOf((*kube.ConfigLoader)(nil)),
		reflect.TypeOf((*kube.ClientFactory)(nil)),
		reflect.TypeOf((*kube.ClientBundle)(nil)),
		reflect.TypeOf(kube.ContextInfo{}),
		reflect.TypeOf((*kube.SafeError)(nil)),
	}
	for _, current := range types {
		assertNoKubernetesType(t, current, map[reflect.Type]bool{})
		if current.Kind() != reflect.Interface {
			for index := 0; index < current.NumMethod(); index++ {
				assertNoKubernetesType(t, current.Method(index).Type, map[reflect.Type]bool{})
			}
		}
	}
}

func assertNoKubernetesType(t *testing.T, current reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	if current == nil || seen[current] {
		return
	}
	seen[current] = true
	if strings.HasPrefix(current.PkgPath(), "k8s.io/") {
		t.Fatalf("exported Kubernetes boundary contains vendor type %s", current)
	}
	switch current.Kind() {
	case reflect.Array, reflect.Chan, reflect.Pointer, reflect.Slice:
		assertNoKubernetesType(t, current.Elem(), seen)
	case reflect.Func:
		for index := 0; index < current.NumIn(); index++ {
			assertNoKubernetesType(t, current.In(index), seen)
		}
		for index := 0; index < current.NumOut(); index++ {
			assertNoKubernetesType(t, current.Out(index), seen)
		}
	case reflect.Map:
		assertNoKubernetesType(t, current.Key(), seen)
		assertNoKubernetesType(t, current.Elem(), seen)
	case reflect.Struct:
		for index := 0; index < current.NumField(); index++ {
			field := current.Field(index)
			if field.IsExported() {
				assertNoKubernetesType(t, field.Type, seen)
			}
		}
	}
}
