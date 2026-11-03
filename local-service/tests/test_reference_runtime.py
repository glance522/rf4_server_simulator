"""Real Go service behind the authenticated Python gateway, with a copied save."""
from pathlib import Path
from contextlib import closing
import shutil
import sqlite3
import struct
import tempfile
import unittest
from uuid import UUID

from test_gateway_bridge import gateway
from test_local_server import Client, ROOT, decode, GuidArray
from rf4_service_process import BackendProcess


class ReferenceRuntimeTests(unittest.TestCase):
    def test_import_inventory_take_rod_and_restart(self):
        reference = ROOT.parent
        if not (reference / 'bin' / 'gameserver.exe').is_file() or not (ROOT / 'data' / 'characters').is_dir():
            self.skipTest('Requires locally supplied character snapshots and compiled services')
        with tempfile.TemporaryDirectory(prefix='rf4-runtime-') as temporary:
            data = Path(temporary)
            shutil.copytree(ROOT / 'data' / 'characters', data / 'characters')
            manager = BackendProcess(data)
            try:
                manager.start()
                with closing(sqlite3.connect(manager.database)) as db:
                    items_before = db.execute('SELECT COUNT(*) FROM item WHERE uid=7357').fetchone()[0]
                    self.assertGreaterEqual(items_before, 1021)
                    # Imported rods use the verified 157 model.
                    rod = db.execute('''SELECT guid FROM item WHERE uid=7357 AND type_id=157
                        AND durability>0 AND container IN ('backpack','rig') LIMIT 1''').fetchone()[0]
                    rod_id = UUID(bytes_le=bytes.fromhex(rod))
                    profile_before = db.execute('SELECT level,xp,silver,gold FROM profile WHERE uid=7357').fetchone()
                with gateway(manager.address, manager.psk) as (port, token):
                    client = Client(port, token)
                    try:
                        self.assertEqual(decode(client.call(10, (1, 1), ['4.0.25034'])), '4.0.25034')
                        profile = client.call(11, (3, 1))
                        self.assertEqual(profile[:5], b'\x01' + struct.pack('<i', 307))
                        ids = decode(client.call(12, (4, 4), [17, 0, 500]))
                        self.assertIsInstance(ids, GuidArray)
                        self.assertEqual(len(ids.values), 500)
                        self.assertEqual(len(decode(client.call(13, (4, 4), [17, 500, 500])).values), 399)
                        self.assertEqual(len(decode(client.call(14, (4, 4), [18, 0, 500])).values), 22)
                        taken = client.call(15, (17, 7), [rod_id])
                        self.assertIsInstance(taken, bytes)
                        self.assertEqual(taken[:5], b'\x01' + struct.pack('<i', 158))
                        self.assertEqual(UUID(bytes_le=taken[5:21]), rod_id)
                    finally:
                        client.close()
                manager.close()
                manager.start()
                with closing(sqlite3.connect(manager.database)) as db:
                    self.assertGreaterEqual(db.execute('SELECT COUNT(*) FROM item WHERE uid=7357').fetchone()[0], items_before)
                    self.assertEqual(db.execute('SELECT level,xp,silver,gold FROM profile WHERE uid=7357').fetchone(), profile_before)
            finally:
                manager.close()
