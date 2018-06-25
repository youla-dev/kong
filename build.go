package kong

import (
	"fmt"
	"reflect"
	"strings"
)

func build(k *Kong, ast interface{}) (app *Application, err error) {
	defer catch(&err)
	v := reflect.ValueOf(ast)
	iv := reflect.Indirect(v)
	if v.Kind() != reflect.Ptr || iv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected a pointer to a struct but got %T", ast)
	}

	app = &Application{}
	extraFlags := k.extraFlags()
	seenFlags := map[string]bool{}
	for _, flag := range extraFlags {
		seenFlags[flag.Name] = true
	}
	node := buildNode(k, iv, ApplicationNode, seenFlags)
	if len(node.Positional) > 0 && len(node.Children) > 0 {
		return nil, fmt.Errorf("can't mix positional arguments and branching arguments on %T", ast)
	}
	app.Node = node
	app.Node.Flags = append(extraFlags, app.Node.Flags...)
	return app, nil
}

func dashedString(s string) string {
	return strings.Join(camelCase(s), "-")
}

type flattenedField struct {
	field reflect.StructField
	value reflect.Value
}

func flattenedFields(v reflect.Value) (out []flattenedField) {
	for i := 0; i < v.NumField(); i++ {
		ft := v.Type().Field(i)
		fv := v.Field(i)
		if ft.Anonymous {
			out = append(out, flattenedFields(fv)...)
			continue
		}
		if !fv.CanSet() {
			continue
		}
		out = append(out, flattenedField{field: ft, value: fv})
	}
	return
}

func buildNode(k *Kong, v reflect.Value, typ NodeType, seenFlags map[string]bool) *Node {
	node := &Node{
		Type:   typ,
		Target: v,
	}
	for _, field := range flattenedFields(v) {
		ft := field.field
		fv := field.value

		tag := parseTag(fv, ft)

		name := tag.Name
		if name == "" {
			name = strings.ToLower(dashedString(ft.Name))
		}

		// Nested structs are either commands or args.
		if ft.Type.Kind() == reflect.Struct && (tag.Cmd || tag.Arg) {
			typ := CommandNode
			if tag.Arg {
				typ = ArgumentNode
			}
			buildChild(k, node, typ, v, ft, fv, tag, name, seenFlags)
		} else {
			buildField(k, node, v, ft, fv, tag, name, seenFlags)
		}
	}

	// "Unsee" flags.
	for _, flag := range node.Flags {
		delete(seenFlags, flag.Name)
	}

	// Scan through argument positionals to ensure optional is never before a required.
	last := true
	for i, p := range node.Positional {
		if !last && p.Required {
			fail("argument %q can not be required after an optional", p.Name)
		}

		last = p.Required
		p.Position = i
	}

	return node
}

func buildChild(k *Kong, node *Node, typ NodeType, v reflect.Value, ft reflect.StructField, fv reflect.Value, tag *Tag, name string, seenFlags map[string]bool) {
	child := buildNode(k, fv, typ, seenFlags)
	child.Parent = node
	child.Help = tag.Help
	child.Hidden = tag.Hidden

	// A branching argument. This is a bit hairy, as we let buildNode() do the parsing, then check that
	// a positional argument is provided to the child, and move it to the branching argument field.
	if tag.Arg {
		if len(child.Positional) == 0 {
			fail("positional branch %s.%s must have at least one child positional argument named %q",
				v.Type().Name(), ft.Name, name)
		}

		value := child.Positional[0]
		child.Positional = child.Positional[1:]
		if child.Help == "" {
			child.Help = value.Help
		}

		child.Name = value.Name
		if child.Name != name {
			fail("first field in positional branch %s.%s must have the same name as the parent field (%s).",
				v.Type().Name(), ft.Name, child.Name)
		}

		child.Argument = value
	} else {
		child.Name = name
	}
	node.Children = append(node.Children, child)

	if len(child.Positional) > 0 && len(child.Children) > 0 {
		fail("can't mix positional arguments and branching arguments on %s.%s", v.Type().Name(), ft.Name)
	}
}

func buildField(k *Kong, node *Node, v reflect.Value, ft reflect.StructField, fv reflect.Value, tag *Tag, name string, seenFlags map[string]bool) {
	mapper := k.registry.ForNamedValue(tag.Type, fv)
	if mapper == nil {
		fail("unsupported field type %s.%s (of type %s)", v.Type(), ft.Name, ft.Type)
	}

	value := &Value{
		Name:    name,
		Help:    tag.Help,
		Default: tag.Default,
		Mapper:  mapper,
		Tag:     tag,
		Target:  fv,

		// Flags are optional by default, and args are required by default.
		Required: (!tag.Arg && tag.Required) || (tag.Arg && !tag.Optional),
		Format:   tag.Format,
	}

	if tag.Arg {
		node.Positional = append(node.Positional, value)
	} else {
		if seenFlags[value.Name] {
			fail("duplicate flag --%s", value.Name)
		}
		seenFlags[value.Name] = true
		flag := &Flag{
			Value:       value,
			Short:       tag.Short,
			PlaceHolder: tag.PlaceHolder,
			Env:         tag.Env,
		}
		value.Flag = flag
		node.Flags = append(node.Flags, flag)
	}
}
