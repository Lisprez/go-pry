package pry

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
)

// Scope is a string-interface key-value pair that represents variables/functions in scope.
type Scope struct {
	Vals   map[string]interface{}
	Parent *Scope
}

// NewScope creates a new initialized scope
func NewScope() *Scope {
	return &Scope{
		map[string]interface{}{},
		nil,
	}
}

// Get walks the scope and finds the value of interest
func (s *Scope) Get(name string) (val interface{}, exists bool) {
	currentScope := s
	for !exists && currentScope != nil {
		val, exists = currentScope.Vals[name]
		currentScope = s.Parent
	}
	return
}

// Set walks the scope and sets a value in a parent scope if it exists, else current.
func (s *Scope) Set(name string, val interface{}) {
	exists := false
	currentScope := s
	for !exists && currentScope != nil {
		_, exists = currentScope.Vals[name]
		if exists {
			currentScope.Vals[name] = val
		}
		currentScope = s.Parent
	}
	if !exists {
		s.Vals[name] = val
	}
}

// Keys returns all keys in scope
func (s *Scope) Keys() (keys []string) {
	currentScope := s
	for currentScope != nil {
		for k := range currentScope.Vals {
			keys = append(keys, k)
		}
		currentScope = s.Parent
	}
	return
}

// Func represents an interpreted function definition.
type Func struct {
	Def *ast.FuncLit
}

// InterpretString interprets a string of go code and returns the result.
func InterpretString(scope *Scope, exprStr string) (interface{}, error) {

	// TODO: Mild hack, should just parse string with wrapper
	parts := strings.Split(exprStr, "=")
	if len(parts) == 2 && len(parts[0]) > 0 && len(parts[1]) > 0 {
		lhs := parts[0]
		rhs := parts[1]

		infer := lhs[len(lhs)-1:] == ":"
		if infer {
			lhs = lhs[:len(lhs)-1]
		}
		lhsExpr, err := parser.ParseExpr(lhs)

		// Ignore this error and fall back to standard parser
		if err == nil {
			lhsIdent, isIdent := lhsExpr.(*ast.Ident)
			if isIdent {
				prevVal, exists := scope.Get(lhsIdent.Name)
				// Enforce := and =
				if !exists && !infer {
					return nil, fmt.Errorf("Variable %#v is not defined.", lhsIdent.Name)
				} else if exists && infer {
					return nil, fmt.Errorf("Variable %#v is already defined.", lhsIdent.Name)
				}

				rhsExpr, err := parser.ParseExpr(rhs)
				if err != nil {
					return nil, err
				}
				val, err := InterpretExpr(scope, rhsExpr)
				if err != nil {
					return nil, err
				}

				// Enforce types
				if exists && reflect.TypeOf(prevVal) != reflect.TypeOf(val) {
					return nil, fmt.Errorf("Error %#v is of type %T not %T.", lhsIdent.Name, prevVal, val)
				}
				// TODO walk scope
				scope.Vals[lhsIdent.Name] = val
				return val, nil
			}
		}
	}
	expr, err := parser.ParseExpr(exprStr)
	if err != nil {
		return nil, err
	}
	return InterpretExpr(scope, expr)
}

