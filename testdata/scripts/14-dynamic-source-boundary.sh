#!/usr/bin/env bash
# Boundary target: known generated source followed by eval of unresolved source.
# Commands hidden inside the unresolved eval remain opaque, while the final
# handler remains conservatively reachable because the unknown source may return.

printf '%s\n' 'lark-cli dynamic known-source --mode "$MODE"' > generated.sh
MODE=virtual source generated.sh

snippet=$(remote-script --language bash)
eval "$snippet"

lark-cli dynamic unreachable
