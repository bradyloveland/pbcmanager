"""next_run_time: when each schedule type fires next."""
import datetime
import unittest

from tests.helpers import app


def ts(*args):
    return datetime.datetime(*args).timestamp()


def at(value):
    return datetime.datetime.fromtimestamp(value)


class NextRunTests(unittest.TestCase):
    # 2026-09-30 is a Wednesday (weekday 2).
    NOW = ts(2026, 9, 30, 23, 54)

    def job(self, **schedule):
        return {"enabled": True, "schedule": schedule}

    def test_manual_and_disabled_never_run(self):
        self.assertIsNone(app.next_run_time(self.job(type="manual"), self.NOW))
        job = self.job(type="daily", time="02:00", days=list(range(7)))
        job["enabled"] = False
        self.assertIsNone(app.next_run_time(job, self.NOW))

    def test_daily_every_day(self):
        nxt = app.next_run_time(self.job(type="daily", time="02:00", days=list(range(7))), self.NOW)
        self.assertEqual(at(nxt), datetime.datetime(2026, 10, 1, 2, 0))

    def test_daily_selected_days(self):
        nxt = app.next_run_time(self.job(type="daily", time="03:00", days=[0, 2, 4]), self.NOW)
        self.assertEqual(at(nxt), datetime.datetime(2026, 10, 2, 3, 0))  # Friday

    def test_daily_later_today(self):
        nxt = app.next_run_time(self.job(type="daily", time="23:59", days=list(range(7))), self.NOW)
        self.assertEqual(at(nxt), datetime.datetime(2026, 9, 30, 23, 59))

    def test_exact_time_is_not_repeated(self):
        start = ts(2026, 10, 1, 2, 0)
        nxt = app.next_run_time(self.job(type="daily", time="02:00", days=list(range(7))), start)
        self.assertEqual(at(nxt), datetime.datetime(2026, 10, 2, 2, 0))

    def test_hourly(self):
        self.assertEqual(at(app.next_run_time(self.job(type="hourly", time="00:55", interval_hours=1), self.NOW)),
                         datetime.datetime(2026, 9, 30, 23, 55))
        self.assertEqual(at(app.next_run_time(self.job(type="hourly", time="00:15", interval_hours=6), self.NOW)),
                         datetime.datetime(2026, 10, 1, 0, 15))
        self.assertEqual(at(app.next_run_time(self.job(type="hourly", time="00:30", interval_hours=4),
                                              ts(2026, 10, 1, 9, 0))),
                         datetime.datetime(2026, 10, 1, 12, 30))


if __name__ == "__main__":
    unittest.main()
