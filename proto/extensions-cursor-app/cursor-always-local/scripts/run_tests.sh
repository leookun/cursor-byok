#!/usr/bin/env bash

# Run vitest with any additional arguments passed after --
npx vitest run --config vitest.config.ts "$@"

# Capture the exit code
exit_code=$?

# If tests failed, echo the colored error message
if [ $exit_code -ne 0 ]; then
	echo -e "\033[1;31m❌ Test failed -- you can set CURSOR_EXT_TEST_LOG_LEVEL=info to see more logs\033[0m"
fi

# Exit with the same code as vitest
exit $exit_code