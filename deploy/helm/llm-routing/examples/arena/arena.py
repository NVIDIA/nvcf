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
