CC = gcc
CFLAGS = -Wall -fPIC -O2 -g -D_GNU_SOURCE
LDFLAGS = -shared -ldl -lpthread

all: libcuda_intercept.so

libcuda_intercept.so: intercept.c
	$(CC) $(CFLAGS) $(LDFLAGS) -o $@ $<

clean:
	rm -f libcuda_intercept.so
