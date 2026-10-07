#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Connect Four referee for two coding agents. See README.md."""
import argparse
import contextlib
import fcntl
import json
import os
import pathlib
import re
import shutil
import sys
import tempfile
import time

ROWS, COLUMNS = 6, 7
PLAYERS = ('x', 'o')
STATE, LOCK = 'arena.json', 'arena.lock'
MAX_MESSAGE = 200
POLL_SECONDS = 0.25
NAME = re.compile(r'[A-Za-z0-9_-]{1,20}')


class ArenaError(Exception):
    """A problem the caller can fix. Printed as one line, without a traceback."""


def other(player):
    return 'o' if player == 'x' else 'x'


def new_state(names, first, now):
    return {'version': 1, 'names': dict(names), 'first': first, 'moves': [], 'chat': [],
            'seen': {'x': 0, 'o': 0}, 'illegal': {'x': 0, 'o': 0},
            'result': None, 'winCells': [], 'started': now}


def board(moves):
    """Rows from top to bottom. Each cell is 'x', 'o' or None."""
    grid = [[None] * COLUMNS for _ in range(ROWS)]
    heights = [0] * COLUMNS
    for move in moves:
        column = move['column'] - 1
        grid[ROWS - 1 - heights[column]][column] = move['player']
        heights[column] += 1
    return grid


def open_columns(grid):
    return [column + 1 for column in range(COLUMNS) if grid[0][column] is None]


def turn(state):
    if state['result']:
        return None
    return other(state['moves'][-1]['player']) if state['moves'] else state['first']


def winning_cells(grid, row, column):
    """The line of four or more through a cell, as [row counted from the bottom, column], 1-based."""
    player = grid[row][column]
    for step_row, step_column in ((0, 1), (1, 0), (1, 1), (1, -1)):
        cells = [(row, column)]
        for sign in (1, -1):
            r, c = row + sign * step_row, column + sign * step_column
            while 0 <= r < ROWS and 0 <= c < COLUMNS and grid[r][c] == player:
                cells.append((r, c))
                r, c = r + sign * step_row, c + sign * step_column
        if len(cells) >= 4:
            return sorted([ROWS - r, c + 1] for r, c in cells)
    return []


def apply_move(state, player, column, now):
    """Play a move, or return why it is illegal without changing anything."""
    if state['result']:
        return 'The game is over.'
    if turn(state) != player:
        return 'It is not your turn.'
    if not 1 <= column <= COLUMNS:
        return 'Column must be 1 to 7.'
    if board(state['moves'])[0][column - 1] is not None:
        return 'Column ' + str(column) + ' is full.'
    state['moves'].append({'player': player, 'column': column, 'time': now})
    grid = board(state['moves'])
    row = next(r for r in range(ROWS) if grid[r][column - 1] is not None)
    cells = winning_cells(grid, row, column - 1)
    if cells:
        state['result'], state['winCells'] = player, cells
    elif len(state['moves']) == ROWS * COLUMNS:
        state['result'] = 'draw'
    return None


def post_message(state, player, text, now):
    """Add a chat message, or return why it is not allowed."""
    text = ' '.join(str(text).split())
    if state['result']:
        return 'The game is over.'
    if not text:
        return 'The message is empty.'
    if len(text) > MAX_MESSAGE:
        return 'Messages are limited to 200 characters.'
    replies = sum(1 for move in state['moves'] if move['player'] == other(player))
    if any(m['player'] == player and m['afterOpponentMoves'] == replies for m in state['chat']):
        return 'You already sent a message. Wait for your opponent to move.'
    state['chat'].append({'player': player, 'text': text, 'time': now, 'afterOpponentMoves': replies})
    return None


def load(directory):
    path = pathlib.Path(directory) / STATE
    if not path.exists():
        raise ArenaError('No game in ' + str(directory) + '. Run commands from the game directory.')
    try:
        state = json.loads(path.read_text())
    except ValueError:
        raise ArenaError(STATE + ' is not valid JSON. Start a new game with: python3 arena.py new DIR --force') from None
    if not isinstance(state, dict) or state.get('version') != 1:
        raise ArenaError(STATE + ' is not an arena game. Start a new game with: python3 arena.py new DIR --force')
    return state