// InterpretExpr interprets an ast.Expr and returns the value.
func InterpretExpr(scope *Scope, expr ast.Expr) (interface{}, error) {
	builtinScope := map[string]interface{}{
		"nil":    nil,
		"true":   true,
		"false":  false,
		"append": Append,
		"make":   Make,
	}

	switch e := expr.(type) {
	case *ast.Ident:

		typ, err := StringToType(e.Name)
		if err == nil {
			return typ, err
		}

		obj, exists := scope.Get(e.Name)
		if !exists {
			// TODO make builtinScope root of other scopes
			obj, exists = builtinScope[e.Name]
			if !exists {
				return nil, errors.New(fmt.Sprint("Can't find EXPR ", e.Name))
			}
		}
		return obj, nil

	case *ast.SelectorExpr:
		X, err := InterpretExpr(scope, e.X)
		if err != nil {
			return nil, err
		}
		sel := e.Sel

		rVal := reflect.ValueOf(X)
		if rVal.Kind() != reflect.Struct {
			return nil, fmt.Errorf("%#v is not a struct and thus has no field %#v", X, sel.Name)
		}

		pkg, isPackage := X.(Package)
		if isPackage {
			obj, isPresent := pkg.Functions[sel.Name]
			if isPresent {
				return obj, nil
			}
			return nil, fmt.Errorf("Unknown field %#v", sel.Name)
		}

		zero := reflect.ValueOf(nil)
		field := rVal.FieldByName(sel.Name)
		if field != zero {
			return field.Interface(), nil
		}
		method := rVal.MethodByName(sel.Name)
		if method != zero {
			return method.Interface(), nil
		}
		return nil, fmt.Errorf("Unknown field %#v", sel.Name)

	case *ast.CallExpr:
		fun, err := InterpretExpr(scope, e.Fun)
		if err != nil {
			return nil, err
		}

		args := make([]reflect.Value, len(e.Args))
		for i, arg := range e.Args {
			interpretedArg, err := InterpretExpr(scope, arg)
			if err != nil {
				return nil, err
			}
			args[i] = reflect.ValueOf(interpretedArg)
		}

		switch funV := fun.(type) {
		case reflect.Type:
			return args[0].Convert(funV).Interface(), nil
		case *Func:
			// TODO enforce func return values
			return InterpretStmt(scope, funV.Def.Body)
		}

		funVal := reflect.ValueOf(fun)

		values := ValuesToInterfaces(funVal.Call(args))
		if len(values) == 0 {
			return nil, nil
		} else if len(values) == 1 {
			return values[0], nil
		}
		err, _ = values[1].(error)
		return values[0], err

	case *ast.BasicLit:
		switch e.Kind {
		case token.INT:
			return strconv.Atoi(e.Value)
		case token.FLOAT, token.IMAG:
			return strconv.ParseFloat(e.Value, 64)
		case token.CHAR:
			return (rune)(e.Value[1]), nil
		case token.STRING:
			return e.Value[1 : len(e.Value)-1], nil
		default:
			return nil, fmt.Errorf("Unknown basic literal %d", e.Kind)
		}

	case *ast.CompositeLit:
		typ, err := InterpretExpr(scope, e.Type)
		if err != nil {
			return nil, err
		}

		switch t := e.Type.(type) {
		case *ast.ArrayType:
			l := len(e.Elts)
			slice := reflect.MakeSlice(typ.(reflect.Type), l, l)
			for i, elem := range e.Elts {
				elemValue, err := InterpretExpr(scope, elem)
				if err != nil {
					return nil, err
				}
				slice.Index(i).Set(reflect.ValueOf(elemValue))
			}
			return slice.Interface(), nil

		case *ast.MapType:
			nMap := reflect.MakeMap(typ.(reflect.Type))
			for _, elem := range e.Elts {
				switch eT := elem.(type) {
				case *ast.KeyValueExpr:
					key, err := InterpretExpr(scope, eT.Key)
					if err != nil {
						return nil, err
					}
					val, err := InterpretExpr(scope, eT.Value)
					if err != nil {
						return nil, err
					}
					nMap.SetMapIndex(reflect.ValueOf(key), reflect.ValueOf(val))

				default:
					return nil, fmt.Errorf("Invalid element type %#v to map. Expecting key value pair.", eT)
				}
			}
			return nMap.Interface(), nil

		default:
			return nil, fmt.Errorf("Unknown composite literal %#v", t)
		}

	case *ast.BinaryExpr:
		x, err := InterpretExpr(scope, e.X)
		if err != nil {
			return nil, err
		}
		y, err := InterpretExpr(scope, e.Y)
		if err != nil {
			return nil, err
		}
		return ComputeBinaryOp(x, y, e.Op)

	case *ast.UnaryExpr:
		x, err := InterpretExpr(scope, e.X)
		if err != nil {
			return nil, err
		}
		return ComputeUnaryOp(x, e.Op)

	case *ast.ArrayType:
		typ, err := InterpretExpr(scope, e.Elt)
		if err != nil {
			return nil, err
		}
		arrType := reflect.SliceOf(typ.(reflect.Type))
		return arrType, nil

	case *ast.MapType:
		keyType, err := InterpretExpr(scope, e.Key)
		if err != nil {
			return nil, err
		}
		valType, err := InterpretExpr(scope, e.Value)
		if err != nil {
			return nil, err
		}
		mapType := reflect.MapOf(keyType.(reflect.Type), valType.(reflect.Type))
		return mapType, nil

	case *ast.ChanType:
		typeI, err := InterpretExpr(scope, e.Value)
		if err != nil {
			return nil, err
		}
		typ, isType := typeI.(reflect.Type)
		if !isType {
			return nil, fmt.Errorf("chan needs to be passed a type not %T", typ)
		}
		return reflect.ChanOf(reflect.BothDir, typ), nil

	case *ast.IndexExpr:
		X, err := InterpretExpr(scope, e.X)
		if err != nil {
			return nil, err
		}
		i, err := InterpretExpr(scope, e.Index)
		if err != nil {
			return nil, err
		}
		xVal := reflect.ValueOf(X)
		if reflect.TypeOf(X).Kind() == reflect.Map {
			val := xVal.MapIndex(reflect.ValueOf(i))
			if !val.IsValid() {
				// If not valid key, return the "zero" type. Eg for int 0, string ""
				return reflect.Zero(xVal.Type().Elem()).Interface(), nil
			}
			return val.Interface(), nil
		}

		iVal, isInt := i.(int)
		if !isInt {
			return nil, fmt.Errorf("Index has to be an int not %T", i)
		}
		if iVal >= xVal.Len() || iVal < 0 {
			return nil, errors.New("slice index out of range")
		}
		return xVal.Index(iVal).Interface(), nil
	case *ast.SliceExpr:
		low, err := InterpretExpr(scope, e.Low)
		if err != nil {
			return nil, err
		}
		high, err := InterpretExpr(scope, e.High)
		if err != nil {
			return nil, err
		}
		X, err := InterpretExpr(scope, e.X)
		if err != nil {
			return nil, err
		}
		xVal := reflect.ValueOf(X)
		if low == nil {
			low = 0
		}
		if high == nil {
			high = xVal.Len()
		}
		lowVal, isLowInt := low.(int)
		highVal, isHighInt := high.(int)
		if !isLowInt || !isHighInt {
			return nil, fmt.Errorf("slice: indexes have to be an ints not %T and %T", low, high)
		}
		if lowVal < 0 || highVal >= xVal.Len() {
			return nil, errors.New("slice: index out of bounds")
		}
		return xVal.Slice(lowVal, highVal).Interface(), nil

	case *ast.ParenExpr:
		return InterpretExpr(scope, e.X)

	case *ast.FuncLit:
		return &Func{e}, nil

	default:
		return nil, fmt.Errorf("Unknown EXPR %T", e)
	}
}

