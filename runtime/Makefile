# Builds the TLang C runtime and its unit tests (GNU make).
#
#   make -C runtime [all|test|clean] [CC=gcc|clang|tcc] [SAN=1] [RELEASE=1] [PG=0]
#
# Every src/*.c goes into build/<variant>/libtlangrt.a and every tests/test_*.c
# becomes its own executable build/<variant>/tests/test_*, linked with that
# library. <variant> is the compiler name plus -san, -rel and -nopg suffixes, so
# configurations never share objects. src/ may hold no .c file yet: the library
# is then an empty archive.
#
#   CC=...     compiler: gcc (default), clang or tcc
#   SAN=1      AddressSanitizer + UndefinedBehaviorSanitizer, gcc/clang only
#   RELEASE=1  -O3 -fwrapv -DNDEBUG
#   PG=0       no libpq: -DTLANG_NO_PG, tests/test_pg_*.c are not built
#
# tests/test_pg_*.c only run when TLANG_TEST_DATABASE_URL is set (a libpq
# connection string for a disposable database). CFLAGS, CPPFLAGS, LDFLAGS and
# LDLIBS given on the command line are appended to the runtime's own flags.
# `make all` also compiles src/tlang_internal.h on its own with the strict flags.

# make's built-in default for CC is cc; the runtime defaults to gcc instead.
ifeq ($(origin CC),default)
CC := gcc
endif

SAN ?= 0
RELEASE ?= 0
PG ?= 1

CC_NAME := $(notdir $(firstword $(CC)))
ifneq ($(findstring tcc,$(CC_NAME)),)
CC_KIND := tcc
else ifneq ($(findstring clang,$(CC_NAME)),)
CC_KIND := clang
else
CC_KIND := gcc
endif

VARIANT := $(CC_NAME)$(if $(filter 1,$(SAN)),-san)$(if $(filter 1,$(RELEASE)),-rel)$(if $(filter 0,$(PG)),-nopg)
BUILD := build/$(VARIANT)

RT_CPPFLAGS := -D_GNU_SOURCE -Iinclude -Isrc
RT_CFLAGS := -std=c11 -fwrapv -Wall
RT_LDLIBS := -lpthread -lm

ifneq ($(CC_KIND),tcc)
RT_CFLAGS += -Wextra
endif

ifeq ($(RELEASE),1)
RT_CFLAGS += -O3
RT_CPPFLAGS += -DNDEBUG
else ifeq ($(SAN),1)
RT_CFLAGS += -O1 -g
else
RT_CFLAGS += -O2 -g
endif

ifeq ($(SAN),1)
ifeq ($(CC_KIND),tcc)
$(error SAN=1 needs gcc or clang)
endif
RT_CFLAGS += -fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer -g
endif

ifeq ($(PG),0)
RT_CPPFLAGS += -DTLANG_NO_PG
else
PG_INCDIR := $(shell pg_config --includedir 2>/dev/null)
ifeq ($(PG_INCDIR),)
PG_INCDIR := /usr/include/postgresql
endif
RT_CPPFLAGS += -I$(PG_INCDIR)
RT_LDLIBS := -lpq $(RT_LDLIBS)
endif

# Extra flags for the self-containment checks of the headers.
ifeq ($(CC_KIND),tcc)
STRICT := -Werror
else
STRICT := -pedantic -Werror
endif

SRCS := $(sort $(wildcard src/*.c))
OBJS := $(SRCS:src/%.c=$(BUILD)/obj/%.o)
HDRS := $(wildcard include/*.h src/*.h)
LIB := $(BUILD)/libtlangrt.a

ALL_TESTS := $(sort $(wildcard tests/test_*.c))
ifeq ($(PG),0)
TEST_SRCS := $(filter-out tests/test_pg_%.c,$(ALL_TESTS))
SKIPPED_PG := $(filter tests/test_pg_%.c,$(ALL_TESTS))
else
TEST_SRCS := $(ALL_TESTS)
SKIPPED_PG :=
endif
TEST_BINS := $(TEST_SRCS:tests/%.c=$(BUILD)/tests/%)

CHECK_SRC := $(BUILD)/check/tlang_internal_h.c
CHECK_OBJ := $(BUILD)/check/tlang_internal_h.o

.PHONY: all test clean
.DELETE_ON_ERROR:

all: $(LIB) $(TEST_BINS) $(CHECK_OBJ)

$(BUILD) $(BUILD)/obj $(BUILD)/tests $(BUILD)/check:
	mkdir -p $@

# Coarse dependencies: every object depends on every header (works with tcc,
# which has no reliable -MMD).
$(BUILD)/obj/%.o: src/%.c $(HDRS) | $(BUILD)/obj
	$(CC) $(RT_CPPFLAGS) $(CPPFLAGS) $(RT_CFLAGS) $(CFLAGS) -c -o $@ $<

$(LIB): $(OBJS) | $(BUILD)
	rm -f $@
	$(AR) rcs $@ $(OBJS)

$(BUILD)/tests/%: tests/%.c $(LIB) $(HDRS) | $(BUILD)/tests
	$(CC) $(RT_CPPFLAGS) $(CPPFLAGS) $(RT_CFLAGS) $(CFLAGS) $(TEST_CFLAGS) -o $@ $< $(LIB) $(LDFLAGS) $(RT_LDLIBS) $(LDLIBS)

# tlang.h must compile on its own under the strictest flags.
$(BUILD)/tests/test_header: TEST_CFLAGS := $(STRICT)

# tlang_internal.h must compile on its own as the first include.
$(CHECK_OBJ): $(HDRS) | $(BUILD)/check
	printf '#include "tlang_internal.h"\nint tlang_internal_h_check;\n' > $(CHECK_SRC)
	$(CC) $(RT_CPPFLAGS) $(CPPFLAGS) $(RT_CFLAGS) $(CFLAGS) $(STRICT) -c -o $@ $(CHECK_SRC)

test: all
	@fail=0; \
	for s in $(SKIPPED_PG); do s=$${s##*/}; echo "SKIP $${s%.c} (PG=0)"; done; \
	for t in $(TEST_BINS); do \
	    n=$${t##*/}; \
	    case $$n in \
	    test_pg_*) \
	        if [ -z "$$TLANG_TEST_DATABASE_URL" ]; then \
	            echo "SKIP $$n (TLANG_TEST_DATABASE_URL unset)"; continue; \
	        fi ;; \
	    esac; \
	    if ASAN_OPTIONS="$${ASAN_OPTIONS:-detect_leaks=1}" \
	       UBSAN_OPTIONS="$${UBSAN_OPTIONS:-print_stacktrace=1}" "./$$t"; then \
	        echo "ok   $$n ($(VARIANT))"; \
	    else \
	        echo "FAIL $$n ($(VARIANT))"; fail=1; \
	    fi; \
	done; \
	exit $$fail

clean:
	rm -rf build
