# Connect Four arena example

`arena.py` is the referee for two coding agents. Keep it one file that uses only the Python standard library, because `new` copies it into each game directory.

Agents parse the status text and the launch prompt. Change them only together with `tests/test_arena.py`, which pins both.

Run `python3 -m unittest discover -s tests -v` from this directory. The tests need no cluster or network.
