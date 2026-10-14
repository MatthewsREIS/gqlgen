package shardruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/99designs/gqlgen/graphql"
)

func TestFieldRegistry(t *testing.T) {
	resetFieldRegistryForTest()

	h := func(context.Context, ObjectExecutionContext, graphql.CollectedField, any) graphql.Marshaler {
		return graphql.Null
	}

	if got, ok := LookupField("scope", "Query", "name"); ok || got != nil {
		t.Fatalf("unexpected field handler before registration: handler=%v ok=%v", got, ok)
	}

	RegisterField("scope", "Query", "name", h)

	got, ok := LookupField("scope", "Query", "name")
	if !ok {
		t.Fatal("expected registered field handler")
	}
	if got == nil {
		t.Fatal("expected non-nil field handler")
	}

	if got, ok := LookupField("scope", "Query", "missing"); ok || got != nil {
		t.Fatalf("unexpected field handler for missing field: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupField("scope", "Mutation", "name"); ok || got != nil {
		t.Fatalf("unexpected field handler for missing object: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupField("other-scope", "Query", "name"); ok || got != nil {
		t.Fatalf("unexpected field handler for missing scope: handler=%v ok=%v", got, ok)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		expected := "duplicate field shard handler registration: scope:Query:name"
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	RegisterField("scope", "Query", "name", h)
}

func TestFieldLookupSnapshotIsBuiltLazily(t *testing.T) {
	resetFieldRegistryForTest()

	h := func(context.Context, ObjectExecutionContext, graphql.CollectedField, any) graphql.Marshaler {
		return graphql.Null
	}

	const total = 16
	for i := range total {
		RegisterField("scope", "Query", fmt.Sprintf("field_%03d", i), h)
	}

	if got := len(loadFieldLookupSnapshot()); got != 0 {
		t.Fatalf("unexpected eager field snapshot size: got %d want 0", got)
	}

	got, ok := LookupField("scope", "Query", "field_000")
	if !ok || got == nil {
		t.Fatal("expected lookup to resolve registered field after lazy snapshot build")
	}

	if got := len(loadFieldLookupSnapshot()); got != total {
		t.Fatalf("unexpected rebuilt field snapshot size: got %d want %d", got, total)
	}
}

func TestStreamFieldRegistry(t *testing.T) {
	resetStreamFieldRegistryForTest()

	h := func(context.Context, ObjectExecutionContext, graphql.CollectedField, any) func(context.Context) graphql.Marshaler {
		return func(context.Context) graphql.Marshaler {
			return graphql.Null
		}
	}

	if got, ok := LookupStreamField("scope", "Query", "name"); ok || got != nil {
		t.Fatalf("unexpected stream field handler before registration: handler=%v ok=%v", got, ok)
	}

	RegisterStreamField("scope", "Query", "name", h)

	got, ok := LookupStreamField("scope", "Query", "name")
	if !ok {
		t.Fatal("expected registered stream field handler")
	}
	if got == nil {
		t.Fatal("expected non-nil stream field handler")
	}

	if got, ok := LookupStreamField("scope", "Query", "missing"); ok || got != nil {
		t.Fatalf("unexpected stream field handler for missing field: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupStreamField("scope", "Mutation", "name"); ok || got != nil {
		t.Fatalf("unexpected stream field handler for missing object: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupStreamField("other-scope", "Query", "name"); ok || got != nil {
		t.Fatalf("unexpected stream field handler for missing scope: handler=%v ok=%v", got, ok)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		expected := "duplicate stream field shard handler registration: scope:Query:name"
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	RegisterStreamField("scope", "Query", "name", h)
}

func TestComplexityRegistry(t *testing.T) {
	resetComplexityRegistryForTest()

	h := func(context.Context, ObjectExecutionContext, int, map[string]any) (int, bool) {
		return 42, true
	}

	if got, ok := LookupComplexity("scope", "Query", "name"); ok || got != nil {
		t.Fatalf("unexpected complexity handler before registration: handler=%v ok=%v", got, ok)
	}

	RegisterComplexity("scope", "Query", "name", h)

	got, ok := LookupComplexity("scope", "Query", "name")
	if !ok {
		t.Fatal("expected registered complexity handler")
	}
	if got == nil {
		t.Fatal("expected non-nil complexity handler")
	}

	if got, ok := LookupComplexity("scope", "Query", "missing"); ok || got != nil {
		t.Fatalf("unexpected complexity handler for missing field: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupComplexity("scope", "Mutation", "name"); ok || got != nil {
		t.Fatalf("unexpected complexity handler for missing object: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupComplexity("other-scope", "Query", "name"); ok || got != nil {
		t.Fatalf("unexpected complexity handler for missing scope: handler=%v ok=%v", got, ok)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		expected := "duplicate complexity shard handler registration: scope:Query:name"
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	RegisterComplexity("scope", "Query", "name", h)
}

func TestInputUnmarshalRegistryDeterministicOrder(t *testing.T) {
	resetInputUnmarshalRegistryForTest()

	type marker struct{ id string }
	inputB := &marker{id: "B"}
	inputA := &marker{id: "A"}
	inputC := &marker{id: "C"}

	if got := ListInputUnmarshalers("scope", nil); got != nil {
		t.Fatalf("unexpected input unmarshalers before registration: %v", got)
	}

	RegisterInputUnmarshaler("scope", "InputB", inputB)
	RegisterInputUnmarshaler("scope", "InputA", inputA)
	RegisterInputUnmarshaler("scope", "InputC", inputC)
	RegisterInputUnmarshaler("other-scope", "InputA", &marker{id: "other"})

	got := ListInputUnmarshalers("scope", nil)
	if len(got) != 3 {
		t.Fatalf("unexpected number of input unmarshalers: got %d want %d", len(got), 3)
	}

	if got[0] != inputA {
		t.Fatalf("unexpected first input unmarshaler: got %v want %v", got[0], inputA)
	}
	if got[1] != inputB {
		t.Fatalf("unexpected second input unmarshaler: got %v want %v", got[1], inputB)
	}
	if got[2] != inputC {
		t.Fatalf("unexpected third input unmarshaler: got %v want %v", got[2], inputC)
	}

	if got := ListInputUnmarshalers("missing-scope", nil); got != nil {
		t.Fatalf("unexpected input unmarshalers for missing scope: %v", got)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		expected := "duplicate input unmarshaler registration: scope:InputA"
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	RegisterInputUnmarshaler("scope", "InputA", &marker{id: "dup"})
}

func TestInputUnmarshalMap(t *testing.T) {
	resetInputUnmarshalRegistryForTest()

	type inputA struct{ Value string }
	type inputB struct{ Value string }

	fnA := func(context.Context, any) (inputA, error) { return inputA{}, nil }
	fnB := func(context.Context, any) (inputB, error) { return inputB{}, nil }

	RegisterInputUnmarshaler("scope", "InputA", fnA)
	RegisterInputUnmarshaler("scope", "InputB", fnB)

	inputMap := InputUnmarshalMap("scope", nil)
	if len(inputMap) != 2 {
		t.Fatalf("unexpected number of input unmarshalers in map: got %d want %d", len(inputMap), 2)
	}

	if _, ok := inputMap[reflect.TypeFor[inputA]()]; !ok {
		t.Fatal("missing inputA unmarshaler in map")
	}
	if _, ok := inputMap[reflect.TypeFor[inputB]()]; !ok {
		t.Fatal("missing inputB unmarshaler in map")
	}

	missingScope := InputUnmarshalMap("missing-scope", nil)
	if len(missingScope) != 0 {
		t.Fatalf(
			"expected empty input unmarshaler map for missing scope, got %d entries",
			len(missingScope),
		)
	}
}

func TestCodecMarshalRegistry(t *testing.T) {
	resetCodecMarshalRegistryForTest()

	h := func(context.Context, ObjectExecutionContext, ast.SelectionSet, any) graphql.Marshaler {
		return graphql.Null
	}

	if got, ok := LookupCodecMarshal("scope", "marshalFoo"); ok || got != nil {
		t.Fatalf("unexpected codec marshal handler before registration: handler=%v ok=%v", got, ok)
	}

	RegisterCodecMarshal("scope", "marshalFoo", h)

	got, ok := LookupCodecMarshal("scope", "marshalFoo")
	if !ok {
		t.Fatal("expected registered codec marshal handler")
	}
	if got == nil {
		t.Fatal("expected non-nil codec marshal handler")
	}

	if got, ok := LookupCodecMarshal("scope", "marshalMissing"); ok || got != nil {
		t.Fatalf("unexpected codec marshal handler for missing func: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupCodecMarshal("other-scope", "marshalFoo"); ok || got != nil {
		t.Fatalf("unexpected codec marshal handler for missing scope: handler=%v ok=%v", got, ok)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		expected := "duplicate codec marshal handler registration: scope:marshalFoo"
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	RegisterCodecMarshal("scope", "marshalFoo", h)
}

func TestCodecUnmarshalRegistry(t *testing.T) {
	resetCodecUnmarshalRegistryForTest()

	h := func(context.Context, ObjectExecutionContext, any) (any, error) {
		return nil, nil
	}

	if got, ok := LookupCodecUnmarshal("scope", "unmarshalBar"); ok || got != nil {
		t.Fatalf(
			"unexpected codec unmarshal handler before registration: handler=%v ok=%v",
			got,
			ok,
		)
	}

	RegisterCodecUnmarshal("scope", "unmarshalBar", h)

	got, ok := LookupCodecUnmarshal("scope", "unmarshalBar")
	if !ok {
		t.Fatal("expected registered codec unmarshal handler")
	}
	if got == nil {
		t.Fatal("expected non-nil codec unmarshal handler")
	}

	if got, ok := LookupCodecUnmarshal("scope", "unmarshalMissing"); ok || got != nil {
		t.Fatalf("unexpected codec unmarshal handler for missing func: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupCodecUnmarshal("other-scope", "unmarshalBar"); ok || got != nil {
		t.Fatalf("unexpected codec unmarshal handler for missing scope: handler=%v ok=%v", got, ok)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		expected := "duplicate codec unmarshal handler registration: scope:unmarshalBar"
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	RegisterCodecUnmarshal("scope", "unmarshalBar", h)
}

func TestRegistryDuplicatePanics(t *testing.T) {
	t.Run("object", func(t *testing.T) {
		resetObjectRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, ast.SelectionSet, any) graphql.Marshaler {
			return graphql.Null
		}

		RegisterObject(
			"scope",
			"Query",
			func(ctx context.Context, ec ObjectExecutionContext, sel ast.SelectionSet, obj any) graphql.Marshaler {
				return h(ctx, ec, sel, obj)
			},
		)
		assertDuplicateRegistrationPanic(
			t,
			"duplicate object shard handler registration: scope:Query",
			func() {
				RegisterObject(
					"scope",
					"Query",
					func(ctx context.Context, ec ObjectExecutionContext, sel ast.SelectionSet, obj any) graphql.Marshaler {
						return h(ctx, ec, sel, obj)
					},
				)
			},
		)
	})

	t.Run("stream object", func(t *testing.T) {
		resetStreamObjectRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, ast.SelectionSet) func(context.Context) graphql.Marshaler {
			return func(context.Context) graphql.Marshaler {
				return graphql.Null
			}
		}

		RegisterStreamObject(
			"scope",
			"Query",
			func(ctx context.Context, ec ObjectExecutionContext, sel ast.SelectionSet) func(context.Context) graphql.Marshaler {
				return h(ctx, ec, sel)
			},
		)
		assertDuplicateRegistrationPanic(
			t,
			"duplicate stream object shard handler registration: scope:Query",
			func() {
				RegisterStreamObject(
					"scope",
					"Query",
					func(ctx context.Context, ec ObjectExecutionContext, sel ast.SelectionSet) func(context.Context) graphql.Marshaler {
						return h(ctx, ec, sel)
					},
				)
			},
		)
	})

	t.Run("field", func(t *testing.T) {
		resetFieldRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, graphql.CollectedField, any) graphql.Marshaler {
			return graphql.Null
		}

		RegisterField("scope", "Query", "name", h)
		assertDuplicateRegistrationPanic(
			t,
			"duplicate field shard handler registration: scope:Query:name",
			func() {
				RegisterField("scope", "Query", "name", h)
			},
		)
	})

	t.Run("stream field", func(t *testing.T) {
		resetStreamFieldRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, graphql.CollectedField, any) func(context.Context) graphql.Marshaler {
			return func(context.Context) graphql.Marshaler {
				return graphql.Null
			}
		}

		RegisterStreamField("scope", "Query", "name", h)
		assertDuplicateRegistrationPanic(
			t,
			"duplicate stream field shard handler registration: scope:Query:name",
			func() {
				RegisterStreamField("scope", "Query", "name", h)
			},
		)
	})

	t.Run("complexity", func(t *testing.T) {
		resetComplexityRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, int, map[string]any) (int, bool) {
			return 42, true
		}

		RegisterComplexity("scope", "Query", "name", h)
		assertDuplicateRegistrationPanic(
			t,
			"duplicate complexity shard handler registration: scope:Query:name",
			func() {
				RegisterComplexity("scope", "Query", "name", h)
			},
		)
	})

	t.Run("input unmarshaler", func(t *testing.T) {
		resetInputUnmarshalRegistryForTest()

		type marker struct{ id string }

		RegisterInputUnmarshaler("scope", "InputA", &marker{id: "A"})
		assertDuplicateRegistrationPanic(
			t,
			"duplicate input unmarshaler registration: scope:InputA",
			func() {
				RegisterInputUnmarshaler("scope", "InputA", &marker{id: "dup"})
			},
		)
	})

	t.Run("codec marshal", func(t *testing.T) {
		resetCodecMarshalRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, ast.SelectionSet, any) graphql.Marshaler {
			return graphql.Null
		}

		RegisterCodecMarshal("scope", "marshalFoo", h)
		assertDuplicateRegistrationPanic(
			t,
			"duplicate codec marshal handler registration: scope:marshalFoo",
			func() {
				RegisterCodecMarshal("scope", "marshalFoo", h)
			},
		)
	})

	t.Run("codec unmarshal", func(t *testing.T) {
		resetCodecUnmarshalRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, any) (any, error) {
			return nil, nil
		}

		RegisterCodecUnmarshal("scope", "unmarshalBar", h)
		assertDuplicateRegistrationPanic(
			t,
			"duplicate codec unmarshal handler registration: scope:unmarshalBar",
			func() {
				RegisterCodecUnmarshal("scope", "unmarshalBar", h)
			},
		)
	})
}

func TestRegistryConcurrentAccess(t *testing.T) {
	t.Run("field", func(t *testing.T) {
		resetFieldRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, graphql.CollectedField, any) graphql.Marshaler {
			return graphql.Null
		}

		const total = 128
		const readers = 8

		errCh := make(chan error, total+readers)
		reportErr := func(err error) {
			select {
			case errCh <- err:
			default:
			}
		}

		start := make(chan struct{})
		var writersWG sync.WaitGroup
		var readersWG sync.WaitGroup
		var writesDone atomic.Bool

		for i := range total {
			writersWG.Add(1)
			go func(i int) {
				defer writersWG.Done()
				<-start
				RegisterField("scope", "Query", fmt.Sprintf("field_%03d", i), h)
			}(i)
		}

		for range readers {
			readersWG.Go(func() {
				<-start
				for !writesDone.Load() {
					for i := range total {
						handler, ok := LookupField("scope", "Query", fmt.Sprintf("field_%03d", i))
						if ok && handler == nil {
							reportErr(fmt.Errorf("nil field handler for registered key %d", i))
						}
					}
				}
			})
		}

		close(start)
		writersWG.Wait()
		writesDone.Store(true)
		readersWG.Wait()

		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}

		for i := range total {
			handler, ok := LookupField("scope", "Query", fmt.Sprintf("field_%03d", i))
			if !ok || handler == nil {
				t.Fatalf("missing registered field handler for key %d", i)
			}
		}
	})

	t.Run("stream field", func(t *testing.T) {
		resetStreamFieldRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, graphql.CollectedField, any) func(context.Context) graphql.Marshaler {
			return func(context.Context) graphql.Marshaler {
				return graphql.Null
			}
		}

		const total = 128
		const readers = 8

		errCh := make(chan error, total+readers)
		reportErr := func(err error) {
			select {
			case errCh <- err:
			default:
			}
		}

		start := make(chan struct{})
		var writersWG sync.WaitGroup
		var readersWG sync.WaitGroup
		var writesDone atomic.Bool

		for i := range total {
			writersWG.Add(1)
			go func(i int) {
				defer writersWG.Done()
				<-start
				RegisterStreamField("scope", "Query", fmt.Sprintf("stream_field_%03d", i), h)
			}(i)
		}

		for range readers {
			readersWG.Go(func() {
				<-start
				for !writesDone.Load() {
					for i := range total {
						handler, ok := LookupStreamField(
							"scope",
							"Query",
							fmt.Sprintf("stream_field_%03d", i),
						)
						if ok && handler == nil {
							reportErr(
								fmt.Errorf("nil stream field handler for registered key %d", i),
							)
						}
					}
				}
			})
		}

		close(start)
		writersWG.Wait()
		writesDone.Store(true)
		readersWG.Wait()

		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}

		for i := range total {
			handler, ok := LookupStreamField("scope", "Query", fmt.Sprintf("stream_field_%03d", i))
			if !ok || handler == nil {
				t.Fatalf("missing registered stream field handler for key %d", i)
			}
		}
	})

	t.Run("complexity", func(t *testing.T) {
		resetComplexityRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, int, map[string]any) (int, bool) {
			return 42, true
		}

		const total = 128
		const readers = 8

		errCh := make(chan error, total+readers)
		reportErr := func(err error) {
			select {
			case errCh <- err:
			default:
			}
		}

		start := make(chan struct{})
		var writersWG sync.WaitGroup
		var readersWG sync.WaitGroup
		var writesDone atomic.Bool

		for i := range total {
			writersWG.Add(1)
			go func(i int) {
				defer writersWG.Done()
				<-start
				RegisterComplexity("scope", "Query", fmt.Sprintf("complexity_%03d", i), h)
			}(i)
		}

		for range readers {
			readersWG.Go(func() {
				<-start
				for !writesDone.Load() {
					for i := range total {
						handler, ok := LookupComplexity(
							"scope",
							"Query",
							fmt.Sprintf("complexity_%03d", i),
						)
						if ok && handler == nil {
							reportErr(fmt.Errorf("nil complexity handler for registered key %d", i))
						}
					}
				}
			})
		}

		close(start)
		writersWG.Wait()
		writesDone.Store(true)
		readersWG.Wait()

		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}

		for i := range total {
			handler, ok := LookupComplexity("scope", "Query", fmt.Sprintf("complexity_%03d", i))
			if !ok || handler == nil {
				t.Fatalf("missing registered complexity handler for key %d", i)
			}
		}
	})

	t.Run("input unmarshaler", func(t *testing.T) {
		resetInputUnmarshalRegistryForTest()

		type marker struct{ id string }

		const total = 128
		const readers = 8

		errCh := make(chan error, total+readers)
		reportErr := func(err error) {
			select {
			case errCh <- err:
			default:
			}
		}

		start := make(chan struct{})
		var writersWG sync.WaitGroup
		var readersWG sync.WaitGroup
		var writesDone atomic.Bool

		for i := range total {
			writersWG.Add(1)
			go func(i int) {
				defer writersWG.Done()
				<-start
				RegisterInputUnmarshaler(
					"scope",
					fmt.Sprintf("Input_%03d", i),
					&marker{id: fmt.Sprintf("%03d", i)},
				)
			}(i)
		}

		for range readers {
			readersWG.Go(func() {
				<-start
				for !writesDone.Load() {
					unmarshalers := ListInputUnmarshalers("scope", nil)
					if len(unmarshalers) > total {
						reportErr(
							fmt.Errorf(
								"unexpected input unmarshaler count: got %d want <= %d",
								len(unmarshalers),
								total,
							),
						)
					}
					for i, unmarshaler := range unmarshalers {
						if unmarshaler == nil {
							reportErr(fmt.Errorf("nil input unmarshaler at index %d", i))
						}
					}
				}
			})
		}

		close(start)
		writersWG.Wait()
		writesDone.Store(true)
		readersWG.Wait()

		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}

		unmarshalers := ListInputUnmarshalers("scope", nil)
		if len(unmarshalers) != total {
			t.Fatalf(
				"unexpected number of registered input unmarshalers: got %d want %d",
				len(unmarshalers),
				total,
			)
		}
		for i, unmarshaler := range unmarshalers {
			if unmarshaler == nil {
				t.Fatalf("nil input unmarshaler at index %d after registration", i)
			}
		}
	})

	t.Run("codec marshal", func(t *testing.T) {
		resetCodecMarshalRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, ast.SelectionSet, any) graphql.Marshaler {
			return graphql.Null
		}

		const total = 128
		const readers = 8

		errCh := make(chan error, total+readers)
		reportErr := func(err error) {
			select {
			case errCh <- err:
			default:
			}
		}

		start := make(chan struct{})
		var writersWG sync.WaitGroup
		var readersWG sync.WaitGroup
		var writesDone atomic.Bool

		for i := range total {
			writersWG.Add(1)
			go func(i int) {
				defer writersWG.Done()
				<-start
				RegisterCodecMarshal("scope", fmt.Sprintf("marshal_%03d", i), h)
			}(i)
		}

		for range readers {
			readersWG.Go(func() {
				<-start
				for !writesDone.Load() {
					for i := range total {
						handler, ok := LookupCodecMarshal("scope", fmt.Sprintf("marshal_%03d", i))
						if ok && handler == nil {
							reportErr(
								fmt.Errorf("nil codec marshal handler for registered key %d", i),
							)
						}
					}
				}
			})
		}

		close(start)
		writersWG.Wait()
		writesDone.Store(true)
		readersWG.Wait()

		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}

		for i := range total {
			handler, ok := LookupCodecMarshal("scope", fmt.Sprintf("marshal_%03d", i))
			if !ok || handler == nil {
				t.Fatalf("missing registered codec marshal handler for key %d", i)
			}
		}
	})

	t.Run("codec unmarshal", func(t *testing.T) {
		resetCodecUnmarshalRegistryForTest()

		h := func(context.Context, ObjectExecutionContext, any) (any, error) {
			return nil, nil
		}

		const total = 128
		const readers = 8

		errCh := make(chan error, total+readers)
		reportErr := func(err error) {
			select {
			case errCh <- err:
			default:
			}
		}

		start := make(chan struct{})
		var writersWG sync.WaitGroup
		var readersWG sync.WaitGroup
		var writesDone atomic.Bool

		for i := range total {
			writersWG.Add(1)
			go func(i int) {
				defer writersWG.Done()
				<-start
				RegisterCodecUnmarshal("scope", fmt.Sprintf("unmarshal_%03d", i), h)
			}(i)
		}

		for range readers {
			readersWG.Go(func() {
				<-start
				for !writesDone.Load() {
					for i := range total {
						handler, ok := LookupCodecUnmarshal(
							"scope",
							fmt.Sprintf("unmarshal_%03d", i),
						)
						if ok && handler == nil {
							reportErr(
								fmt.Errorf("nil codec unmarshal handler for registered key %d", i),
							)
						}
					}
				}
			})
		}

		close(start)
		writersWG.Wait()
		writesDone.Store(true)
		readersWG.Wait()

		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}

		for i := range total {
			handler, ok := LookupCodecUnmarshal("scope", fmt.Sprintf("unmarshal_%03d", i))
			if !ok || handler == nil {
				t.Fatalf("missing registered codec unmarshal handler for key %d", i)
			}
		}
	})
}

