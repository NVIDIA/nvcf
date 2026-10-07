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


class GameFileTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='arena-')
        self.addCleanup(self.tmp.cleanup)
        self.dir = pathlib.Path(self.tmp.name)
        arena.save(self.dir, arena.new_state(NAMES, 'x', 1000.0))

    def test_locked_changes_are_saved(self):
        with arena.locked(self.dir) as state:
            arena.apply_move(state, 'x', 4, 1001.0)
        self.assertEqual(arena.load(self.dir)['moves'][0]['column'], 4)

    def test_an_error_inside_the_lock_saves_nothing(self):
        with self.assertRaises(RuntimeError), arena.locked(self.dir) as state:
            state['moves'].append({'player': 'x', 'column': 4, 'time': 1001.0})
            raise RuntimeError('stop')
        self.assertEqual(arena.load(self.dir)['moves'], [])

    def test_two_processes_do_not_lose_updates(self):
        code = ('import pathlib, sys\nsys.path.insert(0, sys.argv[1])\nimport arena\n'
                'for _ in range(200):\n'
                '    with arena.locked(pathlib.Path(sys.argv[2])) as state:\n'
                "        state['counter'] = state.get('counter', 0) + 1\n")
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE='1')
        workers = [subprocess.Popen([sys.executable, '-c', code, str(HERE), str(self.dir)], env=env) for _ in range(2)]
        self.assertEqual([worker.wait(timeout=60) for worker in workers], [0, 0])
        self.assertEqual(arena.load(self.dir)['counter'], 400)

    def test_missing_or_damaged_game_files_are_explained(self):
        with self.assertRaisesRegex(arena.ArenaError, 'No game in'):
            arena.load(self.dir/'elsewhere')
        with self.assertRaisesRegex(arena.ArenaError, 'No game in'):
            with arena.locked(self.dir/'elsewhere'):
                pass
        (self.dir/'arena.json').write_text('{not json')
        with self.assertRaisesRegex(arena.ArenaError, 'not valid JSON'):
            arena.load(self.dir)
        (self.dir/'arena.json').write_text('[]')
        with self.assertRaisesRegex(arena.ArenaError, 'not an arena game'):
            arena.load(self.dir)


class CommandTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='arena-')
        self.addCleanup(self.tmp.cleanup)
        self.dir = pathlib.Path(self.tmp.name)/'game'
        self.assertEqual(self.cli(None, 'new', str(self.dir), '--x', 'pi', '--o', 'codex')[0], 0)

    def no_sleep(self, seconds):
        self.fail('unexpected wait')

    def cli(self, player, *argv, now=1000.0, clock=None, sleep=None, directory=None):
        out = io.StringIO()
        env = {} if player is None else {'ARENA_PLAYER': player}
        code = arena.main(list(argv), directory=directory or self.dir, env=env, out=out,
                          now=lambda: now, clock=clock or (lambda: 0.0), sleep=sleep or self.no_sleep)
        return code, out.getvalue()

    def test_status_matches_the_documented_format(self):
        for player, column in zip('xoxoxoxo', [4, 4, 3, 5, 2, 1, 5, 4]):
            self.assertEqual(self.cli(player, 'move', str(column))[0], 0)
        self.assertEqual(self.cli('o', 'say', 'Column 4 is mine.'), (0, 'Message sent.\n'))
        code, text = self.cli('x', 'status')
        self.assertEqual(code, 0)
        self.assertEqual(text, '\n'.join([
            'Connect Four. You are X (pi). Opponent: O (codex).',
            'Move 9. Your turn.',
            '  1 2 3 4 5 6 7',
            '  . . . . . . .',
            '  . . . . . . .',
            '  . . . . . . .',
            '  . . . O . . .',
            '  . . . O X . .',
            '  O X X X O . .',
            'Last move: O in column 4.',
            'Open columns: 1 2 3 4 5 6 7',
            'codex says: "Column 4 is mine."',
            'Next: python3 arena.py move <column>',
        ]) + '\n')
        self.assertNotIn('says', self.cli('x', 'status')[1])

    def test_the_waiting_player_is_told_to_wait(self):
        self.cli('x', 'move', '4')
        text = self.cli('x', 'status')[1]
        self.assertIn('Move 2. O (codex) to move.', text)
        self.assertTrue(text.endswith('Next: python3 arena.py wait\n'))

    def test_bad_moves_are_rejected_counted_and_explained(self):
        cases = [('o', ['4'], 'It is not your turn.'), ('x', ['9'], 'Column must be 1 to 7.'),
                 ('x', ['abc'], 'Usage: python3 arena.py move <column>'), ('x', ['4.'], 'Usage:'),
                 ('x', ['column', '4'], 'Usage:'), ('x', [], 'Usage:')]
        for player, words, message in cases:
            with self.subTest(words=words):
                code, text = self.cli(player, 'move', *words)
                self.assertEqual(code, 1)
                self.assertTrue(text.startswith('Rejected: ' + message), text)
                self.assertIn('Open columns:', text)
        state = arena.load(self.dir)
        self.assertEqual((state['illegal'], state['moves']), ({'x': 5, 'o': 1}, []))

    def test_messages_may_be_unquoted_and_are_limited(self):
        self.assertEqual(self.cli('x', 'say', 'Good', 'luck', 'codex')[0], 0)
        self.assertEqual(arena.load(self.dir)['chat'][-1]['text'], 'Good luck codex')
        code, text = self.cli('x', 'say', 'again')
        self.assertEqual(code, 1)
        self.assertTrue(text.startswith('Rejected: You already sent a message.'))

    def test_wait_returns_at_once_on_your_turn(self):
        code, text = self.cli('x', 'wait')
        self.assertEqual(code, 0)
        self.assertIn('Move 1. Your turn.', text)

    def test_wait_returns_when_the_opponent_moves(self):
        self.cli('x', 'move', '4')
        code, text = self.cli('x', 'wait', sleep=lambda seconds: self.cli('o', 'move', '3'))
        self.assertEqual(code, 0)
        self.assertIn('Move 3. Your turn.', text)
        self.assertIn('Last move: O in column 3.', text)

    def test_wait_returns_when_the_opponent_sends_a_message(self):
        self.cli('x', 'move', '4')
        code, text = self.cli('x', 'wait', sleep=lambda seconds: self.cli('o', 'say', 'Nice opening.'))
        self.assertIn('codex says: "Nice opening."', text)
        self.assertTrue(text.endswith('Next: python3 arena.py wait\n'))

    def test_wait_returns_when_the_game_ends(self):
        for player, column in zip('xoxoxo', [1, 2, 1, 2, 1, 2]):
            self.cli(player, 'move', str(column))
        code, text = self.cli('o', 'wait', sleep=lambda seconds: self.cli('x', 'move', '1'))
        self.assertIn('Game over. X (pi) wins with four in a row at (row 1, column 1), (row 2, column 1), '
                      '(row 3, column 1), (row 4, column 1).', text)
        self.assertTrue(text.endswith('The game is over. Stop.\n'))
        self.assertNotIn('Open columns', text)

    def test_wait_gives_up_after_the_timeout(self):
        self.cli('x', 'move', '4')
        ticks = iter([0.0, 60.0, 120.0])
        code, text = self.cli('x', 'wait', '--timeout', '120', clock=lambda: next(ticks), sleep=lambda seconds: None)
        self.assertEqual(code, 0)
        self.assertTrue(text.endswith('Still O (codex) to move. Run python3 arena.py wait again.\n'))

    def test_prompt_matches_the_documented_text(self):
        self.assertEqual(self.cli('x', 'prompt'), (0, '\n'.join([
            'You are playing Connect Four as X against another AI agent (codex, playing O).',
            'The referee is `python3 arena.py` in this directory. Repeat until the game is over:',
            '1. Run `python3 arena.py wait`.',
            '2. When it says it is your turn, choose a column from "Open columns" and run',
            '   `python3 arena.py move <column>`.',
            'You may send one short message to your opponent per turn with',
            '`python3 arena.py say "<text>"`. Use only these commands. Do not read or edit',
            'arena.json or arena.py. When the referee says the game is over, report the result and stop.',
        ]) + '\n'))

    def test_new_copies_the_referee_and_prints_both_prompts(self):
        target = pathlib.Path(self.tmp.name)/'second'
        code, text = self.cli(None, 'new', str(target), '--x', 'pi', '--o', 'codex', '--first', 'o')
        self.assertEqual(code, 0)
        self.assertEqual((target/'arena.py').read_bytes(), (HERE/'arena.py').read_bytes())
        state = arena.load(target)
        self.assertEqual((state['names'], state['first'], state['moves']), (NAMES, 'o', []))
        self.assertIn('Prompt for X (pi), launched with ARENA_PLAYER=x:', text)
        self.assertIn('You are playing Connect Four as O against another AI agent (pi, playing X).', text)

    def test_new_does_not_replace_a_game_without_force(self):
        self.cli('x', 'move', '4')
        code, text = self.cli(None, 'new', str(self.dir), '--x', 'pi', '--o', 'codex')
        self.assertEqual((code, arena.load(self.dir)['moves'][0]['column']), (1, 4))
        self.assertIn('Add --force', text)
        self.assertEqual(self.cli(None, 'new', str(self.dir), '--x', 'pi', '--o', 'codex', '--force')[0], 0)
        self.assertEqual(arena.load(self.dir)['moves'], [])

    def test_new_from_the_copied_referee_replaces_the_game(self):
        copy_spec = importlib.util.spec_from_file_location('arena_copy', self.dir/'arena.py')
        copied = importlib.util.module_from_spec(copy_spec)
        copy_spec.loader.exec_module(copied)
        out = io.StringIO()
        self.assertEqual(copied.main(['new', str(self.dir), '--x', 'pi', '--o', 'codex', '--force'], out=out), 0, out.getvalue())

    def test_player_names_are_checked(self):
        for names in (['--x', 'pi bot', '--o', 'codex'], ['--x', 'pi', '--o', 'pi']):
            with self.subTest(names=names):
                code, text = self.cli(None, 'new', str(pathlib.Path(self.tmp.name)/'bad'), *names)
                self.assertEqual(code, 1)
                self.assertTrue(text.startswith('error: '), text)

    def test_player_identity_comes_from_the_environment(self):
        self.assertEqual(self.cli(None, 'status'), (1, 'error: Set ARENA_PLAYER to x or o when you launch the agent.\n'))
        self.assertIn('You are X (pi)', self.cli(' X ', 'status')[1])

    def test_commands_outside_the_game_directory_are_explained(self):
        code, text = self.cli('x', 'status', directory=pathlib.Path(self.tmp.name))
        self.assertEqual(code, 1)
        self.assertTrue(text.startswith('error: No game in '), text)


if __name__ == '__main__':
    unittest.main()
