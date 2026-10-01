"""Folder measurement and the size summary shown on the dashboard."""
import os
import tempfile
import threading
import unittest
from unittest import mock

from tests.helpers import app, fresh_dirs


def make_tree(root):
    os.makedirs(os.path.join(root, "a", "b"))
    for rel, size in (("one.bin", 1000), ("a/two.bin", 2500), ("a/b/three.bin", 40)):
        with open(os.path.join(root, rel), "wb") as f:
            f.write(b"x" * size)
    return 3540


class MeasureTests(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.files = make_tree(self.root)

    def test_walk_size_counts_files(self):
        self.assertEqual(app.walk_size(self.root), self.files)

    def test_du_includes_files(self):
        # du -b also counts directory entries, so it is at least the file total.
        self.assertGreaterEqual(app.measure_folder(self.root), self.files)

    def test_fallback_when_du_missing(self):
        with mock.patch.object(app.shutil, "which", return_value=None):
            self.assertEqual(app.measure_folder(self.root), self.files)

    def test_fallback_when_du_output_unusable(self):
        fake = mock.Mock(returncode=1, stdout="du: invalid option -- 'b'")
        with mock.patch.object(app.subprocess, "run", return_value=fake):
            self.assertEqual(app.measure_folder(self.root), self.files)


class FakeApp:
    def __init__(self):
        self.stopping = threading.Event()


class SummaryTests(unittest.TestCase):
    def setUp(self):
        fresh_dirs()
        self.tracker = app.SizeTracker(FakeApp())
        self.cfg = {"jobs": [
            {"id": "j1", "shares": [{"path": "/srv/media"}, {"path": "/srv/docs"}]},
            {"id": "j2", "shares": [{"path": "/srv/media/tv"}]},
            {"id": "j3", "shares": [{"path": "/srv/new"}]},
        ], "targets": []}
        self.tracker.state["sources"] = {
            "/srv/media": {"bytes": 1000, "measured": 100},
            "/srv/docs": {"bytes": 50, "measured": 200},
            "/srv/media/tv": {"bytes": 400, "measured": 150},
        }
        self.tracker.state["snapshots"] = {"j1": {"bytes": 900, "time": 5, "count": 3},
                                           "j2": {"bytes": 400, "time": 6, "count": 1}}

    def test_totals_skip_nested_folders(self):
        s = self.tracker.summary(self.cfg)
        self.assertEqual(s["source_total"], 1050)  # /srv/media/tv is inside /srv/media
        self.assertEqual(s["source_pending"], 1)   # /srv/new not measured yet
        self.assertEqual(s["source_measured"], 100)
        self.assertEqual(s["backup_total"], 1300)

    def test_per_job(self):
        jobs = self.tracker.summary(self.cfg)["jobs"]
        self.assertEqual(jobs["j1"]["source_bytes"], 1050)
        self.assertTrue(jobs["j1"]["source_complete"])
        self.assertEqual(jobs["j1"]["snapshot_count"], 3)
        self.assertIsNone(jobs["j3"]["source_bytes"])
        self.assertIsNone(jobs["j3"]["backup_bytes"])

    def test_nothing_measured(self):
        tracker = app.SizeTracker(FakeApp())
        s = tracker.summary(self.cfg)
        self.assertIsNone(s["source_total"])
        self.assertIsNone(s["backup_total"])

    def test_requests_force_refresh(self):
        self.tracker.request(sources=["/srv/media"])
        self.assertTrue(self.tracker._due("sources", "/srv/media", app.now(), 3600))
        self.assertFalse(self.tracker._due("sources", "/srv/media", app.now(), 3600))  # consumed
        self.tracker.request(targets="*")
        self.assertTrue(self.tracker._due("targets", "anything", app.now(), 3600))

    def test_state_persists(self):
        self.tracker._set("sources", "/x", {"bytes": 7, "measured": 1})
        again = app.SizeTracker(FakeApp())
        self.assertEqual(again.state["sources"]["/x"]["bytes"], 7)


if __name__ == "__main__":
    unittest.main()