func assertDuplicateRegistrationPanic(t *testing.T, expected string, register func()) {
	t.Helper()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	register()
}

func resetObjectRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	objectByScope = map[string]map[string]ObjectHandler{}
	resetObjectLookupSnapshotForTest()
}

func resetStreamObjectRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	streamByScope = map[string]map[string]StreamObjectHandler{}
	resetStreamObjectLookupSnapshotForTest()
}

func resetFieldRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	fieldByScope = map[string]map[string]map[string]FieldHandler{}
	resetFieldLookupSnapshotForTest()
}

func resetStreamFieldRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	streamFieldByScope = map[string]map[string]map[string]StreamFieldHandler{}
	resetStreamFieldLookupSnapshotForTest()
}

func resetComplexityRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	complexityByScope = map[string]map[string]map[string]ComplexityHandler{}
	resetComplexityLookupSnapshotForTest()
}

func resetInputUnmarshalRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	inputUnmarshalByScope = map[string]map[string]any{}
	resetInputUnmarshalLookupSnapshotForTest()
}

func resetCodecMarshalRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	codecMarshalByScope = map[string]map[string]CodecMarshalHandler{}
	resetCodecMarshalLookupSnapshotForTest()
}

func resetCodecUnmarshalRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	codecUnmarshalByScope = map[string]map[string]CodecUnmarshalHandler{}
	resetCodecUnmarshalLookupSnapshotForTest()
}

func resetFieldContextRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	fieldContextByScope = map[string]map[string]FieldContextHandler{}
	resetFieldContextLookupSnapshotForTest()
}

func resetResolverInvokerRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	resolverInvokerByScope = map[string]map[string]ResolverInvokerHandler{}
	resetResolverInvokerLookupSnapshotForTest()
}

func resetArgsRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()

	argsByScope = map[string]map[string]ArgsHandler{}
	resetArgsLookupSnapshotForTest()
}

func TestFieldContextRegistry(t *testing.T) {
	resetFieldContextRegistryForTest()

	h := func(context.Context, ObjectExecutionContext, graphql.CollectedField) (*graphql.FieldContext, error) {
		return &graphql.FieldContext{}, nil
	}

	if got, ok := LookupFieldContext("scope", "Query", "name"); ok || got != nil {
		t.Fatalf("unexpected field context handler before registration: handler=%v ok=%v", got, ok)
	}

	RegisterFieldContext("scope", "Query", "name", h)

	got, ok := LookupFieldContext("scope", "Query", "name")
	if !ok {
		t.Fatal("expected registered field context handler")
	}
	if got == nil {
		t.Fatal("expected non-nil field context handler")
	}

	if got, ok := LookupFieldContext("scope", "Query", "missing"); ok || got != nil {
		t.Fatalf("unexpected field context handler for missing field: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupFieldContext("scope", "Mutation", "name"); ok || got != nil {
		t.Fatalf("unexpected field context handler for missing object: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupFieldContext("other-scope", "Query", "name"); ok || got != nil {
		t.Fatalf("unexpected field context handler for missing scope: handler=%v ok=%v", got, ok)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		expected := "duplicate field context handler registration: scope:Query:name"
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	RegisterFieldContext("scope", "Query", "name", h)
}

func TestResolverInvokerRegistry(t *testing.T) {
	resetResolverInvokerRegistryForTest()

	h := func(context.Context, ObjectExecutionContext, any) (any, error) {
		return "resolved", nil
	}

	if got, ok := LookupResolverInvoker("scope", "Query", "name"); ok || got != nil {
		t.Fatalf(
			"unexpected resolver invoker handler before registration: handler=%v ok=%v",
			got,
			ok,
		)
	}

	RegisterResolverInvoker("scope", "Query", "name", h)

	got, ok := LookupResolverInvoker("scope", "Query", "name")
	if !ok {
		t.Fatal("expected registered resolver invoker handler")
	}
	if got == nil {
		t.Fatal("expected non-nil resolver invoker handler")
	}

	result, err := got(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "resolved" {
		t.Fatalf("unexpected result: got %v want %q", result, "resolved")
	}

	if got, ok := LookupResolverInvoker("scope", "Query", "missing"); ok || got != nil {
		t.Fatalf("unexpected resolver invoker handler for missing field: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupResolverInvoker("scope", "Mutation", "name"); ok || got != nil {
		t.Fatalf(
			"unexpected resolver invoker handler for missing object: handler=%v ok=%v",
			got,
			ok,
		)
	}
	if got, ok := LookupResolverInvoker("other-scope", "Query", "name"); ok || got != nil {
		t.Fatalf("unexpected resolver invoker handler for missing scope: handler=%v ok=%v", got, ok)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		expected := "duplicate resolver invoker handler registration: scope:Query:name"
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	RegisterResolverInvoker("scope", "Query", "name", h)
}

func TestArgsRegistry(t *testing.T) {
	resetArgsRegistryForTest()

	h := func(context.Context, ObjectExecutionContext, map[string]any) (map[string]any, error) {
		return map[string]any{"parsed": true}, nil
	}

	if got, ok := LookupArgs("scope", "Query.name"); ok || got != nil {
		t.Fatalf("unexpected args handler before registration: handler=%v ok=%v", got, ok)
	}

	RegisterArgs("scope", "Query.name", h)

	got, ok := LookupArgs("scope", "Query.name")
	if !ok {
		t.Fatal("expected registered args handler")
	}
	if got == nil {
		t.Fatal("expected non-nil args handler")
	}

	result, err := got(context.Background(), nil, map[string]any{"input": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["parsed"] != true {
		t.Fatalf("unexpected result: got %v", result)
	}

	if got, ok := LookupArgs("scope", "Query.missing"); ok || got != nil {
		t.Fatalf("unexpected args handler for missing key: handler=%v ok=%v", got, ok)
	}
	if got, ok := LookupArgs("other-scope", "Query.name"); ok || got != nil {
		t.Fatalf("unexpected args handler for missing scope: handler=%v ok=%v", got, ok)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected duplicate registration panic")
		}
		msg, ok := recovered.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T", recovered)
		}
		expected := "duplicate args handler registration: scope:Query.name"
		if msg != expected {
			t.Fatalf("unexpected panic message: got %q want %q", msg, expected)
		}
	}()

	RegisterArgs("scope", "Query.name", h)
}

// fakeEC is a test-only implementation of ObjectExecutionContext.
type fakeEC struct {
	fieldContextHandlers map[string]FieldContextHandler
	invokeDirective      func(ctx context.Context, name string, obj any, next graphql.Resolver, args map[string]any) (any, error)
	schema               *ast.Schema
}

func (f *fakeEC) Schema() *ast.Schema { return f.schema }

// fakeECWithOpCtx embeds fakeEC and overrides GetOperationContext to return a
// non-nil OperationContext with empty Variables, satisfying the args path in
// buildFieldContext which calls ec.GetOperationContext().Variables.
type fakeECWithOpCtx struct {
	fakeEC
}

func (f *fakeECWithOpCtx) GetOperationContext() *graphql.OperationContext {
	return &graphql.OperationContext{
		Variables: map[string]any{},
		ResolverMiddleware: func(ctx context.Context, next graphql.Resolver) (res any, err error) {
			return next(ctx)
		},
	}
}

func (f *fakeEC) GetOperationContext() *graphql.OperationContext { return nil }
func (f *fakeEC) MarshalCodec(context.Context, string, ast.SelectionSet, any) graphql.Marshaler {
	return graphql.Null
}
func (f *fakeEC) UnmarshalCodec(context.Context, string, any) (any, error) { return nil, nil }
func (f *fakeEC) ParseFieldArgs(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, nil
}

func (f *fakeEC) ResolveField(
	context.Context,
	string,
	string,
	graphql.CollectedField,
	any,
) graphql.Marshaler {
	return graphql.Null
}

func (f *fakeEC) ResolveStreamField(
	context.Context,
	string,
	string,
	graphql.CollectedField,
	any,
) func(context.Context) graphql.Marshaler {
	return func(context.Context) graphql.Marshaler { return graphql.Null }
}

func (f *fakeEC) InvokeResolver(context.Context, string, string, any) (any, error) {
	return nil, nil
}

func (f *fakeEC) InvokeDirective(
	ctx context.Context,
	name string,
	obj any,
	next graphql.Resolver,
	args map[string]any,
) (any, error) {
	if f.invokeDirective != nil {
		return f.invokeDirective(ctx, name, obj, next, args)
	}
	return next(ctx)
}

func (f *fakeEC) FieldMiddleware(
	_ context.Context,
	_ any,
	next graphql.Resolver,
) graphql.Resolver {
	return next
}

func (f *fakeEC) LookupFieldContextHandler(obj, field string) (FieldContextHandler, bool) {
	h, ok := f.fieldContextHandlers[obj+"."+field]
	return h, ok
}
func (f *fakeEC) ProcessDeferredGroup(graphql.DeferredGroup) {}
func (f *fakeEC) AddDeferred(int32)                          {}
func (f *fakeEC) Error(context.Context, error)               {}
func (f *fakeEC) Recover(_ context.Context, r any) error     { return fmt.Errorf("%v", r) }

func TestMakeChildResolver_Scalar(t *testing.T) {
	ec := &fakeEC{}
	lookup := &ObjectChildLookup{TypeName: "UUID", Kind: ast.Scalar}

	childFn := makeChildResolver(ec, lookup)
	_, err := childFn(context.Background(), graphql.CollectedField{})
	if err == nil {
		t.Fatal("expected error for scalar Child resolution")
	}
	if got := err.Error(); got != "field of type UUID does not have child fields" {
		t.Fatalf("unexpected error: got %q", got)
	}
}

func TestMakeChildResolver_NonObjectComposite(t *testing.T) {
	ec := &fakeEC{}
	lookup := &ObjectChildLookup{TypeName: "MyInput", Kind: ast.InputObject}

	childFn := makeChildResolver(ec, lookup)
	_, err := childFn(context.Background(), graphql.CollectedField{})
	if err == nil {
		t.Fatal("expected error for input-object Child resolution")
	}
	if got := err.Error(); got != "FieldContext.Child cannot be called on type INPUT_OBJECT" {
		t.Fatalf("unexpected error: got %q", got)
	}
}

func TestMakeChildResolver_ObjectKnownField(t *testing.T) {
	ec := &fakeEC{
		fieldContextHandlers: map[string]FieldContextHandler{
			"Escrow.id": func(_ context.Context, _ ObjectExecutionContext, _ graphql.CollectedField) (*graphql.FieldContext, error) {
				return &graphql.FieldContext{
					Object: "Escrow",
					Field:  graphql.CollectedField{Field: &ast.Field{Name: "id"}},
				}, nil
			},
		},
	}
	lookup := &ObjectChildLookup{
		TypeName: "Escrow",
		Kind:     ast.Object,
		Children: []string{"id", "address"},
	}

	childFn := makeChildResolver(ec, lookup)
	fc, err := childFn(context.Background(), graphql.CollectedField{Field: &ast.Field{Name: "id"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fc == nil || fc.Object != "Escrow" {
		t.Fatalf("unexpected FieldContext: %+v", fc)
	}
}

func TestMakeChildResolver_ObjectUnknownField(t *testing.T) {
	ec := &fakeEC{}
	lookup := &ObjectChildLookup{TypeName: "Escrow", Kind: ast.Object, Children: []string{"id"}}

	childFn := makeChildResolver(ec, lookup)
	_, err := childFn(
		context.Background(),
		graphql.CollectedField{Field: &ast.Field{Name: "nonexistent"}},
	)
	if err == nil {
		t.Fatal("expected error for unknown child field")
	}
	expected := `no field named "nonexistent" was found under type Escrow`
	if got := err.Error(); got != expected {
		t.Fatalf("unexpected error: got %q want %q", got, expected)
	}
}

func TestMakeChildResolver_NilRet(t *testing.T) {
	ec := &fakeEC{}

	childFn := makeChildResolver(ec, nil)
	_, err := childFn(context.Background(), graphql.CollectedField{})
	if err == nil {
		t.Fatal("expected error for nil return-type lookup")
	}
	if got := err.Error(); got != "no return type information for field" {
		t.Fatalf("unexpected error: got %q", got)
	}
}

func TestMakeChildResolver_ObjectHandlerMissing(t *testing.T) {
	ec := &fakeEC{}
	lookup := &ObjectChildLookup{TypeName: "Escrow", Kind: ast.Object, Children: []string{"id"}}

	childFn := makeChildResolver(ec, lookup)
	_, err := childFn(context.Background(), graphql.CollectedField{Field: &ast.Field{Name: "id"}})
	if err == nil {
		t.Fatal("expected error when no FieldContext handler is registered")
	}
	if got := err.Error(); got != "no field context handler for Escrow.id" {
		t.Fatalf("unexpected error: got %q", got)
	}
}

func TestRegisterFieldDef_BasicRegistration(t *testing.T) {
	resetFieldRegistryForTest()
	resetFieldContextRegistryForTest()

	def := FieldDef{
		Resolve: func(ctx context.Context, _ ObjectExecutionContext, obj any) (any, error) {
			return "value", nil
		},
		ReturnType:   &ObjectChildLookup{TypeName: "String", Kind: ast.Scalar},
		MarshalCodec: "marshalNString",
		NonNull:      true,
		PanicHandled: true,
	}

	RegisterFieldDef("scope-x", "MyObj", "myField", def)

	if _, ok := LookupField("scope-x", "MyObj", "myField"); !ok {
		t.Fatal("expected RegisterFieldDef to register a FieldHandler")
	}
	if _, ok := LookupFieldContext("scope-x", "MyObj", "myField"); !ok {
		t.Fatal("expected RegisterFieldDef to register a FieldContextHandler")
	}
}

func TestBuildFieldContext_NoArgs(t *testing.T) {
	resetFieldRegistryForTest()
	resetFieldContextRegistryForTest()
	resetArgsRegistryForTest()

	ec := &fakeEC{
		fieldContextHandlers: map[string]FieldContextHandler{},
	}
	def := &FieldDef{
		IsMethod:   true,
		IsResolver: false,
		ReturnType: &ObjectChildLookup{TypeName: "String", Kind: ast.Scalar},
	}
	cf := graphql.CollectedField{Field: &ast.Field{Name: "id"}}

	fc, err := buildFieldContext(context.Background(), ec, def, "scope", "Escrow", cf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fc.Object != "Escrow" {
		t.Fatalf("unexpected Object: got %q want Escrow", fc.Object)
	}
	if fc.IsMethod != true || fc.IsResolver != false {
		t.Fatalf("unexpected flags: IsMethod=%v IsResolver=%v", fc.IsMethod, fc.IsResolver)
	}
	if fc.Child == nil {
		t.Fatal("expected non-nil Child resolver")
	}
}

func TestBuildFieldContext_ArgsPath(t *testing.T) {
	resetFieldRegistryForTest()
	resetFieldContextRegistryForTest()
	resetArgsRegistryForTest()

	RegisterArgs(
		"scope",
		"EscrowQueryArgs",
		func(_ context.Context, _ ObjectExecutionContext, raw map[string]any) (map[string]any, error) {
			return map[string]any{"id": raw["id"]}, nil
		},
	)

	ec := &fakeECWithOpCtx{}
	def := &FieldDef{
		IsMethod: true,
		ReturnType: &ObjectChildLookup{
			TypeName: "Escrow",
			Kind:     ast.Object,
			Children: []string{"id"},
		},
		ArgsKey: "EscrowQueryArgs",
	}
	// Build a synthetic field with a Definition so ArgumentMap can walk the arg defs.
	cf := graphql.CollectedField{
		Field: &ast.Field{
			Name: "escrow",
			Arguments: ast.ArgumentList{
				{Name: "id", Value: &ast.Value{Raw: "abc", Kind: ast.StringValue}},
			},
			Definition: &ast.FieldDefinition{
				Name: "escrow",
				Arguments: ast.ArgumentDefinitionList{
					{Name: "id"},
				},
			},
		},
	}

	fc, err := buildFieldContext(context.Background(), ec, def, "scope", "Query", cf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fc.Args["id"] != "abc" {
		t.Fatalf("unexpected args: %#v", fc.Args)
	}
}

func TestBuildFieldContext_ArgsPath_MissingArgsHandler(t *testing.T) {
	resetFieldRegistryForTest()
	resetFieldContextRegistryForTest()
	resetArgsRegistryForTest()

	ec := &fakeECWithOpCtx{}
	def := &FieldDef{
		ReturnType: &ObjectChildLookup{
			TypeName: "Escrow",
			Kind:     ast.Object,
			Children: []string{"id"},
		},
		ArgsKey: "MissingHandler",
	}
	cf := graphql.CollectedField{Field: &ast.Field{Name: "escrow"}}

	_, err := buildFieldContext(context.Background(), ec, def, "scope", "Query", cf)
	if err == nil {
		t.Fatal("expected error for missing args handler")
	}
	if err.Error() != `no args handler for "MissingHandler"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveFromDef_CallsResolve(t *testing.T) {
	resetFieldRegistryForTest()
	resetFieldContextRegistryForTest()
	resetArgsRegistryForTest()
	resetCodecMarshalRegistryForTest()

	called := false
	RegisterCodecMarshal(
		"scope",
		"marshalNString",
		func(_ context.Context, _ ObjectExecutionContext, _ ast.SelectionSet, v any) graphql.Marshaler {
			return graphql.MarshalString(v.(string))
		},
	)

	def := FieldDef{
		Resolve: func(_ context.Context, _ ObjectExecutionContext, obj any) (any, error) {
			called = true
			return obj.(string) + "_resolved", nil
		},
		ReturnType:   &ObjectChildLookup{TypeName: "String", Kind: ast.Scalar},
		MarshalCodec: "marshalNString",
		NonNull:      true,
		PanicHandled: true,
	}

	RegisterFieldDef("scope", "Query", "name", def)

	h, ok := LookupField("scope", "Query", "name")
	if !ok {
		t.Fatal("missing handler")
	}
	ec := &fakeECWithOpCtx{}
	cf := graphql.CollectedField{Field: &ast.Field{Name: "name"}}

	m := h(context.Background(), ec, cf, "hello")
	if m == graphql.Null {
		t.Fatal("expected non-null marshaler from resolveFromDef")
	}
	if !called {
		t.Fatal("Resolve was not invoked")
	}
}

func TestBuildFieldContext_ArgsPath_PanicRecovered(t *testing.T) {
	resetFieldRegistryForTest()
	resetFieldContextRegistryForTest()
	resetArgsRegistryForTest()

	RegisterArgs(
		"scope",
		"PanickingArgs",
		func(_ context.Context, _ ObjectExecutionContext, _ map[string]any) (map[string]any, error) {
			panic("boom")
		},
	)

	ec := &fakeECWithOpCtx{}
	def := &FieldDef{
		ReturnType: &ObjectChildLookup{
			TypeName: "Escrow",
			Kind:     ast.Object,
			Children: []string{"id"},
		},
		ArgsKey: "PanickingArgs",
	}
	cf := graphql.CollectedField{Field: &ast.Field{Name: "escrow"}}

	_, err := buildFieldContext(context.Background(), ec, def, "scope", "Query", cf)
	if err == nil {
		t.Fatal("expected error from recovered panic")
	}
	if err.Error() != "boom" {
		t.Fatalf("expected recovered panic message, got %v", err)
	}
}

func TestRegisterStreamFieldDef(t *testing.T) {
	resetFieldRegistryForTest()
	resetStreamFieldRegistryForTest()
	resetFieldContextRegistryForTest()
	resetCodecMarshalRegistryForTest()

	RegisterCodecMarshal(
		"scope",
		"marshalNChat",
		func(_ context.Context, _ ObjectExecutionContext, _ ast.SelectionSet, _ any) graphql.Marshaler {
			return graphql.MarshalString("chat")
		},
	)

	def := StreamFieldDef{
		Resolve: func(_ context.Context, _ ObjectExecutionContext, _ any) (any, error) {
			return make(<-chan string), nil
		},
		ReturnType: &ObjectChildLookup{
			TypeName: "Chat",
			Kind:     ast.Object,
			Children: []string{"id"},
		},
		MarshalCodec: "marshalNChat",
		NonNull:      true,
		PanicHandled: true,
	}
	RegisterStreamFieldDef("scope", "Subscription", "chat", def)

	if _, ok := LookupStreamField("scope", "Subscription", "chat"); !ok {
		t.Fatal("expected stream field handler registered")
	}
	if _, ok := LookupFieldContext("scope", "Subscription", "chat"); !ok {
		t.Fatal("expected field context handler registered (shared with non-streaming)")
	}
}

// TestResolveFromDef_DirectivesReceiveECAndObj pins the middleware signature:
// the shard-emitted Directives builder must receive the ObjectExecutionContext
// (its only route to the root package's directive implementations) and the
// parent object, and its chain must wrap the resolver.
func TestResolveFromDef_DirectivesReceiveECAndObj(t *testing.T) {
	resetFieldRegistryForTest()
	resetFieldContextRegistryForTest()
	resetArgsRegistryForTest()
	resetCodecMarshalRegistryForTest()

	RegisterCodecMarshal(
		"scope",
		"marshalNString",
		func(_ context.Context, _ ObjectExecutionContext, _ ast.SelectionSet, v any) graphql.Marshaler {
			return graphql.MarshalString(v.(string))
		},
	)

	var gotEC ObjectExecutionContext
	var gotObj any
	def := FieldDef{
		Resolve: func(_ context.Context, _ ObjectExecutionContext, obj any) (any, error) {
			return obj.(string) + "_resolved", nil
		},
		Directives: func(_ context.Context, ec ObjectExecutionContext, obj any, next graphql.Resolver) graphql.Resolver {
			gotEC, gotObj = ec, obj
			return func(ctx context.Context) (any, error) {
				res, err := next(ctx)
				if err != nil {
					return nil, err
				}
				return "wrapped_" + res.(string), nil
			}
		},
		ReturnType:   &ObjectChildLookup{TypeName: "String", Kind: ast.Scalar},
		MarshalCodec: "marshalNString",
		NonNull:      true,
		PanicHandled: true,
	}

	RegisterFieldDef("scope", "Query", "name", def)

	h, ok := LookupField("scope", "Query", "name")
	if !ok {
		t.Fatal("missing handler")
	}
	ec := &fakeECWithOpCtx{}
	cf := graphql.CollectedField{Field: &ast.Field{Name: "name"}}

	var buf bytes.Buffer
	h(context.Background(), ec, cf, "hello").MarshalGQL(&buf)

	if buf.String() != `"wrapped_hello_resolved"` {
		t.Fatalf("directive chain not applied: got %s", buf.String())
	}
	if gotEC != ObjectExecutionContext(ec) {
		t.Fatal("Directives did not receive the execution context")
	}
	if gotObj != "hello" {
		t.Fatalf("Directives did not receive the parent object: got %v", gotObj)
	}
}

// TestDirectiveChain_NilStaysNil keeps graphql.ResolveField's no-middleware
// fast path: a FieldDef without directives must pass a nil middlewareChain.
func TestDirectiveChain_NilStaysNil(t *testing.T) {
	if directiveChain(&fakeEC{}, nil, nil) != nil {
		t.Fatal("expected nil middleware chain for a FieldDef with no directives")
	}
	if directiveChain(
		&fakeEC{},
		nil,
		func(_ context.Context, _ ObjectExecutionContext, _ any, next graphql.Resolver) graphql.Resolver {
			return next
		},
	) == nil {
		t.Fatal("expected non-nil middleware chain when Directives is set")
	}
}

// TestAdaptStreamChannel_TypedChannel covers the type-erased stream path: the
// split runtime instantiates graphql.ResolveFieldStream with T = any, so a
// resolver's typed channel must be adapted to <-chan any.
func TestAdaptStreamChannel_TypedChannel(t *testing.T) {
	src := make(chan *string, 1)
	v := "ok"
	src <- &v
	close(src)

	out, err := adaptStreamChannel(context.Background(), (<-chan *string)(src), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ch, ok := out.(<-chan any)
	if !ok {
		t.Fatalf("expected <-chan any, got %T", out)
	}
	got := <-ch
	if p, ok := got.(*string); !ok || *p != "ok" {
		t.Fatalf("unexpected value from adapted channel: %#v", got)
	}
	if _, open := <-ch; open {
		t.Fatal("expected adapted channel to close with the source")
	}

	// Errors and nil results pass straight through.
	if res, err := adaptStreamChannel(context.Background(), nil, errFake); res != nil ||
		err != errFake {
		t.Fatalf("expected error passthrough, got %v %v", res, err)
	}
}

var errFake = errors.New("boom")

// TestAdaptStreamChannel_CancelledContext pins that the adapter goroutine is
// cancellable even when the source channel neither sends nor closes — the case
// a disconnected subscriber leaves behind.
func TestAdaptStreamChannel_CancelledContext(t *testing.T) {
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	src := make(chan *string) // never sends, never closes

	out, err := adaptStreamChannel(ctx, (<-chan *string)(src), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ch := out.(<-chan any)

	cancel()

	select {
	case _, open := <-ch:
		if open {
			t.Fatal("expected the adapted channel to close, not to yield a value")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adapted channel did not close after context cancellation")
	}

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Fatalf("adapter goroutine leaked: %d goroutines before, %d after", before, got)
	}
}
