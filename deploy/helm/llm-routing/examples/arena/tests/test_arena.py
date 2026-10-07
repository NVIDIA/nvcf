# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Tests for the Connect Four referee. They need no cluster or network."""
import importlib.util
import io
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('arena', HERE/'arena.py')
arena = importlib.util.module_from_spec(spec)
spec.loader.exec_module(arena)

NAMES = {'x': 'pi', 'o': 'codex'}


def play(state, columns, start=1000.0):
    """Play alternating moves for whoever's turn it is; any rejection fails the test."""
    for index, column in enumerate(columns):
        error = arena.apply_move(state, arena.turn(state), column, start + index)
        assert error is None, (column, error)
    return state


class RulesTests(unittest.TestCase):
    def fresh(self, first='x'):
        return arena.new_state(NAMES, first, 1000.0)

    def test_pieces_stack_from_the_bottom(self):
        grid = arena.board([{'player': 'x', 'column': 4}, {'player': 'o', 'column': 4}])
        self.assertEqual((grid[5][3], grid[4][3]), ('x', 'o'))
        self.assertEqual(sum(cell is not None for row in grid for cell in row), 2)

    def test_first_player_moves_first_then_turns_alternate(self):
        state = self.fresh(first='o')
        self.assertEqual(arena.turn(state), 'o')
        play(state, [4])
        self.assertEqual(arena.turn(state), 'x')

    def test_four_in_a_row_wins_in_every_direction(self):
        cases = {
            'horizontal': ([1, 1, 2, 2, 3, 3, 4], [[1, 1], [1, 2], [1, 3], [1, 4]]),
            'vertical': ([1, 2, 1, 2, 1, 2, 1], [[1, 1], [2, 1], [3, 1], [4, 1]]),
            'rising': ([1, 2, 2, 3, 4, 3, 3, 4, 5, 4, 4], [[1, 1], [2, 2], [3, 3], [4, 4]]),
            'falling': ([7, 6, 6, 5, 4, 5, 5, 4, 3, 4, 4], [[1, 7], [2, 6], [3, 5], [4, 4]]),
        }
        for name, (columns, cells) in cases.items():
            with self.subTest(name):
                state = play(self.fresh(), columns)
                self.assertEqual((state['result'], state['winCells']), ('x', cells))
                self.assertIsNone(arena.turn(state))

    def test_a_full_board_without_four_in_a_row_is_a_draw(self):
        rows = ['xxooxxo', 'ooxxoox', 'xxooxxo', 'ooxxoox', 'xxooxxo', 'ooxxoox']  # bottom to top
        state = self.fresh()
        state['moves'] = [{'player': rows[row][column], 'column': column + 1, 'time': 1000.0}
                          for column in range(7) for row in range(6) if (row, column) != (5, 6)]
        self.assertEqual(state['moves'][-1]['player'], 'o')
        self.assertIsNone(arena.apply_move(state, 'x', 7, 2000.0))
        self.assertEqual((state['result'], state['winCells']), ('draw', []))

    def test_illegal_moves_are_rejected_without_changing_the_board(self):
        state = play(self.fresh(), [1, 1, 1, 1, 1, 1])
        before = json.dumps(state)
        for player, column, message in (('o', 2, 'It is not your turn.'), ('x', 1, 'Column 1 is full.'),
                                         ('x', 0, 'Column must be 1 to 7.'), ('x', 8, 'Column must be 1 to 7.')):
            with self.subTest(column=column):
                self.assertEqual(arena.apply_move(state, player, column, 2000.0), message)
                self.assertEqual(json.dumps(state), before)

    def test_no_moves_after_the_game_ends(self):
        state = play(self.fresh(), [1, 2, 1, 2, 1, 2, 1])
        self.assertEqual(arena.apply_move(state, 'o', 3, 2000.0), 'The game is over.')

    def test_one_message_per_player_between_opponent_moves(self):
        state = self.fresh()
        self.assertIsNone(arena.post_message(state, 'x', 'Good luck.', 1000.0))
        self.assertIn('already sent', arena.post_message(state, 'x', 'Again?', 1001.0))
        self.assertIsNone(arena.post_message(state, 'o', 'You too.', 1002.0))
        play(state, [4, 4])
        self.assertIsNone(arena.post_message(state, 'x', 'Center is mine.', 1003.0))
        self.assertEqual([m['text'] for m in state['chat']], ['Good luck.', 'You too.', 'Center is mine.'])

    def test_message_text_is_checked_and_flattened(self):
        state = self.fresh()
        self.assertEqual(arena.post_message(state, 'x', ' \n ', 1000.0), 'The message is empty.')
        self.assertEqual(arena.post_message(state, 'x', 'a' * 201, 1000.0), 'Messages are limited to 200 characters.')
        self.assertIsNone(arena.post_message(state, 'x', 'line one\nline two', 1000.0))
        self.assertEqual(state['chat'][-1]['text'], 'line one line two')
        play(state, [1, 2, 1, 2, 1, 2, 1])
        self.assertEqual(arena.post_message(state, 'o', 'gg', 2000.0), 'The game is over.')


if __name__ == '__main__':
    unittest.main()