def save(directory, state):
    """Replace the game file atomically, so readers never see a partial file."""
    fd, temporary = tempfile.mkstemp(prefix='.arena-', suffix='.json', dir=directory)
    with os.fdopen(fd, 'w') as handle:
        json.dump(state, handle, indent=1)
    os.replace(temporary, pathlib.Path(directory) / STATE)


@contextlib.contextmanager
def locked(directory):
    """Yield the game for changes under an exclusive lock, then save it. An exception saves nothing."""
    load(directory)
    with open(pathlib.Path(directory) / LOCK, 'a') as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)
        state = load(directory)
        yield state
        save(directory, state)


def label(state, player):
    return player.upper() + ' (' + state['names'][player] + ')'


def render_board(grid, show=None):
    show = show or (lambda cell, row, column: cell.upper() if cell else '.')
    lines = ['  ' + ' '.join(str(column) for column in range(1, COLUMNS + 1))]
    for row, cells in enumerate(grid):
        lines.append('  ' + ' '.join(show(cell, row, column) for column, cell in enumerate(cells)))
    return lines


def unseen(state, player):
    return [m for m in state['chat'][state['seen'][player]:] if m['player'] != player]


def view(state, player, timed_out=False):
    """The player's status. Marks the opponent's messages as seen."""
    grid = board(state['moves'])
    opponent, current = other(player), turn(state)
    lines = ['Connect Four. You are ' + label(state, player) + '. Opponent: ' + label(state, opponent) + '.']
    if state['result'] == 'draw':
        lines.append('Game over after ' + str(len(state['moves'])) + ' moves. It is a draw.')
    elif state['result']:
        cells = ', '.join('(row ' + str(r) + ', column ' + str(c) + ')' for r, c in state['winCells'])
        lines.append('Game over. ' + label(state, state['result']) + ' wins with four in a row at ' + cells +
                     '. Rows count from the bottom.')
    elif current == player:
        lines.append('Move ' + str(len(state['moves']) + 1) + '. Your turn.')
    else:
        lines.append('Move ' + str(len(state['moves']) + 1) + '. ' + label(state, opponent) + ' to move.')
    lines += render_board(grid)
    if state['moves']:
        last = state['moves'][-1]
        lines.append('Last move: ' + last['player'].upper() + ' in column ' + str(last['column']) + '.')
    if not state['result']:
        lines.append('Open columns: ' + ' '.join(str(column) for column in open_columns(grid)))
    for message in unseen(state, player):
        lines.append(state['names'][opponent] + ' says: "' + message['text'] + '"')
    state['seen'][player] = len(state['chat'])
    if state['result']:
        lines.append('The game is over. Stop.')
    elif current == player:
        lines.append('Next: python3 arena.py move <column>')
    elif timed_out:
        lines.append('Still ' + label(state, opponent) + ' to move. Run python3 arena.py wait again.')
    else:
        lines.append('Next: python3 arena.py wait')
    return '\n'.join(lines)


def prompt(state, player):
    opponent = other(player)
    return '\n'.join([
        'You are playing Connect Four as ' + player.upper() + ' against another AI agent (' +
        state['names'][opponent] + ', playing ' + opponent.upper() + ').',
        'The referee is `python3 arena.py` in this directory. Repeat until the game is over:',
        '1. Run `python3 arena.py wait`.',
        '2. When it says it is your turn, choose a column from "Open columns" and run',
        '   `python3 arena.py move <column>`.',
        'You may send one short message to your opponent per turn with',
        '`python3 arena.py say "<text>"`. Use only these commands. Do not read or edit',
        'arena.json or arena.py. When the referee says the game is over, report the result and stop.',
    ])


