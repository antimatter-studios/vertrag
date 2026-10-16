# Deprecated. The tasks live in chores.yml and run with chore
# (https://github.com/antimatter-studios/chore):
#
#     brew install antimatter-studios/tap/chore
#     chore            # list the tasks
#     chore cover      # run one
#
# This file only forwards each old target to the chore task of the same name,
# so `make cover` keeps working while habits move over. It holds no logic of
# its own, so the two cannot drift, and it will be removed.

TARGETS := build test cover oracle oracle-deps property fmt clean

.PHONY: help $(TARGETS)

# Expanded only when a target runs, so a missing chore fails with the install
# command rather than `chore: command not found`.
CHORE = $(or $(shell command -v chore),$(error chore is not installed: brew install antimatter-studios/tap/chore))

DEPRECATED = echo "make is deprecated in this repository; the tasks are in chores.yml. Run: $(1)" >&2

# `chore help` is chore's own manual, not this project's task list.
help:
	@$(call DEPRECATED,chore)
	@$(CHORE) --list

# COVERAGE_MINIMUM is passed through so `make cover COVERAGE_MINIMUM=60` and
# the same variable in the environment still lower the floor for one run.
$(TARGETS):
	@$(call DEPRECATED,chore $@)
	@$(CHORE) $@ $(if $(COVERAGE_MINIMUM),COVERAGE_MINIMUM=$(COVERAGE_MINIMUM))
