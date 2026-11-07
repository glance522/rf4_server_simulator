"""Optional real CLI check against a new isolated database."""
from contextlib import closing
import os
from pathlib import Path
import sqlite3
import sys
import tempfile
import time
import unittest
from uuid import uuid4
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from rf4_local_membership import membership
from rf4_local_options import LocalOptionsError

REFERENCE=os.environ.get('RF4_TEST_MEMBERSHIP_REFERENCE')


@unittest.skipUnless(REFERENCE,'Requires a built local-membership CLI reference directory')
class RealMembershipTests(unittest.TestCase):
    def test_real_cli_purchase_and_retry_only_charge_game_gold_once(self):
        with tempfile.TemporaryDirectory(prefix='rf4-member-cli-') as temporary:
            database=Path(temporary)/'gameplay.sqlite3';database.touch()
            with self.assertRaises(LocalOptionsError) as caught:
                membership(database,REFERENCE,42)
            self.assertEqual(caught.exception.status,404)
            with closing(sqlite3.connect(database)) as db:
                db.execute("INSERT INTO profile(uid,name,gold,silver,xp) VALUES(42,'fixture',2000,5000,12345)");db.commit()
            first=membership(database,REFERENCE,42)
            self.assertEqual(first['balance']['gold'],2000)
            request=str(uuid4())
            first=membership(database,REFERENCE,42,plan_id='premium_1d',request_id=request)
            again=membership(database,REFERENCE,42,plan_id='premium_1d',request_id=request)
            self.assertFalse(first['purchase']['replayed']);self.assertTrue(again['purchase']['replayed'])
            self.assertEqual(first['purchase']['expiresAt'],again['purchase']['expiresAt'])
            self.assertGreater(first['purchase']['expiresAt'],time.time())
            self.assertEqual(again['balance']['gold'],1900)
            with closing(sqlite3.connect(database)) as db:
                self.assertEqual(db.execute('SELECT gold,silver,xp FROM profile WHERE uid=42').fetchone(),(1900,5000,12345))
