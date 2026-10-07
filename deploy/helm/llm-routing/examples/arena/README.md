# Connect Four arena

Two coding agents play Connect Four against each other through a referee CLI, and can send each other short messages. Both agents use the same model through the LLM gateway, so the demo shows two different agents sharing one deployment.

`arena.py` holds the board and enforces the rules. Each agent calls it through its shell tool: `wait` blocks without using the model until it is that agent's turn, `move` drops a piece, and `say` sends a message. `watch` shows the game to the audience. The referee design follows the arbiter and `wait_for_turn` pattern of [llm-chess](https://github.com/kmount44/llm-chess).

## Requirements

- A model with at least two slots of 16,384 tokens or more. The GB300 defaults (two 65,536-token slots) work; see [Server tuning](../../README.md#server-tuning).
- Pi and Codex set up as in [Connect a coding agent](../../README.md#connect-a-coding-agent), with the gateway port-forward running and `WORK` and `GLM_API_KEY` set in each agent terminal.
- Python 3 on the machine running the agents.

## Run a game

From `deploy/helm/llm-routing`, start a game in a directory outside the checkout. Agents started inside the checkout would load its AGENTS.md files.

```bash
python3 examples/arena/arena.py new ~/connect-four --x pi --o codex
```

`new` copies `arena.py` into the directory and prints each player's prompt. Use three terminals.

Spectators:

```bash
cd ~/connect-four && python3 arena.py watch
```

Pi plays X:

```bash
cd ~/connect-four
export ARENA_PLAYER=x
NODE_EXTRA_CA_CERTS="$WORK/ca.crt" pi --no-skills "$(python3 arena.py prompt)"
```

Codex plays O. Approvals are off so it does not stop mid-game:

```bash
cd ~/connect-four
export ARENA_PLAYER=o
CODEX_CA_CERTIFICATE="$WORK/ca.crt" codex --profile glm --sandbox workspace-write --ask-for-approval never "$(python3 arena.py prompt)"
```

The audience sees the board, whose turn it is, each player's average seconds per move, illegal attempts and the last eight messages. A game takes up to 42 moves, about 5 to 10 minutes on GB300.

To run without the agents' terminal interfaces, close stdin as the coding agent section describes:

```bash
ARENA_PLAYER=x NODE_EXTRA_CA_CERTS="$WORK/ca.crt" pi --no-skills -p "$(ARENA_PLAYER=x python3 arena.py prompt)" < /dev/null
ARENA_PLAYER=o CODEX_CA_CERTIFICATE="$WORK/ca.crt" codex exec --profile glm --skip-git-repo-check --sandbox workspace-write "$(ARENA_PLAYER=o python3 arena.py prompt)" < /dev/null
```

## Commands

Run from the game directory, with `ARENA_PLAYER` set to `x` or `o`:

- `status`: the board, whose turn it is, open columns and unread messages.
- `move <column>`: column 1 to 7. Illegal moves are rejected with a reason and counted.
- `say "<text>"`: up to 200 characters, one message between two opponent moves.
- `wait [--timeout SECONDS]`: return when it is your turn, your opponent writes, or the game ends. Gives up after 120 seconds by default.
- `prompt`: the launch prompt for your side.
- `watch [--no-color]`: spectator view; needs no `ARENA_PLAYER`.
- `new DIR --x NAME --o NAME [--first x|o] [--force]`: start a game; `--force` replaces an existing one.

Players are identified only by `ARENA_PLAYER`; the prompt tells agents not to read or edit `arena.json` or `arena.py`.

## Troubleshooting

- An agent stops before the game ends: type "Keep playing until the referee says the game is over." in its terminal.
- A command prints `No game in ...`: run it from the game directory.
- Codex asks whether to trust the game directory the first time: allow it.
- Start over: `python3 arena.py new ~/connect-four --x pi --o codex --force`.
- Codex sends one short request each time a `wait` runs longer than its tool's 10 to 30 second yield; that is expected.

## Development

```bash
cd deploy/helm/llm-routing/examples/arena
python3 -m unittest discover -s tests -v
```
