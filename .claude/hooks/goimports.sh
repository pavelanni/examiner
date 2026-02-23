#!/bin/bash
# Run goimports on .go files after Edit/Write tool use.

FILE_PATH=$(jq -r '.tool_input.file_path // empty')

if [[ "$FILE_PATH" == *.go ]]; then
  goimports -w "$FILE_PATH"
fi

exit 0
