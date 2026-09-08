package libcommand

import "testing"

func TestVirtualDescriptorLifetimeAndVisibility(t *testing.T) {
	for _, test := range []*struct{ name, source, want string }{
		{"block writes visible", `{ printf first; cat out; } > out; lark-cli "$(cat out)"`, "firstfirst"},
		{"persistent stdout", `exec > out; echo data; lark-cli "$(cat out)"`, "data"},
		{"temporary override", `exec > out; { printf a; printf b > other; printf c; }; lark-cli "$(cat out):$(cat other)"`, "ac:b"},
		{"numeric output", `exec 3> out; echo one >&3; echo two >&3; lark-cli "$(cat out)"`, "one\ntwo"},
		{"numeric input", `printf 'a\nb\n' > in; exec 3< in; read x <&3; read y <&3; lark-cli "$x:$y"`, "a:b"},
		{"select advances aliased input", `printf '1\nafter\n' > in; exec 3<in; select choice in first; do break; done <&3; read remaining <&3; lark-cli "$choice:$remaining"`, "first:after"},
		{"substitution capture", `exec > out; x=$(printf inner); printf '%s' "$x"; lark-cli "$(cat out)"`, "inner"},
		{"pipeline capture", `exec > out; printf inner | cat; lark-cli "$(cat out)"`, "inner"},
		{"subshell visibility", `exec > out; (printf inner; lark-cli "$(cat out)")`, "inner"},
		{"child shell visibility", `exec > out; bash -c 'printf inner; lark-cli "$(cat out)"'`, "inner"},
		{"wrapper persists", `command exec 3> out; printf value >&3; lark-cli "$(cat out)"`, "value"},
		{"exec without redirs does not retain block redirs", `{ exec; printf first; } > out; printf second; lark-cli "$(cat out)"`, "first"},
		{"block restores its fd only", `{ exec 3> other; exec > inner; printf value; } > outer; printf more >&3; lark-cli "$(cat outer):$(cat inner):$(cat other)"`, ":value:more"},
		{"append", `printf before > out; exec >> out; printf one; printf two; lark-cli "$(cat out)"`, "beforeonetwo"},
		{"truncation order", `echo old > out; { read value < out > out; lark-cli "<$value>"; }`, "<>"},
		{"child input cursor", `printf 'a\nb\n' > in; exec 3<in; (read first <&3); read second <&3; lark-cli "$second"`, "b"},
		{"child input binding is isolated", `printf 'a\nb\n' > in; exec <in; (exec </dev/null); read first; lark-cli "$first"`, "a"},
		{"sibling writes are isolated", `exec >out; if missing >/dev/null 2>&1; then printf yes; lark-cli "$(cat out)"; else printf no; lark-cli "$(cat out)"; fi`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := []string{test.want}
			if test.name == "sibling writes are isolated" {
				want = []string{"yes", "no"}
			}
			requireFirstArguments(t, test.source, want)
		})
	}
}
