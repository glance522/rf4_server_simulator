"""Access existing local game data without creating a new database."""
from contextlib import contextmanager
from pathlib import Path
import sqlite3


class LocalOptionsError(Exception):
    def __init__(self, status, message):
        self.status, self.message = status, message


@contextmanager
def game_database(path, *, writable=False):
    db = None
    try:
        mode = 'rw' if writable else 'ro'
        db = sqlite3.connect(Path(path).resolve().as_uri() + '?mode=' + mode, uri=True, timeout=5)
        db.execute('PRAGMA foreign_keys=ON')
        with db:
            yield db
    except sqlite3.Error:
        raise LocalOptionsError(503, '本地游戏数据库暂不可用') from None
    finally:
        if db is not None:
            db.close()
