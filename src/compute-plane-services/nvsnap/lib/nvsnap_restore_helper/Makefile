CC ?= cc
CFLAGS ?= -O2 -Wall -Wextra -static
BIN = nvsnap-restore-helper

$(BIN): main.c
	$(CC) $(CFLAGS) -o $@ main.c

clean:
	rm -f $(BIN)

.PHONY: clean
