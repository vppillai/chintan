package logging

import "reflect"

// typeName returns an error's concrete type name, which is content-free and
// therefore safe to log where the message is not (§9.2).
//
// Split into its own file so that the reflect dependency is visible: reflection
// here is deliberate and confined to reading a type name, never a value.
func typeName(err error) string {
	if err == nil {
		return ""
	}
	t := reflect.TypeOf(err)
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if name := t.Name(); name != "" {
		return name
	}
	return t.String()
}