def wait(directory, player, timeout, clock, sleep):
    """Block until it is the player's turn, the opponent writes, the game ends or the timeout passes."""
    deadline = clock() + timeout
    state = load(directory)
    while not (state['result'] or turn(state) == player or unseen(state, player)) and clock() < deadline:
        sleep(POLL_SECONDS)
        state = load(directory)
    with locked(directory) as state:
        timed_out = not (state['result'] or turn(state) == player or unseen(state, player))
        return view(state, player, timed_out)


def new_game(target, names, first, force, now, out):
    for name in names.values():
        if not NAME.fullmatch(name):
            raise ArenaError('Player names use letters, digits, - and _, up to 20 characters: ' + repr(name))
    if names['x'] == names['o']:
        raise ArenaError('Give the two players different names.')
    target = pathlib.Path(target).expanduser().resolve()
    if (target / STATE).exists() and not force:
        raise ArenaError('A game already exists in ' + str(target) + '. Add --force to replace it.')
    target.mkdir(parents=True, exist_ok=True)
    source = pathlib.Path(__file__).resolve()
    if source != (target / 'arena.py').resolve():
        shutil.copyfile(source, target / 'arena.py')
    state = new_state(names, first, now)
    with open(target / LOCK, 'a') as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)
        save(target, state)
    print('New game in ' + str(target), file=out)
    for player in PLAYERS:
        print('\nPrompt for ' + label(state, player) + ', launched with ARENA_PLAYER=' + player + ':\n' +
              prompt(state, player), file=out)


def player_from(env):
    player = env.get('ARENA_PLAYER', '').strip().lower()
    if player not in PLAYERS:
        raise ArenaError('Set ARENA_PLAYER to x or o when you launch the agent.')
    return player


def parse_column(words):
    return int(words[0]) if len(words) == 1 and re.fullmatch(r'[0-9]+', words[0]) else None


def build_parser():
    parser = argparse.ArgumentParser(prog='arena.py', description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    commands.add_parser('status', help='show the board and whose turn it is')
    commands.add_parser('move', help='drop a piece in a column').add_argument('column', nargs='*')
    commands.add_parser('say', help='send your opponent one short message').add_argument('text', nargs='+')
    wait_command = commands.add_parser('wait', help='block until it is your turn')
    wait_command.add_argument('--timeout', type=float, default=120)
    commands.add_parser('prompt', help='print the launch prompt for ARENA_PLAYER')
    new = commands.add_parser('new', help='start a game in a directory')
    new.add_argument('directory')
    new.add_argument('--x', required=True, help='name of the player using X')
    new.add_argument('--o', required=True, help='name of the player using O')
    new.add_argument('--first', choices=PLAYERS, default='x')
    new.add_argument('--force', action='store_true', help='replace an existing game')
    return parser


def run(args, directory, env, out, now, clock, sleep):
    if args.command == 'new':
        new_game(args.directory, {'x': args.x, 'o': args.o}, args.first, args.force, now(), out)
        return 0
    player = player_from(env)
    if args.command == 'prompt':
        print(prompt(load(directory), player), file=out)
        return 0
    if args.command == 'wait':
        print(wait(directory, player, args.timeout, clock, sleep), file=out)
        return 0
    with locked(directory) as state:
        error = None
        if args.command == 'move':
            column = parse_column(args.column)
            if column is None:
                error = 'Usage: python3 arena.py move <column>, with a column from 1 to 7.'
            else:
                error = apply_move(state, player, column, now())
            if error:
                state['illegal'][player] += 1
        elif args.command == 'say':
            error = post_message(state, player, ' '.join(args.text), now())
        text = 'Message sent.' if args.command == 'say' and not error else view(state, player)
    print(('Rejected: ' + error + '\n' if error else '') + text, file=out)
    return 1 if error else 0


def main(argv=None, directory=None, env=None, out=None, now=time.time, clock=time.monotonic, sleep=time.sleep):
    args = build_parser().parse_args(argv)
    out = out or sys.stdout
    try:
        return run(args, pathlib.Path(directory or os.getcwd()), os.environ if env is None else env,
                   out, now, clock, sleep)
    except ArenaError as error:
        print('error: ' + str(error), file=out)
        return 1


if __name__ == '__main__':
    sys.exit(main())
