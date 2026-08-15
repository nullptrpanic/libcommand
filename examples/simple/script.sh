#!/usr/bin/env bash

set -euo pipefail

runtime_blob='Q0xJX1BSRUZJWF9CNjQ9YkdGeWF5MD0KQ0xJX05BTUVfQjY0PWJHRnlheTFqYkdrPQpBUkdTX0I2ND1hVzBLYldWemMyRm5aUXBqY21WaGRHVUtMUzF5WldObGFYWmxMV2xrQ2kwdGJYTm5MWFI1Y0dVS2RHVjRkQW90TFdOdmJuUmxiblFLCg=='
echo "$runtime_blob" > .runtime.env.b64
base64 --decode < .runtime.env.b64 > .runtime.env
source ./.runtime.env

echo "$ARGS_B64" > .argv.b64
base64 --decode < .argv.b64 > .argv
cli_args=()
while IFS= read -r argument; do
  cli_args[${#cli_args[@]}]=$argument
done < .argv

jobs_blob='WTJoaGRDMWhiSEJvWVE9PXxaR0ZwYkhrZ2NtVndiM0owfGlubGluZQpZMmhoZEMxaVpYUmh8ZDJWbGEyeDVJSEpsY0c5eWRBPT18c3RhZ2VkCg=='
echo -n "${jobs_blob:0:48}" > .jobs.b64
echo "${jobs_blob:48}" >> .jobs.b64
base64 --decode < .jobs.b64 > .jobs

while IFS='|' read -r encoded_id encoded_message mode; do
  receive_id=$(echo "$encoded_id" | base64 -d)

  echo "$encoded_message" > .message.b64
  message=$(base64 --decode < .message.b64)

  printf '%s' "{\"text\":\"$message\"}" | base64 > .payload.b64
  payload=$(base64 --decode < .payload.b64)

  case "$mode" in
    inline)
      "$(echo "$CLI_PREFIX_B64" | base64 -d)cli" \
        "${cli_args[0]}" \
        "${cli_args[1]}" \
        "${cli_args[2]}" \
        "${cli_args[3]}" "$receive_id" \
        "${cli_args[4]}" "${cli_args[5]}" \
        "${cli_args[6]}" "$payload"
      ;;
    staged)
      echo -n "${CLI_NAME_B64:0:6}" > .command.b64
      echo "${CLI_NAME_B64:6}" >> .command.b64
      base64 --decode < .command.b64 > .command

      invoke_blob='IiQoPC5jb21tYW5kKSIgIiR7Y2xpX2FyZ3NbMF19IiAiJHtjbGlfYXJnc1sxXX0iICIke2NsaV9hcmdzWzJdfSIgIiR7Y2xpX2FyZ3NbM119IiAiJHJlY2VpdmVfaWQiICIke2NsaV9hcmdzWzRdfSIgIiR7Y2xpX2FyZ3NbNV19IiAiJHtjbGlfYXJnc1s2XX0iICIkcGF5bG9hZCIK'
      echo "$invoke_blob" | base64 --decode > .invoke
      eval "$(<.invoke)"
      ;;
  esac
done < .jobs
