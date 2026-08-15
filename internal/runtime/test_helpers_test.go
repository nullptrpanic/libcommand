package runtime

import "testing"

func argumentStrings(t testing.TB, invocation *Invocation) []string {
	t.Helper()
	args := invocation.Args
	if args == nil {
		return nil
	}
	values := make([]string, len(args))
	for index, argument := range args {
		if argument.Kind != ArgumentString {
			t.Fatalf("argument %d is unresolved: %#v", index, argument)
		}
		values[index] = argument.Value
	}
	return values
}