// InterpretStmt interprets an ast.Stmt and returns the value.
func InterpretStmt(scope *Scope, stmt ast.Stmt) (interface{}, error) {
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		var outFinal interface{}
		for _, stmts := range s.List {
			out, err := InterpretStmt(scope, stmts)
			if err != nil {
				return out, err
			}
			outFinal = out
		}
		return outFinal, nil
	case *ast.ReturnStmt:
		results := make([]interface{}, len(s.Results))
		for i, result := range s.Results {
			out, err := InterpretExpr(scope, result)
			if err != nil {
				return out, err
			}
			results[i] = out
		}

		if len(results) == 0 {
			return nil, nil
		} else if len(results) == 1 {
			return results[0], nil
		}
		return results, nil

	case *ast.ExprStmt:
		return InterpretExpr(scope, s.X)
	default:
		return nil, fmt.Errorf("Unknown STMT %#v", s)
	}
}

// StringToType returns the reflect.Type corresponding to the type string provided. Ex: StringToType("int")
func StringToType(str string) (reflect.Type, error) {
	types := map[string]reflect.Type{
		"bool":       reflect.TypeOf(true),
		"byte":       reflect.TypeOf(byte(0)),
		"rune":       reflect.TypeOf(rune(0)),
		"string":     reflect.TypeOf(""),
		"int":        reflect.TypeOf(int(0)),
		"int8":       reflect.TypeOf(int8(0)),
		"int16":      reflect.TypeOf(int16(0)),
		"int32":      reflect.TypeOf(int32(0)),
		"int64":      reflect.TypeOf(int64(0)),
		"uint":       reflect.TypeOf(uint(0)),
		"uint8":      reflect.TypeOf(uint8(0)),
		"uint16":     reflect.TypeOf(uint16(0)),
		"uint32":     reflect.TypeOf(uint32(0)),
		"uint64":     reflect.TypeOf(uint64(0)),
		"uintptr":    reflect.TypeOf(uintptr(0)),
		"float32":    reflect.TypeOf(float32(0)),
		"float64":    reflect.TypeOf(float64(0)),
		"complex64":  reflect.TypeOf(complex64(0)),
		"complex128": reflect.TypeOf(complex128(0)),
		"error":      reflect.TypeOf(errors.New("")),
	}
	val, present := types[str]
	if !present {
		return nil, fmt.Errorf("Error type %#v is not in table.", str)
	}
	return val, nil
}

// ValuesToInterfaces converts a slice of []reflect.Value to []interface{}
func ValuesToInterfaces(vals []reflect.Value) []interface{} {
	inters := make([]interface{}, len(vals))
	for i, val := range vals {
		inters[i] = val.Interface()
	}
	return inters
}
