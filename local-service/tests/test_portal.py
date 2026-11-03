from pathlib import Path
import sys
import unittest
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from rf4_portal import asset

class PortalAssetsTests(unittest.TestCase):
    def test_supplied_design_assets_and_local_routes(self):
        status,body,kind=asset('/portal/')
        self.assertEqual(status,200)
        self.assertIn('个人中心'.encode(),body)
        self.assertIn(b'/portal/local.js',body)
        self.assertNotIn(b'Player_0001',body)
        status,body,kind=asset('/portal/local.css')
        self.assertEqual(status,200)
        self.assertIn(b'.av_mask::after',body)
        self.assertIsNone(asset('/api/membership'))
        self.assertIsNone(asset('/portal/api/login'))
    def test_traversal_and_missing_assets_never_return_account_files(self):
        for path in ['/portal/../local_server.py','/portal/%2e%2e/%2e%2e/data/local.sqlite3','/portal/missing.js']:
            self.assertEqual(asset(path)[0],404)
