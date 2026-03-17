"""Tests for autoissue.py functions."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

from autoissue import parse_complexity, snapshot_issues


class TestParseComplexity(unittest.TestCase):
    def _write_plan(self, content: str) -> Path:
        tmp = tempfile.NamedTemporaryFile(mode="w", suffix=".md", delete=False)
        try:
            tmp.write(content)
        finally:
            tmp.close()
        self.addCleanup(Path(tmp.name).unlink, missing_ok=True)
        return Path(tmp.name)

    def test_parse_complexity_none(self) -> None:
        path = self._write_plan(
            "### Complexity\n- Rating: none\n- Reason: already fixed\n"
        )
        self.assertEqual(parse_complexity(path), "none")

    def test_parse_complexity_simple(self) -> None:
        path = self._write_plan(
            "### Complexity\n- Rating: simple\n- Reason: small change\n"
        )
        self.assertEqual(parse_complexity(path), "simple")

    def test_parse_complexity_moderate(self) -> None:
        path = self._write_plan(
            "### Complexity\n- Rating: moderate\n- Reason: some work\n"
        )
        self.assertEqual(parse_complexity(path), "moderate")

    def test_parse_complexity_complex(self) -> None:
        path = self._write_plan(
            "### Complexity\n- Rating: complex\n- Reason: big refactor\n"
        )
        self.assertEqual(parse_complexity(path), "complex")

    def test_parse_complexity_unknown(self) -> None:
        path = self._write_plan("No rating here.\n")
        self.assertEqual(parse_complexity(path), "unknown")

    def test_parse_complexity_case_insensitive(self) -> None:
        path = self._write_plan("### Complexity\n- Rating: NONE\n")
        self.assertEqual(parse_complexity(path), "none")

    def test_parse_complexity_with_asterisks(self) -> None:
        path = self._write_plan("### Complexity\n- Rating: **none**\n")
        # The regex strips leading asterisks then captures the word
        self.assertEqual(parse_complexity(path), "none")


class TestSnapshotIssues(unittest.TestCase):
    def _make_gh_output(self, state: str, title: str = "Test Issue") -> str:
        return json.dumps(
            {
                "title": title,
                "body": "Test body.",
                "comments": [],
                "state": state,
            }
        )

    @patch("autoissue.run_cmd")
    def test_snapshot_issues_skips_closed(self, mock_run_cmd: MagicMock) -> None:
        closed_result = MagicMock()
        closed_result.returncode = 0
        closed_result.stdout = self._make_gh_output("CLOSED", "Already Fixed")

        open_result = MagicMock()
        open_result.returncode = 0
        open_result.stdout = self._make_gh_output("OPEN", "Needs Work")

        mock_run_cmd.side_effect = [closed_result, open_result]

        with tempfile.TemporaryDirectory() as tmp:
            work_dir = Path(tmp)
            titles = snapshot_issues(["42", "43"], work_dir)

            self.assertNotIn("42", titles)
            self.assertIn("43", titles)
            self.assertEqual(titles["43"], "Needs Work")
            self.assertFalse((work_dir / "issue-42.md").exists())
            self.assertTrue((work_dir / "issue-43.md").exists())

    @patch("autoissue.run_cmd")
    def test_snapshot_issues_includes_open(self, mock_run_cmd: MagicMock) -> None:
        open_result = MagicMock()
        open_result.returncode = 0
        open_result.stdout = self._make_gh_output("OPEN", "Open Issue")

        mock_run_cmd.return_value = open_result

        with tempfile.TemporaryDirectory() as tmp:
            work_dir = Path(tmp)
            titles = snapshot_issues(["10"], work_dir)

            self.assertIn("10", titles)
            self.assertEqual(titles["10"], "Open Issue")


if __name__ == "__main__":
    unittest.main()
