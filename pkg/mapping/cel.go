/*
Copyright 2026 Kartikey Gupta.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mapping

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
)

var (
	celOnce sync.Once
	celEnv  *cel.Env
	celErr  error
)

func env() (*cel.Env, error) {
	celOnce.Do(func() {
		celEnv, celErr = cel.NewEnv(
			cel.Variable("source", cel.MapType(cel.StringType, cel.DynType)),
			cel.Variable("item", cel.DynType),
			cel.OptionalTypes(),
		)
	})
	return celEnv, celErr
}

func evalCEL(expr string, source map[string]any, item any) (any, error) {
	e, err := env()
	if err != nil {
		return nil, err
	}
	ast, iss := e.Compile(expr)
	if iss.Err() != nil {
		return nil, fmt.Errorf("cel compile %q: %w", expr, iss.Err())
	}
	prg, err := e.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("cel program %q: %w", expr, err)
	}
	if item == nil {
		item = map[string]any{}
	}
	out, _, err := prg.Eval(map[string]any{
		"source": source,
		"item":   item,
	})
	if err != nil {
		return nil, fmt.Errorf("cel eval %q: %w", expr, err)
	}
	return out.Value(), nil
}

func evalBool(expr string, source map[string]any, item any) (bool, error) {
	if expr == "" {
		return true, nil
	}
	v, err := evalCEL(expr, source, item)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("cel %q: expected bool, got %T", expr, v)
	}
	return b, nil
}
